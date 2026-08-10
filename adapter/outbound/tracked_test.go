package outbound

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/stretchr/testify/require"
)

// blockingOutbound 的 DialContext 会停在 dialing 上等 release，好让测试精确地把
// retire() 插进「拨号已经开始、连接还没交出来」的那一瞬间。
type blockingOutbound struct {
	dialing chan struct{}
	release chan struct{}
	closed  atomic.Bool
}

func (o *blockingOutbound) Type() string           { return "block-test" }
func (o *blockingOutbound) Tag() string            { return "node" }
func (o *blockingOutbound) Network() []string      { return []string{"tcp"} }
func (o *blockingOutbound) Dependencies() []string { return nil }

func (o *blockingOutbound) Close() error {
	o.closed.Store(true)
	return nil
}

func (o *blockingOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	close(o.dialing)
	<-o.release
	client, server := net.Pipe()
	go func() {
		_, _ = io.Copy(io.Discard, server)
	}()
	return client, nil
}

func (o *blockingOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	panic("not used")
}

// 计数原本是拨号成功之后才加的，所以在拨号进行中这个出站看起来身上一条连接都没有。
// 订阅更新恰好在这时把它换掉，retire() 一看计数是 0 就当场关掉了内层——而调用方
// 下一刻拿到的正是一条建立在已关闭出站上的连接。退役的全部意义就是不打断这条连接。
func TestRetiringDoesNotCloseAnOutboundWithADialInFlight(t *testing.T) {
	inner := &blockingOutbound{
		dialing: make(chan struct{}),
		release: make(chan struct{}),
	}
	tracked := newTrackedOutbound(inner, nil)

	dialed := make(chan net.Conn, 1)
	go func() {
		conn, err := tracked.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.invalid:443"))
		require.NoError(t, err)
		dialed <- conn
	}()

	<-inner.dialing
	tracked.retire()
	require.False(t, inner.closed.Load(), "the outbound was closed while a dial was still in flight")

	close(inner.release)
	select {
	case conn := <-dialed:
		require.False(t, inner.closed.Load(), "the outbound was closed before its connection was handed over")
		require.NoError(t, conn.Close())
	case <-time.After(5 * time.Second):
		t.Fatal("the dial never finished")
	}

	// 连接关掉之后才该收尾。
	require.True(t, inner.closed.Load(), "the outbound never retired after its last connection ended")
}

// 拨号失败不能把计数留在高位，否则这个出站永远等不到退役。
func TestAFailedDialDoesNotKeepAnOutboundAlive(t *testing.T) {
	inner := &failingOutbound{}
	tracked := newTrackedOutbound(inner, nil)

	_, err := tracked.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.invalid:443"))
	require.Error(t, err)

	tracked.retire()
	require.True(t, inner.closed.Load(), "the outbound never retired, so a failed dial leaked a reference")
}

type failingOutbound struct {
	blockingOutbound
}

func (o *failingOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return nil, io.ErrUnexpectedEOF
}

// Replace 会给新出站的每个依赖挂上一条反向边，却从不摘掉被顶掉那个留下的。订阅每天
// 更新一次，同一个 tag 反复被 Replace，边就一直往上堆到进程结束。
func TestReplacingAnOutboundDoesNotAccumulateDependencyEdges(t *testing.T) {
	registry := &stubRegistry{dependencies: []string{"upstream"}}
	manager := NewManager(logger.NOP(), registry, nil, "")

	for range 3 {
		require.NoError(t, manager.Replace(context.Background(), nil, nil, "node", "block-test", nil))
	}

	require.Equal(t, []string{"node"}, manager.dependByTag["upstream"])
}

// stubRegistry 造出依赖固定的出站，好让 Replace 的记账被反复触发。
type stubRegistry struct {
	dependencies []string
}

func (r *stubRegistry) CreateOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, outboundType string, options any) (adapter.Outbound, error) {
	return &dependentOutbound{tag: tag, dependencies: r.dependencies}, nil
}

func (r *stubRegistry) CreateOptions(outboundType string) (any, bool) { return nil, false }

func (r *stubRegistry) OptionTypes() []string { return nil }

type dependentOutbound struct {
	blockingOutbound
	tag          string
	dependencies []string
}

func (o *dependentOutbound) Tag() string            { return o.tag }
func (o *dependentOutbound) Dependencies() []string { return o.dependencies }
