package option

import (
	"github.com/sagernet/sing/common/json/badoption"
)

// OutboundProvider 是一份机场订阅：sing-box 自己去拉、自己转成出站、自己按 filter
// 分配给策略组。
//
// 节点在运行中增删替换，进程不重启、配置文件不重写，正在走没被改动的那些节点的连接
// 一条都不会断。
type OutboundProvider struct {
	Tag string `json:"tag"`
	// URL 是订阅地址。这里面的 token 等同机场的账号密码，配置文件的权限要照此对待。
	URL string `json:"url"`
	// Format 目前只有 mihomo（clash 的 proxies 段）。
	Format string `json:"format,omitempty" enum:"mihomo"`
	// Interval 是自动更新间隔，默认 24 小时。内容没变时整轮更新不会碰任何出站。
	Interval badoption.Duration `json:"interval,omitempty"`
	// DownloadDetour 指定用哪个出站去拉订阅。留空表示直连——sing-box 起不来的时候
	// 更新订阅是唯一的自救手段，这时候再绕回自己就是死锁。
	DownloadDetour string `json:"download_detour,omitempty"`
	// Path 是本地存档：启动时先用它把节点立刻装上，再去拉新的。
	// 路由器开机时网络往往还没通，而这份订阅正是连上网所需要的东西。
	Path string `json:"path,omitempty"`
	// ExcludeSkipped 为真时，转不了的节点连名字都不记。默认会留下名字：机场换协议时
	// 节点悄悄少一批却看不见，事后无从查起。
	ExcludeSkipped bool `json:"exclude_skipped,omitempty"`
}

// GroupFilter 从 provider 给出的节点里挑出本组的成员。
//
// 按声明顺序依次应用：include 只留下命中的，exclude 去掉命中的。keywords 里的每一条都是
// 正则，命中任意一条即算命中。
//
// 用的是 Go 的 RE2，不支持 lookahead —— mihomo 配置里那种 (?=...) 搬过来会直接报错，
// 必须改写成 include / exclude 两步。
type GroupFilter struct {
	Action   string                     `json:"action" enum:"include,exclude"`
	Keywords badoption.Listable[string] `json:"keywords"`
}
