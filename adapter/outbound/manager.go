package outbound

import (
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/taskmonitor"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
)

var _ adapter.OutboundManager = (*Manager)(nil)

type Manager struct {
	logger          log.ContextLogger
	registry        adapter.OutboundRegistry
	endpoint        adapter.EndpointManager
	defaultTag      string
	access          sync.RWMutex
	started         bool
	stage           adapter.StartStage
	outbounds       []adapter.Outbound
	outboundByTag   map[string]adapter.Outbound
	dependByTag     map[string][]string
	defaultOutbound adapter.Outbound
	// retiringOutbounds 是被换掉、但还有连接没走完的旧出站。
	retiringOutbounds       map[*trackedOutbound]string
	defaultOutboundFallback func() (adapter.Outbound, error)
}

func NewManager(logger logger.ContextLogger, registry adapter.OutboundRegistry, endpoint adapter.EndpointManager, defaultTag string) *Manager {
	return &Manager{
		logger:            logger,
		registry:          registry,
		endpoint:          endpoint,
		defaultTag:        defaultTag,
		outboundByTag:     make(map[string]adapter.Outbound),
		dependByTag:       make(map[string][]string),
		retiringOutbounds: make(map[*trackedOutbound]string),
	}
}

func (m *Manager) Initialize(defaultOutboundFallback func() (adapter.Outbound, error)) {
	m.defaultOutboundFallback = defaultOutboundFallback
}

func (m *Manager) Start(stage adapter.StartStage) error {
	m.access.Lock()
	if m.started && m.stage >= stage {
		panic("already started")
	}
	m.started = true
	m.stage = stage
	if stage == adapter.StartStateStart {
		if m.defaultTag != "" && m.defaultOutbound == nil {
			defaultEndpoint, loaded := m.endpoint.Get(m.defaultTag)
			if !loaded {
				m.access.Unlock()
				return E.New("default outbound not found: ", m.defaultTag)
			}
			m.defaultOutbound = defaultEndpoint
		}
		if m.defaultOutbound == nil {
			directOutbound, err := m.defaultOutboundFallback()
			if err != nil {
				m.access.Unlock()
				return E.Cause(err, "create direct outbound for fallback")
			}
			m.outbounds = append(m.outbounds, directOutbound)
			m.outboundByTag[directOutbound.Tag()] = directOutbound
			m.defaultOutbound = directOutbound
		}
		outbounds := m.outbounds
		m.access.Unlock()
		return m.startOutbounds(append(outbounds, common.Map(m.endpoint.Endpoints(), func(it adapter.Endpoint) adapter.Outbound { return it })...))
	} else {
		outbounds := m.outbounds
		m.access.Unlock()
		for _, outbound := range outbounds {
			name := "outbound/" + outbound.Type() + "[" + outbound.Tag() + "]"
			done := adapter.LogElapsed(m.logger, stage, " ", name)
			err := adapter.LegacyStart(outbound, stage)
			done()
			if err != nil {
				return E.Cause(err, stage, " ", name)
			}
		}
	}
	return nil
}

func (m *Manager) startOutbounds(outbounds []adapter.Outbound) error {
	monitor := taskmonitor.New(m.logger, C.StartTimeout)
	started := make(map[string]bool)
	for {
		canContinue := false
	startOne:
		for _, outboundToStart := range outbounds {
			outboundTag := outboundToStart.Tag()
			if started[outboundTag] {
				continue
			}
			dependencies := outboundToStart.Dependencies()
			for _, dependency := range dependencies {
				if !started[dependency] {
					continue startOne
				}
			}
			started[outboundTag] = true
			canContinue = true
			name := "outbound/" + outboundToStart.Type() + "[" + outboundTag + "]"
			if starter, isStarter := outboundToStart.(adapter.Lifecycle); isStarter {
				done := adapter.LogElapsed(m.logger, "start ", name)
				monitor.Start("start ", name)
				err := starter.Start(adapter.StartStateStart)
				monitor.Finish()
				done()
				if err != nil {
					return E.Cause(err, "start ", name)
				}
			} else if starter, isStarter := outboundToStart.(interface {
				Start() error
			}); isStarter {
				done := adapter.LogElapsed(m.logger, "start ", name)
				monitor.Start("start ", name)
				err := starter.Start()
				monitor.Finish()
				done()
				if err != nil {
					return E.Cause(err, "start ", name)
				}
			}
		}
		if len(started) == len(outbounds) {
			break
		}
		if canContinue {
			continue
		}
		currentOutbound := common.Find(outbounds, func(it adapter.Outbound) bool {
			return !started[it.Tag()]
		})
		var lintOutbound func(oTree []string, oCurrent adapter.Outbound) error
		lintOutbound = func(oTree []string, oCurrent adapter.Outbound) error {
			problemOutboundTag := common.Find(oCurrent.Dependencies(), func(it string) bool {
				return !started[it]
			})
			if common.Contains(oTree, problemOutboundTag) {
				return E.New("circular outbound dependency: ", strings.Join(oTree, " -> "), " -> ", problemOutboundTag)
			}
			m.access.Lock()
			problemOutbound := m.outboundByTag[problemOutboundTag]
			m.access.Unlock()
			if problemOutbound == nil {
				return E.New("dependency[", problemOutboundTag, "] not found for outbound[", oCurrent.Tag(), "]")
			}
			return lintOutbound(append(oTree, problemOutboundTag), problemOutbound)
		}
		return lintOutbound([]string{currentOutbound.Tag()}, currentOutbound)
	}
	return nil
}

