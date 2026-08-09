package adapter

import (
	"context"

	"github.com/sagernet/sing-box/log"
)

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

// DynamicOutboundManager 是能跟随组成员变化更新依赖记账的出站管理器。
//
// OutboundManager 内部有一份「谁依赖谁」的反向索引，Remove 靠它拦住还有人在用的出站。
// 那份索引原本只在 Create 时按配置里的静态成员建一次；成员一旦能在运行中变化，它就会
// 失真——机场撤掉的节点明明已经不在任何组里，却因为一条陈旧记录而删不掉。
//
// 同样是可选接口，用类型断言问。
type DynamicOutboundManager interface {
	OutboundManager
	// UpdateDependencies 把 tag 这个出站依赖的对象整体换成 dependencies。
	UpdateDependencies(tag string, dependencies []string) error

	// Replace 用新配置换掉一个出站，被顶掉的那个进入退役状态：新连接一律走新的，
	// 旧的等自己最后一条连接结束之后才被关闭。Create 在同样的场景下会当场关掉旧的,
	// 把正在走它的连接全部打断。
	Replace(ctx context.Context, router Router, logger log.ContextLogger, tag string, outboundType string, options any) error

	// Retiring 返回还在等连接走完的出站 tag。
	Retiring() []string
}
