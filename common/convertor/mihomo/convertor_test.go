package mihomo_test

import (
	"context"
	"testing"

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
	outbounds, skipped, err := mihomo.ToOptions(ctx, []byte(subscription))
	require.NoError(t, err)

	t.Run("anytls", func(t *testing.T) {
		outbound := outboundByTag(t, outbounds, "🇭🇰 Hong Kong 01")
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
		outbound := outboundByTag(t, outbounds, "🇯🇵 Japan 02")
		require.Equal(t, C.TypeShadowsocks, outbound.Type)
		options := outbound.Options.(*option.ShadowsocksOutboundOptions)
		require.Equal(t, "jp2.example.invalid", options.Server)
		require.Equal(t, uint16(8388), options.ServerPort)
		// mihomo 管它叫 cipher，sing-box 叫 method。
		require.Equal(t, "chacha20-ietf-poly1305", options.Method)
		require.Equal(t, "FAKE-PASSWORD-NOT-REAL", options.Password)
	})

	t.Run("trojan", func(t *testing.T) {
		outbound := outboundByTag(t, outbounds, "🇸🇬 Singapore 03")
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
		require.Len(t, outbounds, 3)
		require.Len(t, skipped, 1)
		require.Contains(t, skipped[0], "🇺🇸 United States 04")
		require.Contains(t, skipped[0], "hysteria2")
	})
}

// 整份订阅解析不了是另一回事：那说明拿到的根本不是订阅（多半是机场返回的 HTML 错误页），
// 这时候必须失败，绝不能当成「零个节点」把配置清空。
func TestToOptionsRejectsSomethingThatIsNotASubscription(t *testing.T) {
	ctx := include.Context(context.Background())
	_, _, err := mihomo.ToOptions(ctx, []byte("<html><body>403 Forbidden</body></html>"))
	require.Error(t, err)
}

// 不加 t.Parallel()：include.Context 会写 naive.ConfigureHTTP3ListenerFunc 这个包级变量，
// 两个测试同时调它就是一次数据竞争。这个文件里已有的测试也都是串行的。
//
// YAML 里给端口加引号是合法写法，野生订阅里确实有。mihomo 的解码器开了
// WeaklyTypedInput，"443" 照样当 443 用；我们直接丢给 option 反序列化的话，
// uint16 收到字符串就报错，整个节点被静默跳过。
func TestToOptionsAcceptsAQuotedPort(t *testing.T) {
	outbounds, skipped, err := mihomo.ToOptions(include.Context(context.Background()), []byte(`
proxies:
  - name: "quoted"
    type: ss
    server: jp.example.invalid
    port: "8388"
    cipher: chacha20-ietf-poly1305
    password: FAKE-PASSWORD-NOT-REAL
`))
	require.NoError(t, err)
	require.Empty(t, skipped)
	require.Len(t, outbounds, 1)
	require.Equal(t, uint16(8388), outbounds[0].Options.(*option.ShadowsocksOutboundOptions).ServerPort)
}

// 端口根本不是个数时必须照旧跳过并说明原因，不能悄悄变成 0。
func TestToOptionsSkipsANonNumericPort(t *testing.T) {
	outbounds, skipped, err := mihomo.ToOptions(include.Context(context.Background()), []byte(`
proxies:
  - name: "broken"
    type: ss
    server: jp.example.invalid
    port: "not-a-port"
    cipher: chacha20-ietf-poly1305
    password: FAKE-PASSWORD-NOT-REAL
`))
	require.NoError(t, err)
	require.Empty(t, outbounds)
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0], "broken")
}
