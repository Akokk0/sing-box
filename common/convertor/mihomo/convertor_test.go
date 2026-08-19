package mihomo_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sagernet/sing-box/common/convertor/mihomo"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

// 取自真实机场订阅的形状，凭据换成假的。机场把流量和到期信息也伪装成节点排在最前面，
// 那是策略组过滤要处理的事，转换这一层照样把它们当节点转出来。
const subscription = `
proxies:
  - name: "🇭🇰 Hong Kong 01"
    type: anytls
    server: hk1.example.invalid
    port: 15019
    password: FAKE-PASSWORD-NOT-REAL
    sni: hk1.example.invalid
    client-fingerprint: chrome
    fingerprint: 5A2FC2AF7BF0E4C55B0EB9A2E3B4E9A0D8C1F6B3A7E5D2C9B8A6F4E3D1C0B9A8
    alpn:
      - h2
      - http/1.1
    idle-session-check-interval: 30
    idle-session-timeout: 30
    min-idle-session: 5
  - name: "🇯🇵 Japan 02"
    type: ss
    server: jp2.example.invalid
    port: 8388
    cipher: chacha20-ietf-poly1305
    password: FAKE-PASSWORD-NOT-REAL
    udp: true
  - name: "🇸🇬 Singapore 03"
    type: trojan
    server: sg3.example.invalid
    port: 443
    password: FAKE-PASSWORD-NOT-REAL
    sni: sg3.example.invalid
    skip-cert-verify: true
  - name: "🇺🇸 United States 04"
    type: hysteria2
    server: us4.example.invalid
    port: 443
    password: FAKE-PASSWORD-NOT-REAL
`

func outboundByTag(t *testing.T, outbounds []option.Outbound, tag string) option.Outbound {
	t.Helper()
	for _, outbound := range outbounds {
		if outbound.Tag == tag {
			return outbound
		}
	}
	t.Fatalf("no outbound tagged %q in %d converted", tag, len(outbounds))
	return option.Outbound{}
}

func TestToOptions(t *testing.T) {
	ctx := include.Context(context.Background())
	result, err := mihomo.ToOptions(ctx, []byte(subscription))
	require.NoError(t, err)

	t.Run("anytls", func(t *testing.T) {
		outbound := outboundByTag(t, result.Outbounds, "🇭🇰 Hong Kong 01")
		require.Equal(t, C.TypeAnyTLS, outbound.Type)
		options := outbound.Options.(*option.AnyTLSOutboundOptions)
		require.Equal(t, "hk1.example.invalid", options.Server)
		require.Equal(t, uint16(15019), options.ServerPort)
		require.Equal(t, "FAKE-PASSWORD-NOT-REAL", options.Password)
		require.Equal(t, 5, options.MinIdleSession)
		// mihomo 的这两个字段是秒数，sing-box 要的是带单位的时长。
		require.Equal(t, "30s", options.IdleSessionCheckInterval.Build().String())
		require.Equal(t, "30s", options.IdleSessionTimeout.Build().String())

		tls := options.TLS
		require.NotNil(t, tls)
		require.True(t, tls.Enabled)
		require.Equal(t, "hk1.example.invalid", tls.ServerName)
		require.Equal(t, []string{"h2", "http/1.1"}, []string(tls.ALPN))
		require.NotNil(t, tls.UTLS)
		require.True(t, tls.UTLS.Enabled)
		require.Equal(t, "chrome", tls.UTLS.Fingerprint)
		// mihomo 的 fingerprint 是整证书的 SHA-256，对应 fork 里的 certificate_sha256。
		require.Len(t, tls.CertificateSHA256, 1)
	})

	t.Run("shadowsocks", func(t *testing.T) {
		outbound := outboundByTag(t, result.Outbounds, "🇯🇵 Japan 02")
		require.Equal(t, C.TypeShadowsocks, outbound.Type)
		options := outbound.Options.(*option.ShadowsocksOutboundOptions)
		require.Equal(t, "jp2.example.invalid", options.Server)
		require.Equal(t, uint16(8388), options.ServerPort)
		// mihomo 管它叫 cipher，sing-box 叫 method。
		require.Equal(t, "chacha20-ietf-poly1305", options.Method)
		require.Equal(t, "FAKE-PASSWORD-NOT-REAL", options.Password)
	})

	t.Run("trojan", func(t *testing.T) {
		outbound := outboundByTag(t, result.Outbounds, "🇸🇬 Singapore 03")
		require.Equal(t, C.TypeTrojan, outbound.Type)
		options := outbound.Options.(*option.TrojanOutboundOptions)
		require.Equal(t, "sg3.example.invalid", options.Server)
		require.NotNil(t, options.TLS)
		require.True(t, options.TLS.Enabled)
		require.Equal(t, "sg3.example.invalid", options.TLS.ServerName)
		// skip-cert-verify 对应 insecure。漏掉它会让本该报错的证书悄悄通过。
		require.True(t, options.TLS.Insecure)
	})

	// 转不了的节点不能让整份订阅报废——那会把用户钉在旧配置上。但必须报出名字，
	// 否则机场换协议时节点悄悄少一批，事后无从查起。
	t.Run("unsupported protocols are named, not fatal", func(t *testing.T) {
		require.Len(t, result.Outbounds, 3)
		require.Len(t, result.Skipped, 1)
		require.Contains(t, result.Skipped[0], "🇺🇸 United States 04")
		require.Contains(t, result.Skipped[0], "hysteria2")
	})
}

