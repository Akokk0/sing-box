package subscription

import (
	"errors"
	"net/url"

	E "github.com/sagernet/sing/common/exceptions"
)

// redactError 去掉错误里的订阅地址。
//
// 订阅地址的路径和查询串里就是机场的账号密码，而 Go 的 http 客户端把完整 URL 放进它
// 返回的每一个错误里。那些错误有两个去处，两个都不该看到 token：日志（路由器上通常
// 落盘或进 syslog），以及 Clash API 那个 503 的响应体——面板挂在局域网上，往往不带鉴权。
//
// 主机名保留。去掉它就只剩「连接被拒绝」，连是哪份订阅出的问题都看不出来，而主机名
// 本身不是凭据。
func redactError(err error) error {
	var urlError *url.Error
	if !errors.As(err, &urlError) {
		return err
	}
	return E.Cause(urlError.Err, urlError.Op, " ", redactURL(urlError.URL))
}

func redactURL(rawURL string) string {
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil || parsed.Host == "" {
		// 连 URL 都解析不了时宁可什么都不说，也不要把原文回显出去。
		return "subscription"
	}
	// Host 不含 userinfo，所以 https://user:pass@host/ 这种写法也不会漏。
	return parsed.Scheme + "://" + parsed.Host
}
