package adapter

// DynamicOutboundGroup 是成员可以在运行中变化的策略组。
//
// 订阅更新会增删节点，组必须跟着变。但组本身绝不能因此被重建——重建会打断正在走它的
// 每一条连接，而那几秒断流正是这套机制要消灭的东西。所以成员是就地替换的，没有被
// 增删的那些出站对象自始至终不会被碰到。
//
// 这是一个可选接口：拿到一个 OutboundGroup 时用类型断言问它支不支持。
type DynamicOutboundGroup interface {
	OutboundGroup
	// SetMembers 把组的成员整体换成 tags。tags 里的每一个都必须已经在出站管理器里，
	// 否则整次替换失败且组保持原样——宁可不换，也不要换成一个残缺的成员表。
	SetMembers(tags []string) error
}
