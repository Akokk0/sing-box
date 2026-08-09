package provider

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
)

var _ adapter.OutboundProviderManager = (*Manager)(nil)

// Manager 管住所有订阅，并在任何一份变化之后重算依赖它的策略组的成员。
//
// 重算放在这里而不是各个 provider 里：一个组可以同时引用多份订阅，只有管理器看得全。
type Manager struct {
	logger    log.ContextLogger
	outbound  adapter.OutboundManager
	providers []*Provider
	byTag     map[string]*Provider
}

func NewManager(ctx context.Context, logFactory log.Factory, router adapter.Router, options []option.OutboundProvider) (*Manager, error) {
	manager := &Manager{
		logger:   logFactory.NewLogger("provider"),
		outbound: service.FromContext[adapter.OutboundManager](ctx),
		byTag:    make(map[string]*Provider, len(options)),
	}
	for i, providerOptions := range options {
		provider, err := New(ctx, router, logFactory, providerOptions, manager.refreshGroups)
		if err != nil {
			return nil, E.Cause(err, "outbound_providers[", i, "]")
		}
		if _, exists := manager.byTag[provider.Tag()]; exists {
			return nil, E.New("outbound_providers[", i, "]: duplicate tag: ", provider.Tag())
		}
		manager.providers = append(manager.providers, provider)
		manager.byTag[provider.Tag()] = provider
	}
	return manager, nil
}

func (m *Manager) Name() string {
	return "outbound provider"
}

// Start 在所有出站都起来之后才动手：provider 要往出站管理器里塞节点，还要去改策略组。
func (m *Manager) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStarted {
		return nil
	}
	for _, provider := range m.providers {
		if err := provider.Start(); err != nil {
			return E.Cause(err, "start provider[", provider.Tag(), "]")
		}
	}
	return nil
}

func (m *Manager) Close() error {
	var err error
	for _, provider := range m.providers {
		err = E.Append(err, provider.Close(), func(err error) error {
			return E.Cause(err, "close provider[", provider.Tag(), "]")
		})
	}
	return err
}

func (m *Manager) Providers() []adapter.OutboundProvider {
	providers := make([]adapter.OutboundProvider, 0, len(m.providers))
	for _, provider := range m.providers {
		providers = append(providers, provider)
	}
	return providers
}

func (m *Manager) Provider(tag string) (adapter.OutboundProvider, bool) {
	provider, found := m.byTag[tag]
	return provider, found
}

// refreshGroups 把每个引用了订阅的策略组的成员重算一遍。
func (m *Manager) refreshGroups() {
	for _, outbound := range m.outbound.Outbounds() {
		group, isProviderGroup := outbound.(adapter.ProviderOutboundGroup)
		if !isProviderGroup || len(group.ProviderTags()) == 0 {
			continue
		}
		var tags []string
		for _, providerTag := range group.ProviderTags() {
			provider, found := m.byTag[providerTag]
			if !found {
				m.logger.Error("group[", outbound.Tag(), "] refers to unknown provider[", providerTag, "]")
				continue
			}
			tags = append(tags, provider.Nodes()...)
		}
		if err := group.SetProviderNodes(tags); err != nil {
			m.logger.Error("refresh group[", outbound.Tag(), "]: ", err)
		}
	}
}
