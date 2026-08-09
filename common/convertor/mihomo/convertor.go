// Package mihomo 把 mihomo / clash 订阅里的 proxies 段转成 sing-box 出站。
//
// 转换刻意绕一圈 JSON：先把 mihomo 的字段摆成 sing-box 配置该有的样子，再交给
// option.Outbound 自己去反序列化。这样每种协议的字段校验、默认值、废弃字段提示全部
// 沿用配置文件那条路，不必在这里为每个协议手搭一份 Go 结构体——那种重复迟早会跟
// option 包走偏。
package mihomo

import (
	"context"
	"strconv"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"

	"github.com/sagernet/sing-box/option"

	"gopkg.in/yaml.v3"
)

// ToOptions 把订阅内容转成出站配置。
//
// 转不了的节点被跳过并记进 skipped，而不是让整份订阅报废——机场加一个尚未支持的协议
// 不该把用户钉死在旧配置上。但跳过的必须报出名字：节点悄悄少一批却没人看得见，
// 事后根本查不出原因。
//
// 整份内容解析不了则返回错误。那通常意味着拿到的根本不是订阅（机场返回的登录页或
// 限流页），这时候绝不能当成「零个节点」——那会把配置清空，等于断网。
func ToOptions(ctx context.Context, content []byte) (outbounds []option.Outbound, skipped []string, err error) {
	var subscription struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err = yaml.Unmarshal(content, &subscription); err != nil {
		return nil, nil, E.Cause(err, "parse subscription")
	}
	if len(subscription.Proxies) == 0 {
		return nil, nil, E.New("subscription contains no proxies")
	}
	for _, proxy := range subscription.Proxies {
		outbound, convertErr := convert(ctx, proxy)
		if convertErr != nil {
			skipped = append(skipped, convertErr.Error())
			continue
		}
		outbounds = append(outbounds, outbound)
	}
	return outbounds, skipped, nil
}

func convert(ctx context.Context, proxy map[string]any) (option.Outbound, error) {
	name, _ := proxy["name"].(string)
	if name == "" {
		return option.Outbound{}, E.New("proxy without a name")
	}
	proxyType, _ := proxy["type"].(string)

	fields := map[string]any{
		"tag":         name,
		"server":      proxy["server"],
		"server_port": proxy["port"],
	}
	switch proxyType {
	case "anytls":
		fields["type"] = "anytls"
		fields["password"] = proxy["password"]
		fields["tls"] = buildTLS(proxy)
		// mihomo 这两个字段是光秃秃的秒数，sing-box 要的是带单位的时长。
		copyDuration(fields, proxy, "idle-session-check-interval", "idle_session_check_interval")
		copyDuration(fields, proxy, "idle-session-timeout", "idle_session_timeout")
		copyIf(fields, proxy, "min-idle-session", "min_idle_session")
	case "ss", "shadowsocks":
		fields["type"] = "shadowsocks"
		// mihomo 管它叫 cipher。
		fields["method"] = proxy["cipher"]
		fields["password"] = proxy["password"]
	case "trojan":
		fields["type"] = "trojan"
		fields["password"] = proxy["password"]
		fields["tls"] = buildTLS(proxy)
	default:
		return option.Outbound{}, E.New("proxy ", name, ": unsupported protocol ", proxyType)
	}

	content, err := json.Marshal(fields)
	if err != nil {
		return option.Outbound{}, E.Cause(err, "proxy ", name)
	}
	var outbound option.Outbound
	if err = outbound.UnmarshalJSONContext(ctx, content); err != nil {
		return option.Outbound{}, E.Cause(err, "proxy ", name)
	}
	return outbound, nil
}

// buildTLS 把 mihomo 散落在顶层的那些 TLS 字段收拢成 sing-box 的 tls 段。
func buildTLS(proxy map[string]any) map[string]any {
	tls := map[string]any{"enabled": true}

	// mihomo 里 sni 和 servername 是同一件事的两种写法，视协议而定。
	for _, key := range []string{"sni", "servername"} {
		if name, ok := proxy[key].(string); ok && name != "" {
			tls["server_name"] = name
			break
		}
	}
	if skip, ok := proxy["skip-cert-verify"].(bool); ok {
		// 漏掉这一项会让本该被拒的证书悄悄通过。
		tls["insecure"] = skip
	}
	if alpn := stringList(proxy["alpn"]); len(alpn) > 0 {
		tls["alpn"] = alpn
	}
	// mihomo 的 fingerprint 是整张证书的 SHA-256（十六进制），对应本 fork 的
	// certificate_sha256。别跟 client-fingerprint 混了，那是 uTLS 指纹。
	if fingerprint, ok := proxy["fingerprint"].(string); ok && fingerprint != "" {
		tls["certificate_sha256"] = fingerprint
	}
	if fingerprint, ok := proxy["client-fingerprint"].(string); ok && fingerprint != "" {
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": fingerprint}
	}
	return tls
}

func copyIf(fields map[string]any, proxy map[string]any, from string, to string) {
	if value, ok := proxy[from]; ok {
		fields[to] = value
	}
}

func copyDuration(fields map[string]any, proxy map[string]any, from string, to string) {
	switch seconds := proxy[from].(type) {
	case int:
		fields[to] = strconv.Itoa(seconds) + "s"
	case float64:
		fields[to] = strconv.Itoa(int(seconds)) + "s"
	}
}

// stringList 接受 mihomo 里同一个字段的两种写法：单个字符串，或者字符串列表。
func stringList(value any) []string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case []any:
		list := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				list = append(list, text)
			}
		}
		return list
	}
	return nil
}
