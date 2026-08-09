package subscription

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
)

var _ adapter.SubscriptionManager = (*Manager)(nil)

// Manager 管住所有订阅，并在任何一份变化之后重算依赖它的策略组的成员。
//
// 重算放在这里而不是各份订阅里：一个组可以同时引用多份订阅，只有管理器看得全。
type Manager struct {
	logger        log.ContextLogger
	outbound      adapter.OutboundManager
	subscriptions []*Subscription
	byTag         map[string]*Subscription
}

func NewManager(ctx context.Context, logFactory log.Factory, router adapter.Router, options []option.Subscription) (*Manager, error) {
	manager := &Manager{
		logger:   logFactory.NewLogger("subscription"),
		outbound: service.FromContext[adapter.OutboundManager](ctx),
		byTag:    make(map[string]*Subscription, len(options)),
	}
	for i, subscriptionOptions := range options {
		subscription, err := New(ctx, router, logFactory, subscriptionOptions, manager.refreshGroups)
		if err != nil {
			return nil, E.Cause(err, "subscriptions[", i, "]")
		}
		if _, exists := manager.byTag[subscription.Tag()]; exists {
			return nil, E.New("subscriptions[", i, "]: duplicate tag: ", subscription.Tag())
		}
		manager.subscriptions = append(manager.subscriptions, subscription)
		manager.byTag[subscription.Tag()] = subscription
	}
	return manager, nil
}

func (m *Manager) Name() string {
	return "subscription"
}

// Start 在所有出站都起来之后才动手：订阅要往出站管理器里塞节点，还要去改策略组。
func (m *Manager) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStarted {
		return nil
	}
	for _, subscription := range m.subscriptions {
		if err := subscription.Start(); err != nil {
			return E.Cause(err, "start subscription[", subscription.Tag(), "]")
		}
	}
	return nil
}

func (m *Manager) Close() error {
	var err error
	for _, subscription := range m.subscriptions {
		err = E.Append(err, subscription.Close(), func(err error) error {
			return E.Cause(err, "close subscription[", subscription.Tag(), "]")
		})
	}
	return err
}

func (m *Manager) Subscriptions() []adapter.Subscription {
	subscriptions := make([]adapter.Subscription, 0, len(m.subscriptions))
	for _, subscription := range m.subscriptions {
		subscriptions = append(subscriptions, subscription)
	}
	return subscriptions
}

func (m *Manager) Subscription(tag string) (adapter.Subscription, bool) {
	subscription, found := m.byTag[tag]
	return subscription, found
}

// refreshGroups 把每个引用了订阅的策略组的成员重算一遍。
func (m *Manager) refreshGroups() {
	for _, outbound := range m.outbound.Outbounds() {
		group, isSubscriptionGroup := outbound.(adapter.SubscriptionOutboundGroup)
		if !isSubscriptionGroup || len(group.SubscriptionTags()) == 0 {
			continue
		}
		var tags []string
		for _, subscriptionTag := range group.SubscriptionTags() {
			subscription, found := m.byTag[subscriptionTag]
			if !found {
				m.logger.Error("group[", outbound.Tag(), "] refers to unknown subscription[", subscriptionTag, "]")
				continue
			}
			tags = append(tags, subscription.Nodes()...)
		}
		if err := group.SetSubscriptionNodes(tags); err != nil {
			m.logger.Error("refresh group[", outbound.Tag(), "]: ", err)
		}
	}
}
