//go:build with_clash_api

// Clash API 这一层没有被默认的 `go test ./...` 覆盖——它整个挂在 with_clash_api 标签
// 后面。发布用的二进制却是带这个标签编的，所以这里的东西只有到了面板上才会被发现。
package box_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

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
