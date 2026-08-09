package group

import (
	"context"
	"net"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

func RegisterSelector(registry *outbound.Registry) {
	outbound.Register[option.SelectorOutboundOptions](registry, C.TypeSelector, NewSelector)
}

var (
	_ adapter.OutboundGroup           = (*Selector)(nil)
	_ adapter.DynamicOutboundGroup    = (*Selector)(nil)
	_ adapter.ProviderOutboundGroup   = (*Selector)(nil)
	_ adapter.ConnectionHandler       = (*Selector)(nil)
	_ adapter.PacketConnectionHandler = (*Selector)(nil)
)

type Selector struct {
	outbound.Adapter
	ctx        context.Context
	outbound   adapter.OutboundManager
	connection adapter.ConnectionManager
	logger     logger.ContextLogger
	// access 只护住成员表。数据面（DialContext / NewConnection）读的是 selected，
	// 那是个原子值——换成员不该给每一次拨号都加上一把锁。
	access                       sync.RWMutex
	tags                         []string
	providers                    []string
	filter                       []option.GroupFilter
	defaultTag                   string
	outbounds                    map[string]adapter.Outbound
	selected                     common.TypedValue[adapter.Outbound]
	history                      *urltest.HistoryStorage
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
}

func NewSelector(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.SelectorOutboundOptions) (adapter.Outbound, error) {
	outbound := &Selector{
		Adapter:                      outbound.NewAdapter(C.TypeSelector, tag, nil, options.Outbounds),
		ctx:                          ctx,
		outbound:                     service.FromContext[adapter.OutboundManager](ctx),
		connection:                   service.FromContext[adapter.ConnectionManager](ctx),
		logger:                       logger,
		tags:                         options.Outbounds,
		providers:                    options.Providers,
		filter:                       options.Filter,
		defaultTag:                   options.Default,
		outbounds:                    make(map[string]adapter.Outbound),
		history:                      service.PtrFromContext[urltest.HistoryStorage](ctx),
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: options.InterruptExistConnections,
	}
	if len(outbound.tags) == 0 && len(outbound.providers) == 0 {
		return nil, E.New("missing tags")
	}
	return outbound, nil
}

func (s *Selector) Network() []string {
	selected := s.selected.Load()
	if selected == nil {
		return []string{N.NetworkTCP, N.NetworkUDP}
	}
	return selected.Network()
}

func (s *Selector) Start() error {
	// 成员来自订阅时，启动这一刻还一个都没有。provider 会在出站全部起来之后把它们送进来。
	if len(s.tags) == 0 {
		return nil
	}
	outbounds, err := s.resolve(s.tags)
	if err != nil {
		return err
	}
	s.access.Lock()
	s.outbounds = outbounds
	s.access.Unlock()

	if s.Tag() != "" {
		cacheFile := service.FromContext[adapter.CacheFile](s.ctx)
		if cacheFile != nil {
			selected := cacheFile.LoadSelected(s.Tag())
			if selected != "" {
				detour, loaded := outbounds[selected]
				if loaded {
					s.selected.Store(detour)
					return nil
				}
			}
		}
	}

	if s.defaultTag != "" {
		detour, loaded := outbounds[s.defaultTag]
		if !loaded {
			return E.New("default outbound not found: ", s.defaultTag)
		}
		s.selected.Store(detour)
		return nil
	}

	s.selected.Store(outbounds[s.tags[0]])
	return nil
}

func (s *Selector) Now() string {
	selected := s.selected.Load()
	if selected == nil {
		s.access.RLock()
		defer s.access.RUnlock()
		if len(s.tags) == 0 {
			return ""
		}
		return s.tags[0]
	}
	return selected.Tag()
}

func (s *Selector) All() []string {
	s.access.RLock()
	defer s.access.RUnlock()
	return s.tags
}

// resolve 把 tag 表换成出站对象。任何一个找不到就整体失败，调用方据此保持原样。
func (s *Selector) resolve(tags []string) (map[string]adapter.Outbound, error) {
	outbounds := make(map[string]adapter.Outbound, len(tags))
	for i, tag := range tags {
		detour, loaded := s.outbound.Outbound(tag)
		if !loaded {
			return nil, E.New("outbound ", i, " not found: ", tag)
		}
		outbounds[tag] = detour
	}
	return outbounds, nil
}

// SetMembers 实现 adapter.DynamicOutboundGroup。
//
// 刻意不调 interruptGroup.Interrupt：换成员不是换选择，正在走本组的连接没有任何理由
// 被打断。只有当选中的那个节点自己从组里消失时才需要另选一个,而那时它的连接本来就
// 已经随着出站被摘掉而结束了。
func (s *Selector) SetMembers(tags []string) error {
	// 先在锁外解析：出站管理器有自己的锁，拿着本组的锁去调它是自找死锁。
	outbounds, err := s.resolve(tags)
	if err != nil {
		return err
	}

	s.access.Lock()
	s.tags = tags
	s.outbounds = outbounds
	s.access.Unlock()
	if err := syncDependencies(s.outbound, s.Tag(), tags); err != nil {
		return err
	}

	if len(tags) == 0 {
		// filter 一个都没匹配上，或者机场把这批节点全撤了。留着旧成员更糟——那些出站
		// 已经被摘掉了。空着并在拨号时明确报错，比悄悄打到死节点上强。
		s.selected.Store(nil)
		return nil
	}
	// 选中的那个可能：还在（但对象被换成了新的）、彻底没了、或者还没选过。
	// 只按 tag 判断在不在是不够的——节点被替换时 tag 一个字没变，而指针必须换。
	switch selected := s.selected.Load(); {
	case selected == nil:
		s.selected.Store(outbounds[tags[0]])
	case outbounds[selected.Tag()] == nil:
		s.selected.Store(outbounds[tags[0]])
	case outbounds[selected.Tag()] != selected:
		s.selected.Store(outbounds[selected.Tag()])
	}
	return nil
}

func (s *Selector) SelectOutbound(tag string) bool {
	s.access.RLock()
	detour, loaded := s.outbounds[tag]
	s.access.RUnlock()
	if !loaded {
		return false
	}
	if s.selected.Swap(detour) == detour {
		return true
	}
	if s.Tag() != "" {
		cacheFile := service.FromContext[adapter.CacheFile](s.ctx)
		if cacheFile != nil {
			err := cacheFile.StoreSelected(s.Tag(), tag)
			if err != nil {
				s.logger.Error("store selected: ", err)
			}
		}
	}
	s.interruptGroup.Interrupt(s.interruptExternalConnections)
	if s.history != nil {
		s.history.NotifyUpdated()
	}
	return true
}

func (s *Selector) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	selected := s.selected.Load()
	if selected == nil {
		return nil, E.New("group[", s.Tag(), "] has no members")
	}
	conn, err := selected.DialContext(ctx, network, destination)
	if err != nil {
		return nil, err
	}
	return s.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

func (s *Selector) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	selected := s.selected.Load()
	if selected == nil {
		return nil, E.New("group[", s.Tag(), "] has no members")
	}
	conn, err := selected.ListenPacket(ctx, destination)
	if err != nil {
		return nil, err
	}
	return s.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

func (s *Selector) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	selected := s.selected.Load()
	if outboundHandler, isHandler := selected.(adapter.ConnectionHandler); isHandler {
		outboundHandler.NewConnection(ctx, conn, metadata, onClose)
	} else {
		s.connection.NewConnection(ctx, selected, conn, metadata, onClose)
	}
}

func (s *Selector) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	selected := s.selected.Load()
	if outboundHandler, isHandler := selected.(adapter.PacketConnectionHandler); isHandler {
		outboundHandler.NewPacketConnection(ctx, conn, metadata, onClose)
	} else {
		s.connection.NewPacketConnection(ctx, selected, conn, metadata, onClose)
	}
}

// ProviderTags 实现 adapter.ProviderOutboundGroup。
func (s *Selector) ProviderTags() []string {
	return s.providers
}

// SetProviderNodes 实现 adapter.ProviderOutboundGroup：订阅变了之后重算本组成员。
func (s *Selector) SetProviderNodes(tags []string) error {
	selected, err := FilterTags(tags, s.filter)
	if err != nil {
		return err
	}
	return s.SetMembers(selected)
}

func RealTag(detour adapter.Outbound) string {
	if group, isGroup := detour.(adapter.OutboundGroup); isGroup {
		return group.Now()
	}
	return detour.Tag()
}
