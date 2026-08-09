package outbound

import (
	"context"
	"net"
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
)

// trackedOutbound 记着自己身上还有多少条活着的连接。
//
// 订阅更新时机场常常只是改了某个节点的服务器或密码，节点名一个字没动。直接关掉旧出站
// 会把正在走它的连接全部打断——用户可能正在游戏里。所以旧的先留着不关，新连接走新的，
// 等最后一条旧连接自己结束，才真正关掉它。
//
// 只包必须包的东西：
//   - Type / Tag / Network / Dependencies 由内嵌的接口提升，行为不变。
//   - 拨号被接管，只为了给连接计数。
//   - MultiplexEnabled 必须显式转发。内层不支持时回落 false——那与类型断言失败完全
//     等价，common/urltest 正是用断言问这件事的。
//   - Start / Close 必须显式转发：内嵌的是接口，具体类型的 Start 不会被提升，不写的话
//     被包住的出站永远不会被启动。
//
// 代理类出站没有一个实现 adapter.ConnectionHandler，所以不必转发它；哪天有了，路由会
// 退回 DialContext，语义仍然正确，只是少一次协议自己的优化。
type trackedOutbound struct {
	adapter.Outbound
	live      atomic.Int32
	retiring  atomic.Bool
	closeOnce sync.Once
	// onRetired 让退役完成的出站把自己从管理器的退役表里摘掉。
	onRetired func()
}

func newTrackedOutbound(outbound adapter.Outbound, onRetired func()) *trackedOutbound {
	return &trackedOutbound{Outbound: outbound, onRetired: onRetired}
}

func (o *trackedOutbound) Start(stage adapter.StartStage) error {
	return adapter.LegacyStart(o.Outbound, stage)
}

// Close 立刻关掉内层，不管还有没有连接。整个箱子关停时走这条路。
func (o *trackedOutbound) Close() error {
	var err error
	o.closeOnce.Do(func() {
		err = common.Close(o.Outbound)
	})
	return err
}

func (o *trackedOutbound) MultiplexEnabled() bool {
	if multiplex, ok := o.Outbound.(adapter.OutboundWithMultiplex); ok {
		return multiplex.MultiplexEnabled()
	}
	return false
}

func (o *trackedOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := o.Outbound.DialContext(ctx, network, destination)
	if err != nil {
		return nil, err
	}
	o.live.Add(1)
	return &trackedConn{Conn: conn, release: o.release}, nil
}

func (o *trackedOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	conn, err := o.Outbound.ListenPacket(ctx, destination)
	if err != nil {
		return nil, err
	}
	o.live.Add(1)
	return &trackedPacketConn{PacketConn: conn, release: o.release}, nil
}

// retire 宣布这个出站已经被换掉了：不再接新连接，等现有的走完就关。
//
// 先置标志再查计数，顺序不能反。反过来的话，查完计数到置标志之间最后一条连接刚好关闭，
// 两边都认为对方会收尾，这个出站就永远留着了。
func (o *trackedOutbound) retire() {
	o.retiring.Store(true)
	o.releaseIfDrained()
}

func (o *trackedOutbound) release() {
	o.live.Add(-1)
	o.releaseIfDrained()
}

func (o *trackedOutbound) releaseIfDrained() {
	if !o.retiring.Load() || o.live.Load() > 0 {
		return
	}
	var retired bool
	o.closeOnce.Do(func() {
		_ = common.Close(o.Outbound)
		retired = true
	})
	if retired && o.onRetired != nil {
		o.onRetired()
	}
}

// trackedConn 在连接关闭时给出站减一。Close 可能被调用多次，只能算一次。
//
// ReaderReplaceable / WriterReplaceable / Upstream 是这个代码库里包连接的惯例
// （见 common/interrupt/conn.go）：不写的话 sing 的 bufio 无法把它拆开直接对拼，
// 每条连接都会多一层拷贝。
type trackedConn struct {
	net.Conn
	closeOnce sync.Once
	release   func()
}

func (c *trackedConn) Close() error {
	c.closeOnce.Do(c.release)
	return c.Conn.Close()
}

func (c *trackedConn) ReaderReplaceable() bool { return true }
func (c *trackedConn) WriterReplaceable() bool { return true }
func (c *trackedConn) Upstream() any           { return c.Conn }

type trackedPacketConn struct {
	net.PacketConn
	closeOnce sync.Once
	release   func()
}

func (c *trackedPacketConn) Close() error {
	c.closeOnce.Do(c.release)
	return c.PacketConn.Close()
}

func (c *trackedPacketConn) ReaderReplaceable() bool { return true }
func (c *trackedPacketConn) WriterReplaceable() bool { return true }
func (c *trackedPacketConn) Upstream() any           { return bufio.NewPacketConn(c.PacketConn) }
