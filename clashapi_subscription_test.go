//go:build with_clash_api

// Clash API 这一层没有被默认的 `go test ./...` 覆盖——它整个挂在 with_clash_api 标签
// 后面。发布用的二进制却是带这个标签编的，所以这里的东西只有到了面板上才会被发现。
package box_test

import (
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

// startBoxWithClashAPI 起一个带 Clash API 的箱子，返回 API 的 base URL。
func startBoxWithClashAPI(t *testing.T, options option.Options) string {
	t.Helper()
	port := reservePort(t)
	address := "127.0.0.1:" + strconv.Itoa(int(port))
	options.Experimental = &option.ExperimentalOptions{
		ClashAPI: &option.ClashAPIOptions{ExternalController: address},
	}
	startBox(t, options)

	baseURL := "http://" + address
	// 控制器是异步起的，等它开始应答再往下走。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if response, err := http.Get(baseURL + "/version"); err == nil {
			_ = response.Body.Close()
			return baseURL
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the clash api never came up")
	return ""
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	var decoded map[string]any
	if err = json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode %s: %v\nbody: %s", url, err, body)
	}
	return decoded
}

// 面板拿到 /providers/proxies 之后，会去读每个节点的 name / type / history 来画那一列。
// 我们原本把 proxies 填成一串裸 tag 字符串,面板读 proxy.name 得到 undefined,于是
// provider 卡片在、里面一个节点都显示不出来——正是实机上看到的样子。
//
// Clash 的这个字段是完整的 proxy 对象数组，和 /proxies 里的形状一样。
func TestClashAPIExposesSubscriptionNodesAsProxyObjects(t *testing.T) {
	subscription := startSubscriptionServer(t, "proxies:\n"+
		node("🇭🇰 Hong Kong 01", 10002)+
		node("🇯🇵 Japan 01", 10003))

	baseURL := startBoxWithClashAPI(t, option.Options{
		Subscriptions: []option.Subscription{{Tag: "airport", URL: subscription.url}},
		Outbounds: []option.Outbound{
			{Type: "direct", Tag: "direct"},
			{Type: "selector", Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})

	providers := getJSON(t, baseURL+"/providers/proxies")["providers"].(map[string]any)
	airport, found := providers["airport"].(map[string]any)
	require.True(t, found, "no provider named airport in %v", providers)

	// 面板靠这三个字段认出这是一份订阅。
	require.Equal(t, "airport", airport["name"])
	require.Equal(t, "Proxy", airport["type"])
	require.Equal(t, "HTTP", airport["vehicleType"])

	proxies, isList := airport["proxies"].([]any)
	require.True(t, isList, "proxies is %T, want a list", airport["proxies"])
	require.Len(t, proxies, 2)

	byName := make(map[string]map[string]any, len(proxies))
	for i, entry := range proxies {
		proxy, isObject := entry.(map[string]any)
		// 这里就是 bug：原来每个 entry 是字符串 "🇭🇰 Hong Kong 01"，不是对象。
		require.True(t, isObject, "proxies[%d] is %T, want an object", i, entry)
		name, hasName := proxy["name"].(string)
		require.True(t, hasName, "proxies[%d] has no name: %v", i, proxy)
		byName[name] = proxy
	}

	hk, found := byName["🇭🇰 Hong Kong 01"]
	require.True(t, found, "no 🇭🇰 Hong Kong 01 in %v", byName)
	// 面板用 type 画协议标签，用 history 画延迟曲线;history 缺席会让它整行渲染不出来。
	require.Equal(t, "Shadowsocks", hk["type"])
	require.NotNil(t, hk["history"])
	require.Contains(t, hk, "udp")
	require.Contains(t, byName, "🇯🇵 Japan 01")
}

// 面板上点「更新」走的是这个 PUT，成功之后它会重新拉一次 provider 列表。
func TestClashAPIUpdatesASubscription(t *testing.T) {
	subscription := startSubscriptionServer(t, "proxies:\n"+node("🇭🇰 Hong Kong 01", 10002))
	baseURL := startBoxWithClashAPI(t, option.Options{
		Subscriptions: []option.Subscription{{Tag: "airport", URL: subscription.url}},
		Outbounds: []option.Outbound{
			{Type: "direct", Tag: "direct"},
			{Type: "selector", Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})

	subscription.serve("proxies:\n" + node("🇭🇰 Hong Kong 01", 10002) + node("🇸🇬 Singapore 01", 10005))

	request, err := http.NewRequest(http.MethodPut, baseURL+"/providers/proxies/airport", nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusNoContent, response.StatusCode)

	providers := getJSON(t, baseURL+"/providers/proxies")["providers"].(map[string]any)
	proxies := providers["airport"].(map[string]any)["proxies"].([]any)
	require.Len(t, proxies, 2)
}

// 面板的 provider 卡片上有一行「更新于 X 前」，数据来自这里。
func TestClashAPIReportsWhenTheSubscriptionUpdated(t *testing.T) {
	subscription := startSubscriptionServer(t, "proxies:\n"+node("HK 01", 10002))
	baseURL := startBoxWithClashAPI(t, option.Options{
		Subscriptions: []option.Subscription{{Tag: "airport", URL: subscription.url}},
		Outbounds: []option.Outbound{
			{Type: "direct", Tag: "direct"},
			{Type: "selector", Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})

	airport := getJSON(t, baseURL+"/providers/proxies")["providers"].(map[string]any)["airport"].(map[string]any)
	raw, present := airport["updatedAt"].(string)
	require.True(t, present, "no updatedAt in %v", airport)
	updatedAt, err := time.Parse(time.RFC3339Nano, raw)
	require.NoError(t, err, "updatedAt %q is not RFC3339 — the dashboard cannot parse it", raw)
	require.WithinDuration(t, time.Now(), updatedAt, time.Minute)
}

// 机场把流量和到期放在 subscription-userinfo 响应头里,面板的 provider 卡片靠它
// 画「已用 / 总量」和到期日。
func TestClashAPIExposesSubscriptionTrafficInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("subscription-userinfo",
			"upload=455727941; download=6174315083; total=107374182400; expire=1848124800")
		_, _ = w.Write([]byte("proxies:\n" + node("HK 01", 10002)))
	}))
	t.Cleanup(server.Close)

	baseURL := startBoxWithClashAPI(t, option.Options{
		Subscriptions: []option.Subscription{{Tag: "airport", URL: server.URL}},
		Outbounds: []option.Outbound{
			{Type: "direct", Tag: "direct"},
			{Type: "selector", Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})

	airport := getJSON(t, baseURL+"/providers/proxies")["providers"].(map[string]any)["airport"].(map[string]any)
	raw, present := airport["subscriptionInfo"].(map[string]any)
	require.True(t, present, "no subscriptionInfo in %v", airport)
	// 字段名首字母大写：clash 生态里就是这个形状，面板照着这几个键读。
	require.Equal(t, float64(455727941), raw["Upload"])
	require.Equal(t, float64(6174315083), raw["Download"])
	require.Equal(t, float64(107374182400), raw["Total"])
	require.Equal(t, float64(1848124800), raw["Expire"])
}

// 多数机场根本不发那个头。这时候绝不能编一份全零的出来——面板会画成
// 「已用 0 / 总量 0」，看着就像套餐已经用光。
func TestClashAPIOmitsTrafficInfoWhenTheAirportSendsNone(t *testing.T) {
	subscription := startSubscriptionServer(t, "proxies:\n"+node("HK 01", 10002))
	baseURL := startBoxWithClashAPI(t, option.Options{
		Subscriptions: []option.Subscription{{Tag: "airport", URL: subscription.url}},
		Outbounds: []option.Outbound{
			{Type: "direct", Tag: "direct"},
			{Type: "selector", Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})

	airport := getJSON(t, baseURL+"/providers/proxies")["providers"].(map[string]any)["airport"].(map[string]any)
	require.NotContains(t, airport, "subscriptionInfo")
}

// startTLSProbeTarget 起一个本地 HTTPS 探测目标，并返回它的地址和 CA。
//
// 必须是 https：clashapi 会把 http:// 的测速目标丢掉换成默认的 gstatic，那样测试就
// 依赖外网了。自签证书靠把 CA 塞进 certificate 配置解决——urltest 取的是箱子自己的
// 根证书池。
func startTLSProbeTarget(t *testing.T) (url string, ca string) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	return server.URL, string(encoded)
}

// ssNodeYAML 造一个指向本地 shadowsocks 服务端的订阅节点。
func ssNodeYAML(name string, port uint16) string {
	return "  - name: \"" + name + "\"\n" +
		"    type: ss\n" +
		"    server: 127.0.0.1\n" +
		"    port: " + strconv.Itoa(int(port)) + "\n" +
		"    cipher: " + shadowsocksMethod + "\n" +
		"    password: " + shadowsocksPassword + "\n"
}

// 面板 Proxy Providers 页面上的「检查延迟」按钮走这个端点。
//
// 它原本是个空壳：返回 204，什么都不做。面板于是以为测完了，延迟栏却一直空着——
// 点多少次都一样。
func TestClashAPIHealthCheckMeasuresTheSubscriptionsNodes(t *testing.T) {
	probeURL, ca := startTLSProbeTarget(t)
	port := startShadowsocksServer(t)
	subscription := startSubscriptionServer(t, "proxies:\n"+ssNodeYAML("HK 01", port))

	baseURL := startBoxWithClashAPI(t, option.Options{
		Certificate:   &option.CertificateOptions{Certificate: []string{ca}},
		Subscriptions: []option.Subscription{{Tag: "airport", URL: subscription.url}},
		Outbounds: []option.Outbound{
			{Type: "direct", Tag: "direct"},
			{Type: "selector", Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})

	// 测速之前，延迟历史是空的。
	airport := getJSON(t, baseURL+"/providers/proxies")["providers"].(map[string]any)["airport"].(map[string]any)
	history := airport["proxies"].([]any)[0].(map[string]any)["history"].([]any)
	require.Empty(t, history, "history should start out empty")

	response, err := http.Get(baseURL + "/providers/proxies/airport/healthcheck?url=" + probeURL + "&timeout=5000")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusNoContent, response.StatusCode)

	// 测完之后，面板画延迟靠的就是这个 history。
	airport = getJSON(t, baseURL+"/providers/proxies")["providers"].(map[string]any)["airport"].(map[string]any)
	proxy := airport["proxies"].([]any)[0].(map[string]any)
	history = proxy["history"].([]any)
	require.Len(t, history, 1, "the health check left no delay history — the button does nothing")
	delay, ok := history[0].(map[string]any)["delay"].(float64)
	require.True(t, ok, "history entry has no delay: %v", history[0])
	require.Greater(t, delay, float64(0), "delay should be a real measurement")
}

// mihomo 在 provider 底下还挂了单个节点的两个端点，面板点某一个节点时会走这里。
func TestClashAPIExposesASingleNodeUnderTheSubscription(t *testing.T) {
	probeURL, ca := startTLSProbeTarget(t)
	port := startShadowsocksServer(t)
	subscription := startSubscriptionServer(t, "proxies:\n"+ssNodeYAML("HK01", port))

	baseURL := startBoxWithClashAPI(t, option.Options{
		Certificate:   &option.CertificateOptions{Certificate: []string{ca}},
		Subscriptions: []option.Subscription{{Tag: "airport", URL: subscription.url}},
		Outbounds: []option.Outbound{
			{Type: "direct", Tag: "direct"},
			{Type: "selector", Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})

	t.Run("node info", func(t *testing.T) {
		node := getJSON(t, baseURL+"/providers/proxies/airport/HK01")
		require.Equal(t, "HK01", node["name"])
		require.Equal(t, "Shadowsocks", node["type"])
	})

	t.Run("node delay", func(t *testing.T) {
		response, err := http.Get(baseURL + "/providers/proxies/airport/HK01/healthcheck?timeout=5000&url=" + url.QueryEscape(probeURL))
		require.NoError(t, err)
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		require.Equal(t, http.StatusOK, response.StatusCode, "body: %s", body)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(body, &decoded))
		require.Greater(t, decoded["delay"], float64(0))
	})

	// 这条路由挂在某一份订阅底下，就只能看到那份订阅的节点。
	// direct 确实在出站管理器里，但它不是这份订阅给的。
	t.Run("an outbound that is not from this subscription is not found", func(t *testing.T) {
		response, err := http.Get(baseURL + "/providers/proxies/airport/direct")
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusNotFound, response.StatusCode)
	})
}

// mihomo 的 provider 对象里 testUrl 和 expectedStatus 是无条件下发的（没有
// omitempty）。面板会去读它们，拿到 undefined 时前端一个属性访问就足以让那张卡片
// 整个渲染失败——表现就是点一下刷新，卡片闪一下就没了。
func TestClashAPIReportsTheHealthCheckContract(t *testing.T) {
	subscription := startSubscriptionServer(t, "proxies:\n"+node("HK 01", 10002))
	baseURL := startBoxWithClashAPI(t, option.Options{
		Subscriptions: []option.Subscription{{Tag: "airport", URL: subscription.url}},
		Outbounds: []option.Outbound{
			{Type: "direct", Tag: "direct"},
			{Type: "selector", Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})

	airport := getJSON(t, baseURL+"/providers/proxies")["providers"].(map[string]any)["airport"].(map[string]any)
	testURL, present := airport["testUrl"].(string)
	require.True(t, present, "no testUrl in %v", airport)
	require.NotEmpty(t, testURL)
	status, present := airport["expectedStatus"].(string)
	require.True(t, present, "no expectedStatus in %v", airport)
	require.NotEmpty(t, status)
}

// 面板点「刷新」而机场连不上时，503 的响应体是直接把 Update() 的错误发出去的。
// 订阅地址里的 token 等同机场的账号密码，而面板挂在局域网上、通常不带鉴权。
func TestClashAPIRefreshFailureDoesNotLeakTheSubscriptionURL(t *testing.T) {
	// 没人监听这个端口，拉取必定失败。
	address := "127.0.0.1:" + strconv.Itoa(int(reservePort(t)))
	secret := "http://" + address + "/api/v1/client/subscribe?token=SECRET-TOKEN"

	baseURL := startBoxWithClashAPI(t, option.Options{
		Subscriptions: []option.Subscription{{Tag: "airport", URL: secret}},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect, Tag: "direct"},
			{Type: C.TypeSelector, Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})

	request, err := http.NewRequest(http.MethodPut, baseURL+"/providers/proxies/airport", nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	require.NotContains(t, string(body), "SECRET-TOKEN", "the dashboard was handed the subscription token")
}
