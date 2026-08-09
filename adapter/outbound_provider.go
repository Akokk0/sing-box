package adapter

// OutboundProvider 是一份订阅：它自己去拉、转成出站、在运行中把节点增删替换到出站
// 管理器里，全程不重启进程、不重写配置文件。
type OutboundProvider interface {
	Tag() string
	// Nodes 返回当前这份订阅提供的节点 tag，保持订阅里的原始顺序。
	Nodes() []string
	// Update 立刻拉一次。内容跟上次一样时什么都不做。
	Update() error
}

// OutboundProviderManager 管住所有 provider，并负责在任何一份订阅变化之后重算依赖它的
// 策略组的成员。
//
// 重算放在管理器而不是 provider 里：一个组可以引用多份订阅，只有管理器看得全。
type OutboundProviderManager interface {
	Lifecycle
	Providers() []OutboundProvider
	Provider(tag string) (OutboundProvider, bool)
}

// ProviderOutboundGroup 是成员来自订阅的策略组。
//
// 组只负责说清楚「我要哪些订阅的节点、怎么挑」，真正的挑选和下发由 provider 管理器做。
type ProviderOutboundGroup interface {
	DynamicOutboundGroup
	// ProviderTags 返回本组的成员取自哪几份订阅。
	ProviderTags() []string
	// SetProviderNodes 交给本组一份订阅节点的全集，由组自己按 filter 挑出成员。
	//
	// 挑选放在组里而不是管理器里：filter 是组的属性，让管理器代劳会把过滤规则和组的
	// 内部状态拆到两个地方，也会让 adapter 反过来依赖 protocol。
	SetProviderNodes(tags []string) error
}
