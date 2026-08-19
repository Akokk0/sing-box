// Package mihomo 把 mihomo / clash 订阅里的 proxies 段转成 sing-box 出站。
//
// 转换刻意绕一圈 JSON：先把 mihomo 的字段摆成 sing-box 配置该有的样子，再交给
// option.Outbound 自己去反序列化。这样每种协议的字段校验、默认值、废弃字段提示全部
// 沿用配置文件那条路，不必在这里为每个协议手搭一份 Go 结构体——那种重复迟早会跟
// option 包走偏。
package mihomo

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"

	"gopkg.in/yaml.v3"
)

// Result 是一次转换的全部产出：出站，加上转换过程想告诉调用方的事。
//
// Skipped 和 Warnings 分开是因为粒度不同：前者是「这个节点没转成」，一条一个节点；
// 后者是关于整份订阅的，比如「整份文档解析不了，只读了 proxies 段」。
type Result struct {
	Outbounds []option.Outbound
	Skipped   []string
	Warnings  []string
}

// ToOptions 把订阅内容转成出站配置。
//
// 转不了的节点被跳过并记进 skipped，而不是让整份订阅报废——机场加一个尚未支持的协议
// 不该把用户钉死在旧配置上。但跳过的必须报出名字：节点悄悄少一批却没人看得见，
// 事后根本查不出原因。
//
// 整份内容解析不了则返回错误。那通常意味着拿到的根本不是订阅（机场返回的登录页或
// 限流页），这时候绝不能当成「零个节点」——那会把配置清空，等于断网。
func ToOptions(ctx context.Context, content []byte) (Result, error) {
	proxies, warnings, err := parseProxies(content)
	if err != nil {
		return Result{}, err
	}
	if len(proxies) == 0 {
		return Result{}, E.New("subscription contains no proxies")
	}
	result := Result{Warnings: warnings}
	for _, proxy := range proxies {
		outbound, convertErr := convert(ctx, proxy)
		if convertErr != nil {
			result.Skipped = append(result.Skipped, convertErr.Error())
			continue
		}
		result.Outbounds = append(result.Outbounds, outbound)
	}
	return result, nil
}

// parseProxies 取出订阅里的 proxies 段。
//
// 先整份解析——那是正常情况，也保住了 YAML 该有的语义。失败时才退一步，只把 proxies
// 段单独切出来再解析一次：机场生成的 hosts / rules / dns 那些段落坏掉，不该连累我们
// 唯一要读的东西。
//
// 退这一步的前提是「已经失败了」，所以它伤不到正常的订阅：走到这里时另一条路已经断了。
func parseProxies(content []byte) ([]map[string]any, []string, error) {
	var subscription struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	err := yaml.Unmarshal(content, &subscription)
	if err == nil {
		return subscription.Proxies, nil, nil
	}
	block, sections := proxiesBlock(content)
	if sections == 0 {
		// 报 YAML 词法错误在这里是帮倒忙：内容压根不是 clash 订阅，行号指向的东西
		// 毫无意义。多半是机场没认出 User-Agent，给了 base64 订阅或一张登录页。
		return nil, nil, E.New("not a clash subscription: no proxies section in the response; " +
			"the airport likely served another format, try setting user_agent")
	}
	if block == nil || yaml.Unmarshal(block, &subscription) != nil {
		// 坏的就是 proxies 段本身，没有任何东西可以信任。报原始错误——它的行号是相对
		// 整份文件的，用户能对得上。
		// 用 %w 而不是 E.Cause：E.Cause 的格式是 "<message>: <cause>"，把摘录当 message
		// 传进去，yaml 的报错就会拼在最后一行摘录的屁股后面，读起来像是订阅的那一行里
		// 写着这句报错——正是这个诊断本该消除的误解。%w 既能精确排版，又保住了 Unwrap，
		// errors.Is / errors.As 仍够得到底层那个 yaml 错误。
		return nil, nil, fmt.Errorf("parse subscription: %w%s", err, excerptAround(content, err.Error()))
	}
	return subscription.Proxies, []string{
		"the subscription does not parse as a whole (" + err.Error() +
			"); read the proxies section alone, everything else in it was ignored",
	}, nil
}

func convert(ctx context.Context, proxy map[string]any) (option.Outbound, error) {
	name, _ := proxy["name"].(string)
	if name == "" {
		return option.Outbound{}, E.New("proxy without a name")
	}
	proxyType, _ := proxy["type"].(string)

	port, err := parsePort(proxy["port"])
	if err != nil {
		return option.Outbound{}, E.Cause(err, "proxy ", name)
	}
	fields := map[string]any{
		"tag":         name,
		"server":      proxy["server"],
		"server_port": port,
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

// parsePort 接受 YAML 给出的两种端口写法。
//
// 加引号是合法的 YAML，野生订阅里确实有；mihomo 的解码器开着 WeaklyTypedInput，
// "8388" 照样当 8388 用。直接把字符串交给 option 反序列化的话，uint16 收到字符串就
// 报错，整个节点被静默跳过——机场那边看起来一切正常，用户这边少了一批线路。
func parsePort(value any) (uint16, error) {
	switch typed := value.(type) {
	case int:
		if typed < 0 || typed > math.MaxUint16 {
			return 0, E.New("port out of range: ", typed)
		}
		return uint16(typed), nil
	case string:
		parsed, err := strconv.ParseUint(typed, 10, 16)
		if err != nil {
			return 0, E.New("invalid port: ", typed)
		}
		return uint16(parsed), nil
	default:
		return 0, E.New("missing port")
	}
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