func (m *Manager) Close() error {
	monitor := taskmonitor.New(m.logger, C.StopTimeout)
	m.access.Lock()
	if !m.started {
		m.access.Unlock()
		return nil
	}
	m.started = false
	outbounds := m.outbounds
	m.outbounds = nil
	// 退役中的出站已经不在 m.outbounds 里了。关停时必须一并收掉，否则它们和自己的
	// 后台协程会一直留到进程结束。
	retiring := make([]*trackedOutbound, 0, len(m.retiringOutbounds))
	for outbound := range m.retiringOutbounds {
		retiring = append(retiring, outbound)
	}
	m.retiringOutbounds = make(map[*trackedOutbound]string)
	m.access.Unlock()
	for _, outbound := range retiring {
		_ = outbound.Close()
	}
	var err error
	for _, outbound := range outbounds {
		if closer, isCloser := outbound.(io.Closer); isCloser {
			name := "outbound/" + outbound.Type() + "[" + outbound.Tag() + "]"
			done := adapter.LogElapsed(m.logger, "close ", name)
			monitor.Start("close ", name)
			err = E.Append(err, closer.Close(), func(err error) error {
				return E.Cause(err, "close ", name)
			})
			monitor.Finish()
			done()
		}
	}
	return nil
}

// Outbounds 返回一份快照。
//
// 必须是拷贝，不能是内部那份切片本身：Remove 和 Replace 都用
// append(m.outbounds[:i], m.outbounds[i+1:]...) 把后面的元素往前挪，改的是同一个底层
// 数组。调用方脱锁遍历时，那份数组正在它脚下被改写——同一个出站会被读到两次，或者
// 某一个凭空消失，而 -race 直接报 DATA RACE。
//
// 订阅让这件事从「几乎不会发生」变成了每天都在跑：机场撤掉一个节点就是一次 Remove，
// 而面板的 /proxies 恰好就是这么遍历这份列表的。
func (m *Manager) Outbounds() []adapter.Outbound {
	m.access.RLock()
	defer m.access.RUnlock()
	return slices.Clone(m.outbounds)
}

func (m *Manager) Outbound(tag string) (adapter.Outbound, bool) {
	m.access.RLock()
	outbound, found := m.outboundByTag[tag]
	m.access.RUnlock()
	if found {
		return outbound, true
	}
	return m.endpoint.Get(tag)
}

func (m *Manager) Default() adapter.Outbound {
	m.access.RLock()
	defer m.access.RUnlock()
	return m.defaultOutbound
}