// 整份订阅解析不了是另一回事：那说明拿到的根本不是订阅（多半是机场返回的 HTML 错误页），
// 这时候必须失败，绝不能当成「零个节点」把配置清空。
func TestToOptionsRejectsSomethingThatIsNotASubscription(t *testing.T) {
	ctx := include.Context(context.Background())
	_, err := mihomo.ToOptions(ctx, []byte("<html><body>403 Forbidden</body></html>"))
	require.Error(t, err)
}

// 不加 t.Parallel()：include.Context 会写 naive.ConfigureHTTP3ListenerFunc 这个包级变量，
// 两个测试同时调它就是一次数据竞争。这个文件里已有的测试也都是串行的。
//
// YAML 里给端口加引号是合法写法，野生订阅里确实有。mihomo 的解码器开了
// WeaklyTypedInput，"443" 照样当 443 用；我们直接丢给 option 反序列化的话，
// uint16 收到字符串就报错，整个节点被静默跳过。
func TestToOptionsAcceptsAQuotedPort(t *testing.T) {
	result, err := mihomo.ToOptions(include.Context(context.Background()), []byte(`
proxies:
  - name: "quoted"
    type: ss
    server: jp.example.invalid
    port: "8388"
    cipher: chacha20-ietf-poly1305
    password: FAKE-PASSWORD-NOT-REAL
`))
	require.NoError(t, err)
	require.Empty(t, result.Skipped)
	require.Len(t, result.Outbounds, 1)
	require.Equal(t, uint16(8388), result.Outbounds[0].Options.(*option.ShadowsocksOutboundOptions).ServerPort)
}

// 端口根本不是个数时必须照旧跳过并说明原因，不能悄悄变成 0。
func TestToOptionsSkipsANonNumericPort(t *testing.T) {
	result, err := mihomo.ToOptions(include.Context(context.Background()), []byte(`
proxies:
  - name: "broken"
    type: ss
    server: jp.example.invalid
    port: "not-a-port"
    cipher: chacha20-ietf-poly1305
    password: FAKE-PASSWORD-NOT-REAL
`))
	require.NoError(t, err)
	require.Empty(t, result.Outbounds)
	require.Len(t, result.Skipped, 1)
	require.Contains(t, result.Skipped[0], "broken")
}

