package group

import (
	"slices"

	"github.com/sagernet/sing-box/adapter"
)

// mergeMembers 把配置里点名的成员和订阅送来的那批合成一份成员表。
//
// 两者相加而不是后者替换前者：filter 管的是订阅送来的那批，配置里点名的那几个（常常是
// direct 或另一个组）不受它约束，也不该因为订阅刷新过一次就消失。
//
// 静态的排在前面——那是用户自己写下的次序，而 urltest 在还没有测速历史时取的就是第一个。
// 去重是必须的：一个组可以引用多份订阅，两家机场用同一个节点名并不稀奇，而重复的成员会
// 在面板上显示两遍，也会让 urltest 把同一个出站测两次。
func mergeMembers(static []string, fromSubscriptions []string) []string {
	merged := make([]string, 0, len(static)+len(fromSubscriptions))
	seen := make(map[string]bool, len(static)+len(fromSubscriptions))
	for _, tag := range slices.Concat(static, fromSubscriptions) {
		if seen[tag] {
			continue
		}
		seen[tag] = true
		merged = append(merged, tag)
	}
	return merged
}

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
