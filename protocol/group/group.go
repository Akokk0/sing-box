package group

import (
	"github.com/sagernet/sing-box/adapter"
)

// syncDependencies 把组成员的变化同步到出站管理器的依赖记账里。
//
// 不同步的话 Remove 会用一份陈旧的索引做判断：撤掉的节点删不掉，新加的节点又不受保护。
func syncDependencies(manager adapter.OutboundManager, tag string, members []string) error {
	updater, dynamic := manager.(adapter.DynamicOutboundManager)
	if !dynamic {
		// 内置的管理器实现了这个接口。换成别的实现时依赖记账会停在配置时的样子，
		// 那只影响 Remove 的拦阻判断，不影响组本身工作，所以不当成错误。
		return nil
	}
	return updater.UpdateDependencies(tag, members)
}
