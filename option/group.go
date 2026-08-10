package option

import "github.com/sagernet/sing/common/json/badoption"

type SelectorOutboundOptions struct {
	// Subscriptions 让本组的成员来自这几份订阅，而不是写死在配置里。
	Subscriptions []string `json:"subscriptions,omitempty" reference:"subscription"`
	// Filter 从那些节点里挑出本组的成员，按声明顺序应用。
	Filter                    []GroupFilter `json:"filter,omitempty"`
	Outbounds                 []string      `json:"outbounds" reference:"outbound"`
	Default                   string        `json:"default,omitempty" reference:"outbound"`
	InterruptExistConnections bool          `json:"interrupt_exist_connections,omitempty"`
}

type URLTestOutboundOptions struct {
	// Subscriptions 让本组的成员来自这几份订阅，而不是写死在配置里。
	Subscriptions []string `json:"subscriptions,omitempty" reference:"subscription"`
	// Filter 从那些节点里挑出本组的成员，按声明顺序应用。
	Filter                    []GroupFilter      `json:"filter,omitempty"`
	Outbounds                 []string           `json:"outbounds" reference:"outbound"`
	URL                       string             `json:"url,omitempty"`
	Interval                  badoption.Duration `json:"interval,omitempty"`
	Tolerance                 uint16             `json:"tolerance,omitempty"`
	IdleTimeout               badoption.Duration `json:"idle_timeout,omitempty"`
	InterruptExistConnections bool               `json:"interrupt_exist_connections,omitempty"`
}