func (m *Manager) Remove(tag string) error {
	m.access.Lock()
	defer m.access.Unlock()
	outbound, found := m.outboundByTag[tag]
	if !found {
		return os.ErrInvalid
	}
	delete(m.outboundByTag, tag)
	index := common.Index(m.outbounds, func(it adapter.Outbound) bool {
		return it == outbound
	})
	if index == -1 {
		panic("invalid inbound index")
	}
	m.outbounds = append(m.outbounds[:index], m.outbounds[index+1:]...)
	started := m.started
	if m.defaultOutbound == outbound {
		if len(m.outbounds) > 0 {
			m.defaultOutbound = m.outbounds[0]
			m.logger.Info("updated default outbound to ", m.defaultOutbound.Tag())
		} else {
			m.defaultOutbound = nil
		}
	}
	dependBy := m.dependByTag[tag]
	if len(dependBy) > 0 {
		return E.New("outbound[", tag, "] is depended by ", strings.Join(dependBy, ", "))
	}
	dependencies := outbound.Dependencies()
	for _, dependency := range dependencies {
		if len(m.dependByTag[dependency]) == 1 {
			delete(m.dependByTag, dependency)
		} else {
			m.dependByTag[dependency] = common.Filter(m.dependByTag[dependency], func(it string) bool {
				return it != tag
			})
		}
	}
	if started {
		return common.Close(outbound)
	}
	return nil
}

// UpdateDependencies 实现 adapter.DynamicOutboundManager。
func (m *Manager) UpdateDependencies(tag string, dependencies []string) error {
	m.access.Lock()
	defer m.access.Unlock()
	if _, found := m.outboundByTag[tag]; !found {
		return os.ErrInvalid
	}
	m.setDependencies(tag, dependencies)
	return nil
}

// setDependencies 把 tag 的反向边整体换成 dependencies。调用方必须持有写锁。
//
// 先摘干净再重新挂上，而不是逐条增删：一次成员替换里同时增和删的那些会互相盖掉。
// 也正因为是「整体换」，反复 Replace 同一个 tag 才不会让边越堆越多——每次替换都会
// 先把上一个出站留下的那些摘掉。
func (m *Manager) setDependencies(tag string, dependencies []string) {
	for dependency, dependBy := range m.dependByTag {
		remaining := common.Filter(dependBy, func(it string) bool {
			return it != tag
		})
		if len(remaining) == 0 {
			delete(m.dependByTag, dependency)
		} else {
			m.dependByTag[dependency] = remaining
		}
	}
	for _, dependency := range dependencies {
		m.dependByTag[dependency] = append(m.dependByTag[dependency], tag)
	}
}

// Retiring 实现 adapter.DynamicOutboundManager，返回还在等连接走完的出站 tag。
func (m *Manager) Retiring() []string {
	m.access.RLock()
	defer m.access.RUnlock()
	tags := make([]string, 0, len(m.retiringOutbounds))
	for _, tag := range m.retiringOutbounds {
		tags = append(tags, tag)
	}
	return tags
}

func (m *Manager) forgetRetired(outbound *trackedOutbound) {
	m.access.Lock()
	delete(m.retiringOutbounds, outbound)
	m.access.Unlock()
}

// Replace 实现 adapter.DynamicOutboundManager。
//
// 跟 Create 的区别只在被顶掉的那一个身上：Create 当场 common.Close 掉它，走着它的连接
// 全部瞬间死掉；Replace 让它退役——新连接一律走新的，旧的等自己最后一条连接结束才关。
// 机场只是改了某个节点的服务器或密码时,用户手里的游戏、下载、SSH 因此不会断。
//
// 只有经由 Replace 建出来的出站才数得清自己身上有多少条连接。被顶掉的若是配置里建的
// 那种,数不出来,只能照旧立刻关掉。
func (m *Manager) Replace(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, outboundType string, options any) error {
	if tag == "" {
		return os.ErrInvalid
	}
	created, err := m.registry.CreateOutbound(ctx, router, logger, tag, outboundType, options)
	if err != nil {
		return err
	}
	outbound := newTrackedOutbound(created, nil)
	outbound.onRetired = func() { m.forgetRetired(outbound) }

	if m.started {
		name := "outbound/" + outbound.Type() + "[" + tag + "]"
		for _, stage := range adapter.ListStartStages {
			done := adapter.LogElapsed(m.logger, stage, " ", name)
			err = adapter.LegacyStart(outbound, stage)
			done()
			if err != nil {
				return E.Cause(err, stage, " ", name)
			}
		}
	}

	m.access.Lock()
	previous, replaced := m.outboundByTag[tag]
	if replaced {
		index := common.Index(m.outbounds, func(it adapter.Outbound) bool {
			return it == previous
		})
		if index != -1 {
			m.outbounds = append(m.outbounds[:index], m.outbounds[index+1:]...)
		}
	}
	m.outbounds = append(m.outbounds, outbound)
	m.outboundByTag[tag] = outbound
	// 整体换而不是往上加：被顶掉的那个留下的反向边必须一起摘掉，否则订阅每天更新一次，
	// 同一个 tag 反复被 Replace，边就一直堆到进程结束。
	m.setDependencies(tag, outbound.Dependencies())
	if m.defaultOutbound == previous || tag == m.defaultTag || (m.defaultTag == "" && m.defaultOutbound == nil) {
		m.defaultOutbound = outbound
	}
	previousTracked, wasTracked := previous.(*trackedOutbound)
	if replaced && wasTracked && m.started {
		m.retiringOutbounds[previousTracked] = tag
	}
	m.access.Unlock()

	m.refreshDependents(tag)

	if !replaced {
		return nil
	}
	if wasTracked && m.started {
		// 必须在锁外：连接早已走完时 retire 会同步回调 forgetRetired，那里还要拿这把锁。
		previousTracked.retire()
	} else {
		err = common.Close(previous)
		if err != nil {
			return E.Cause(err, "close outbound/", previous.Type(), "[", tag, "]")
		}
	}
	return nil
}