// 机场按 User-Agent 决定返回什么：认出 clash 就给 clash yaml，认不出就常给 base64 那种
// 订阅。后者根本不是 YAML，报出来的却是 "yaml: line 47: did not find expected key"——
// 用户拿着这句话完全无从下手。没有 proxies 段就直说不是 clash 订阅。
func TestToOptionsSaysSoWhenTheResponseIsNotAClashSubscription(t *testing.T) {
	ctx := include.Context(context.Background())
	// base64 订阅：一堆看着像 YAML 又不是 YAML 的行。
	body := strings.Repeat("dm1lc3M6Ly9leUpoWkdRaU9pSXhMakl1TXk0MElpd2ljRzl5ZENJNk5EUXpmUT09\n", 60)
	_, err := mihomo.ToOptions(ctx, []byte(body))
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a clash subscription",
		"应当直说拿到的不是 clash 订阅，而不是甩一句 YAML 词法错误")
}

// 真的是 clash 订阅、只是某一行写坏了时，光给行号不够——用户看不到那一行长什么样。
// 而且 yaml.v3 报的行号常常指向坏行的**前一行**（缩进写错时它指的是上一行的末尾），
// 所以要摘一小段窗口。节点密码绝不能跟着进日志。
func TestToOptionsShowsTheOffendingLinesWithoutLeakingSecrets(t *testing.T) {
	var body strings.Builder
	body.WriteString("proxies:\n")
	for i := 1; i <= 45; i++ {
		fmt.Fprintf(&body, "  - {name: \"JP %02d\", type: anytls, server: 1.1.1.1, port: 443, password: SUPER-SECRET-VALUE}\n", i)
	}
	// 缩进少一格：野生订阅里最常见的坏法，报出来的正是 "did not find expected key"。
	body.WriteString(" - {name: \"the-broken-one\", type: anytls, server: 1.1.1.1, port: 443, password: SUPER-SECRET-VALUE}\n")

	_, err := mihomo.ToOptions(include.Context(context.Background()), []byte(body.String()))
	require.Error(t, err)
	require.Contains(t, err.Error(), "did not find expected key")
	require.Contains(t, err.Error(), "the-broken-one", "要把出问题的那几行带出来")
	require.NotContains(t, err.Error(), "SUPER-SECRET-VALUE", "密码不能进日志")
}

// 机场返回的是一份完整的 mihomo 配置，而我们只看 proxies 段。其余段落是机场自己生成的，
// 坏掉是常事——真实现场就撞上过一个空的 hosts 段：
//
//	hosts:
//	  :
//	  :
//
// yaml.v3 在那两行空键上停住，整份订阅报废，节点一个都拿不到，路由器全网断掉。为了一个
// 我们从不读的段落赔上整份订阅，没有任何道理。
func TestToOptionsSurvivesGarbageOutsideTheProxiesSection(t *testing.T) {
	content := `dns:
  fake-ip-filter:
    - 'localhost.ptlogin2.qq.com'
    - '*.msftncsi.com'
hosts:
  : 
  : 

proxies:
  - name: "JP 01"
    type: anytls
    server: 127.0.0.1
    port: 443
    password: FAKE-PASSWORD-NOT-REAL
`
	result, err := mihomo.ToOptions(include.Context(context.Background()), []byte(content))
	require.NoError(t, err, "坏在 hosts 段里，不该拖垮 proxies")
	require.Len(t, result.Outbounds, 1)
	require.Equal(t, "JP 01", result.Outbounds[0].Tag)
	require.NotEmpty(t, result.Warnings, "退而求其次地只解析了 proxies 段，这件事必须说出来")
}

// 但坏在 proxies 段里就是另一回事了：那时候没有任何东西可以信任，必须失败，
// 绝不能当成「机场撤掉了所有节点」把配置清空。
func TestToOptionsStillFailsWhenTheProxiesSectionItselfIsBroken(t *testing.T) {
	content := `hosts:
  : 
proxies:
  - {name: "ok", type: anytls, server: 127.0.0.1, port: 443}
 - {name: "bad indent", type: anytls, server: 127.0.0.1, port: 443}
`
	_, err := mihomo.ToOptions(include.Context(context.Background()), []byte(content))
	require.Error(t, err)
}

