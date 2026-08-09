package adapter

import "time"

// Subscription 是一份订阅：它自己去拉、转成出站、在运行中把节点增删替换到出站
// 管理器里，全程不重启进程、不重写配置文件。
type Subscription interface {
	Tag() string
	// Nodes 返回当前这份订阅提供的节点 tag，保持订阅里的原始顺序。
	Nodes() []string
	// UpdatedAt 是当前这批节点的来源时间。从本地存档装上来的，报的是存档落盘的时刻，
	// 不是开机的时刻——否则每次重启都会显示「刚刚更新」，而那份存档可能已经很旧了。
	UpdatedAt() time.Time
	// Update 立刻拉一次。内容跟上次一样时什么都不做。
	Update() error
	// Info 是机场随订阅一起报回来的流量和到期信息。多数机场不报，那就是 nil。
	Info() *SubscriptionInfo
}

// SubscriptionInfo 是机场用 subscription-userinfo 响应头报回来的用量。
//
// 字段名首字母大写且不带 json tag 是有意为之：clash 生态里这个对象就是这个形状，
// 面板照着 Upload / Download / Total / Expire 读。改成小写它们就认不出来了。
type SubscriptionInfo struct {
	// Upload 和 Download 是已用流量，字节。
	Upload   int64
	Download int64
	// Total 是套餐总量，字节。0 表示机场没报。
	Total int64
	// Expire 是到期时间，Unix 秒。0 表示不限期或者没报。
	Expire int64
}

// SubscriptionManager 管住所有订阅，并负责在任何一份变化之后重算依赖它的策略组的成员。
//
// 重算放在管理器而不是各份订阅里：一个组可以引用多份订阅，只有管理器看得全。
type SubscriptionManager interface {
	Lifecycle
	Subscriptions() []Subscription
	Subscription(tag string) (Subscription, bool)
}

// SubscriptionOutboundGroup 是成员来自订阅的策略组。
//
// 组只负责说清楚「我要哪些订阅的节点、怎么挑」，真正的挑选和下发由订阅管理器做。
type SubscriptionOutboundGroup interface {
	DynamicOutboundGroup
	// SubscriptionTags 返回本组的成员取自哪几份订阅。
	SubscriptionTags() []string
	// SetSubscriptionNodes 交给本组一份订阅节点的全集，由组自己按 filter 挑出成员。
	//
	// 挑选放在组里而不是管理器里：filter 是组的属性，让管理器代劳会把过滤规则和组的
	// 内部状态拆到两个地方，也会让 adapter 反过来依赖 protocol。
	SetSubscriptionNodes(tags []string) error
}