// refreshDependents 让引用了 tag 的策略组重新解析自己的成员。
//
// 组里存的是出站对象而不是 tag。一个节点被换掉之后，组手里那个指针还指着旧对象，新连接
// 会继续打到机场已经废弃的那台服务器上。要求调用方在每次 Replace 之后自己记得刷新每个组
// 是个陷阱——漏一次就是线上事故——而管理器手上本来就有反向索引，知道谁引用了这个 tag。
func (m *Manager) refreshDependents(tag string) {
	m.access.RLock()
	dependents := make([]adapter.Outbound, 0, len(m.dependByTag[tag]))
	for _, dependentTag := range m.dependByTag[tag] {
		if dependent, found := m.outboundByTag[dependentTag]; found {
			dependents = append(dependents, dependent)
		}
	}
	m.access.RUnlock()

	// 必须在锁外：SetMembers 会回头调管理器解析 tag，还会更新依赖记账。
	for _, dependent := range dependents {
		group, dynamic := dependent.(adapter.DynamicOutboundGroup)
		if !dynamic {
			continue
		}
		if err := group.SetMembers(group.All()); err != nil {
			m.logger.Error("refresh group[", dependent.Tag(), "] after replacing outbound[", tag, "]: ", err)
		}
	}
}

func (m *Manager) Create(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, inboundType string, options any) error {
	if tag == "" {
		return os.ErrInvalid
	}
	outbound, err := m.registry.CreateOutbound(ctx, router, logger, tag, inboundType, options)
	if err != nil {
		return err
	}
	if m.started {
		name := "outbound/" + outbound.Type() + "[" + outbound.Tag() + "]"
		for _, stage := range adapter.ListStartStages {
			done := adapter.LogElapsed(m.logger, stage, " ", name)
			err = adapter.LegacyStart(outbound, stage)
			done()
			if err != nil {
				return E.Cause(err, stage, " ", name)
			}
		}
	}
	m.access.Lock()
	defer m.access.Unlock()
	if existsOutbound, loaded := m.outboundByTag[tag]; loaded {
		if m.started {
			err = common.Close(existsOutbound)
			if err != nil {
				return E.Cause(err, "close outbound/", existsOutbound.Type(), "[", existsOutbound.Tag(), "]")
			}
		}
		existsIndex := common.Index(m.outbounds, func(it adapter.Outbound) bool {
			return it == existsOutbound
		})
		if existsIndex == -1 {
			panic("invalid inbound index")
		}
		m.outbounds = append(m.outbounds[:existsIndex], m.outbounds[existsIndex+1:]...)
	}
	m.outbounds = append(m.outbounds, outbound)
	m.outboundByTag[tag] = outbound
	dependencies := outbound.Dependencies()
	for _, dependency := range dependencies {
		m.dependByTag[dependency] = append(m.dependByTag[dependency], tag)
	}
	if tag == m.defaultTag || (m.defaultTag == "" && m.defaultOutbound == nil) {
		m.defaultOutbound = outbound
		if m.started {
			m.logger.Info("updated default outbound to ", outbound.Tag())
		}
	}
	return nil
}