// 坏掉的段落也可能排在 proxies 后面。切 proxies 段时必须在下一个顶格键处收手，
// 一路切到文件末尾就会把后面那坨坏东西一起带上，等于白退这一步。
func TestToOptionsSurvivesGarbageAfterTheProxiesSection(t *testing.T) {
	content := `proxies:
  - name: "JP 01"
    type: anytls
    server: 127.0.0.1
    port: 443
    password: FAKE-PASSWORD-NOT-REAL
hosts:
  : 
  : 
rules:
  - MATCH,DIRECT
`
	result, err := mihomo.ToOptions(include.Context(context.Background()), []byte(content))
	require.NoError(t, err, "坏在 proxies 后面的 hosts 段里，同样不该拖垮 proxies")
	require.Len(t, result.Outbounds, 1)
	require.Equal(t, "JP 01", result.Outbounds[0].Tag)
	require.NotEmpty(t, result.Warnings)
}

// 回退只解析 proxies 段这一招有个陷阱：切出来的那一份看着是完整的，实际可能只是其中一半。
// 机场模板把 proxies 拼了两次时，整份文档因重复键解析失败，而回退切到第二个 proxies 就
// 收手了——于是后一半节点凭空消失，还会被当成「机场撤掉了这些节点」把出站摘掉。
// 宁可整份失败保住上一次的好状态，也不能悄悄少给一半。
func TestToOptionsRefusesToRecoverHalfOfADuplicatedProxiesSection(t *testing.T) {
	content := "proxies:\n" +
		"  - {name: \"JP 01\", type: anytls, server: 127.0.0.1, port: 10001, password: FAKE}\n" +
		"proxies:\n" +
		"  - {name: \"HK 01\", type: anytls, server: 127.0.0.1, port: 10002, password: FAKE}\n"
	result, err := mihomo.ToOptions(include.Context(context.Background()), []byte(content))
	require.Error(t, err, "只切到一半的节点表绝不能当成成功")
	require.Empty(t, result.Outbounds)
}

// 摘录是要进日志的，而节点名几乎都是 emoji 和中文。按字节截断会切出半个字符，
// 让整条日志变成非法 UTF-8。
func TestToOptionsExcerptStaysValidUTF8(t *testing.T) {
	var body strings.Builder
	body.WriteString("proxies:\n")
	longName := "🇯🇵 " + strings.Repeat("日", 120)
	for range 3 {
		fmt.Fprintf(&body, "  - {name: %q, type: anytls, server: 1.1.1.1, port: 443, password: SECRET}\n", longName)
	}
	body.WriteString(" - {name: \"bad indent\", type: anytls, server: 1.1.1.1, port: 443}\n")

	_, err := mihomo.ToOptions(include.Context(context.Background()), []byte(body.String()))
	require.Error(t, err)
	require.True(t, utf8.ValidString(err.Error()), "摘录被按字节截断，切出了半个字符")
}

// yaml 的错误必须自成一行。作为 message 传给 E.Cause 时它会被拼到最后一行摘录的屁股后面，
// 读起来就像「订阅的第 N 行里写着这句报错」——正是这个诊断本该消除的误解。
func TestToOptionsKeepsTheCauseOffTheLastExcerptLine(t *testing.T) {
	content := `proxies:
  - {name: "ok", type: anytls, server: 127.0.0.1, port: 443}
 - {name: "bad indent", type: anytls, server: 127.0.0.1, port: 443}
`
	_, err := mihomo.ToOptions(include.Context(context.Background()), []byte(content))
	require.Error(t, err)
	for line := range strings.SplitSeq(err.Error(), "\n") {
		if strings.Contains(line, " | ") && strings.Contains(line, "yaml:") {
			t.Fatalf("yaml 错误被拼进了摘录行: %q", line)
		}
	}
}
