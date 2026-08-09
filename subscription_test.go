package box_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

// startBox 在进程内起一个真箱子。这些行为只有在完整装配起来之后才成立——策略组、
// 出站管理器、路由器之间的接线正是这次要改的东西，拿假的替身测等于什么都没测。
// 返回的 context 是箱子自己的：运行时新建出站必须用它，出站构造要从里面取服务注册表。
func startBox(t *testing.T, options option.Options) (*box.Box, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(include.Context(context.Background()))
	options.Log = &option.LogOptions{Level: "warning"}
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		cancel()
		t.Fatalf("create box: %v", err)
	}
	if err = instance.Start(); err != nil {
		cancel()
		t.Fatalf("start box: %v", err)
	}
	t.Cleanup(func() {
		_ = instance.Close()
		cancel()
	})
	return instance, ctx
}

// 订阅更新时机场随时会多出一个节点。策略组必须能在运行中接纳它。
//
// 现在做不到：Selector.Start() 把 tag 一次性解析进 s.outbounds 之后就再没问过出站
// 管理器，运行时新建的出站永远进不了组。唯一的办法是整份配置重来一遍，而那正是
// 我们要消灭的那几秒断流。
func TestSelectorAcceptsAnOutboundAddedAtRuntime(t *testing.T) {
	instance, ctx := startBox(t, option.Options{
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect, Tag: "node-a"},
			{Type: C.TypeSelector, Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Outbounds: []string{"node-a"},
			}},
		},
	})
	outboundManager := instance.Outbound()

	err := outboundManager.Create(
		ctx,
		instance.Router(),
		instance.LogFactory().NewLogger("outbound/direct[node-b]"),
		"node-b",
		C.TypeDirect,
		&option.DirectOutboundOptions{},
	)
	if err != nil {
		t.Fatalf("create node-b at runtime: %v", err)
	}

	proxy, loaded := outboundManager.Outbound("proxy")
	if !loaded {
		t.Fatal("the selector vanished from the outbound manager")
	}
	group, dynamic := proxy.(adapter.DynamicOutboundGroup)
	if !dynamic {
		t.Fatalf("selector does not accept membership changes: %T", proxy)
	}
	if err = group.SetMembers([]string{"node-a", "node-b"}); err != nil {
		t.Fatalf("SetMembers: %v", err)
	}

	if members := group.All(); len(members) != 2 || members[0] != "node-a" || members[1] != "node-b" {
		t.Errorf("All() = %v, want [node-a node-b]", members)
	}
	selector, selectable := proxy.(interface{ SelectOutbound(tag string) bool })
	if !selectable {
		t.Fatalf("selector cannot select: %T", proxy)
	}
	if !selector.SelectOutbound("node-b") {
		t.Fatal("node-b was added to the group but cannot be selected")
	}
	if now := group.Now(); now != "node-b" {
		t.Errorf("Now() = %q, want node-b", now)
	}
}

// startEchoServer 起一个把收到的字节原样送回的 TCP 服务，用来代表「正在进行的会话」。
func startEchoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

// roundTrip 在连接上写一句再读回来，读不回原样就说明这条连接已经断了。
func roundTrip(t *testing.T, conn net.Conn, message string) {
	t.Helper()
	// 出站默认返回「早连接」：握手要到第一次写才发生，在那之前底下还没有真连接，
	// SetDeadline 会报 invalid argument。所以设两次，第一次尽力而为，写完那次才作数。
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatalf("write %q: %v", message, err)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	buffer := make([]byte, len(message))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatalf("read back %q: %v", message, err)
	}
	if string(buffer) != message {
		t.Fatalf("read %q, want %q", buffer, message)
	}
}

// 这条是整件事的目的：订阅更新时，正在走这个组的连接一条都不能断。
// 用户感知到的「断一下」——游戏掉线、下载中断、SSH 卡死——就发生在这里。
//
// 成员表变了不等于选择变了。没有被增删的那个出站对象自始至终不该被碰到，所以走着它的
// 连接根本不该知道发生过什么。
func TestMembershipChangeDoesNotDisturbLiveConnections(t *testing.T) {
	echoAddress := startEchoServer(t)
	instance, ctx := startBox(t, option.Options{
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect, Tag: "node-a"},
			{Type: C.TypeSelector, Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Outbounds: []string{"node-a"},
				// 就算显式要求打断，换成员也不该触发它——那是换选择时才有的语义。
				InterruptExistConnections: true,
			}},
		},
	})
	outboundManager := instance.Outbound()
	proxy, _ := outboundManager.Outbound("proxy")

	conn, err := proxy.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(echoAddress))
	if err != nil {
		t.Fatalf("dial through the group: %v", err)
	}
	defer conn.Close()
	roundTrip(t, conn, "before the subscription update")

	// 机场加了一个节点。
	err = outboundManager.Create(ctx, instance.Router(),
		instance.LogFactory().NewLogger("outbound/direct[node-b]"),
		"node-b", C.TypeDirect, &option.DirectOutboundOptions{})
	if err != nil {
		t.Fatalf("create node-b: %v", err)
	}
	group := proxy.(adapter.DynamicOutboundGroup)
	if err = group.SetMembers([]string{"node-a", "node-b"}); err != nil {
		t.Fatalf("SetMembers after adding node-b: %v", err)
	}
	roundTrip(t, conn, "survived a node being added")

	// 机场又把它撤了。走 node-a 的这条连接跟这件事毫无关系。
	if err = group.SetMembers([]string{"node-a"}); err != nil {
		t.Fatalf("SetMembers after dropping node-b: %v", err)
	}
	if err = outboundManager.Remove("node-b"); err != nil {
		t.Fatalf("remove node-b: %v", err)
	}
	roundTrip(t, conn, "survived a node being removed")
}

// startProbeTarget 起一个永远回 204 的本地服务，给 urltest 当探测目标。
// 用真实的 https://www.gstatic.com/generate_204 会让测试依赖外网，又慢又飘。
func startProbeTarget(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// urltest 组同样要能在运行中增删成员——而且它比 selector 难：成员表被后台的健康检查
// 循环持有，选出来的那个还被缓存在 selectedOutboundTCP/UDP 里。
//
// 和 selector 一样，走着某个没被动过的节点的连接不能因为换成员而断。
func TestURLTestAcceptsMembersAddedAtRuntimeWithoutDisturbingConnections(t *testing.T) {
	echoAddress := startEchoServer(t)
	instance, ctx := startBox(t, option.Options{
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect, Tag: "node-a"},
			{Type: C.TypeURLTest, Tag: "proxy", Options: &option.URLTestOutboundOptions{
				Outbounds:                 []string{"node-a"},
				URL:                       startProbeTarget(t),
				InterruptExistConnections: true,
			}},
		},
	})
	outboundManager := instance.Outbound()
	proxy, _ := outboundManager.Outbound("proxy")

	conn, err := proxy.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(echoAddress))
	if err != nil {
		t.Fatalf("dial through the group: %v", err)
	}
	defer conn.Close()
	roundTrip(t, conn, "before the subscription update")

	err = outboundManager.Create(ctx, instance.Router(),
		instance.LogFactory().NewLogger("outbound/direct[node-b]"),
		"node-b", C.TypeDirect, &option.DirectOutboundOptions{})
	if err != nil {
		t.Fatalf("create node-b: %v", err)
	}
	group, dynamic := proxy.(adapter.DynamicOutboundGroup)
	if !dynamic {
		t.Fatalf("urltest does not accept membership changes: %T", proxy)
	}
	if err = group.SetMembers([]string{"node-a", "node-b"}); err != nil {
		t.Fatalf("SetMembers: %v", err)
	}
	if members := group.All(); len(members) != 2 || members[1] != "node-b" {
		t.Errorf("All() = %v, want [node-a node-b]", members)
	}
	roundTrip(t, conn, "survived a node being added")

	// 撤掉一个跟这条连接无关的节点，同样不该有任何影响。
	if err = group.SetMembers([]string{"node-a"}); err != nil {
		t.Fatalf("SetMembers after dropping node-b: %v", err)
	}
	if err = outboundManager.Remove("node-b"); err != nil {
		t.Fatalf("remove node-b: %v", err)
	}
	roundTrip(t, conn, "survived a node being removed")
}

// waitForSelection 等 urltest 组测出结果并选定一个节点。
func waitForSelection(t *testing.T, group adapter.OutboundGroup) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if now := group.Now(); now != "" {
			return now
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the group never selected anything")
	return ""
}

// 机场撤掉的那个节点，恰好是组当前选中的——这条路必须走对。
//
// Select 会拿缓存的 selectedOutboundTCP/UDP 当比较基准，不清掉的话，一个已经被移出组
// 的出站会继续被选中，流量就打到一个已经不存在的节点上了。
func TestURLTestStopsSelectingARemovedMember(t *testing.T) {
	instance, ctx := startBox(t, option.Options{
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect, Tag: "node-a"},
			{Type: C.TypeDirect, Tag: "node-b"},
			{Type: C.TypeURLTest, Tag: "proxy", Options: &option.URLTestOutboundOptions{
				Outbounds: []string{"node-a", "node-b"},
				URL:       startProbeTarget(t),
			}},
		},
	})
	_ = ctx
	outboundManager := instance.Outbound()
	proxy, _ := outboundManager.Outbound("proxy")
	group := proxy.(adapter.DynamicOutboundGroup)

	// 哪个被选中取决于实测延迟，两个都是本地直连、快慢无从预测——所以不假设，
	// 测出来之后把被选中的那个撤掉。
	selected := waitForSelection(t, group)
	survivor := "node-a"
	if selected == "node-a" {
		survivor = "node-b"
	}

	if err := group.SetMembers([]string{survivor}); err != nil {
		t.Fatalf("SetMembers: %v", err)
	}
	if err := outboundManager.Remove(selected); err != nil {
		t.Fatalf("remove %s: %v", selected, err)
	}

	if now := group.Now(); now == selected {
		t.Errorf("Now() = %q, but %q was removed from the group", now, selected)
	}
	if members := group.All(); len(members) != 1 || members[0] != survivor {
		t.Errorf("All() = %v, want [%s]", members, survivor)
	}
}

// waitFor 轮询等一个条件成立，超时即失败。
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// startShadowsocksServer 起一个 shadowsocks 服务端，返回它的监听端口。
//
// 测试退役必须用一个「关掉出站会真的打断连接」的协议。direct 不行——它只是个 dialer，
// Close 之后已经建立的 TCP 连接照样跑，断言就成了空的。shadowsocks 开 multiplex 之后
// 出站的 Close 会撕掉整条 mux 会话（protocol/shadowsocks/outbound.go:139），隧道里的
// 流当场全断，正是我们要防住的那种断法。而且它不需要证书。
func startShadowsocksServer(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()

	startBox(t, option.Options{
		Inbounds: []option.Inbound{{
			Type: C.TypeShadowsocks,
			Tag:  "ss-in",
			Options: &option.ShadowsocksInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen:     common.Ptr(badoption.Addr(netip.AddrFrom4([4]byte{127, 0, 0, 1}))),
					ListenPort: port,
				},
				Method:    shadowsocksMethod,
				Password:  shadowsocksPassword,
				Multiplex: &option.InboundMultiplexOptions{Enabled: true},
			},
		}},
		Outbounds: []option.Outbound{{Type: C.TypeDirect, Tag: "direct"}},
	})
	return port
}

const (
	shadowsocksMethod   = "chacha20-ietf-poly1305"
	shadowsocksPassword = "test-only-not-a-real-secret"
)

func shadowsocksNode(port uint16) *option.ShadowsocksOutboundOptions {
	return &option.ShadowsocksOutboundOptions{
		ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: port},
		Method:        shadowsocksMethod,
		Password:      shadowsocksPassword,
		// mux 是关键：出站被关掉时整条会话一起断，隧道里的流全部阵亡。
		// 明确选 yamux：默认的 h2mux 在 sing-mux v0.3.5 里自己带一个数据竞争
		// （h2mux_conn.go 的 setup 与 Read 之间），-race 下跑不了。
		Multiplex: &option.OutboundMultiplexOptions{Enabled: true, Protocol: "yamux"},
	}
}

// 机场改了某个节点的服务器地址或密码——节点名没变，但底下换了东西。
//
// 新连接必须走新的；而正在走旧节点的那条连接不能断，用户可能正在游戏里。旧出站要等
// 自己的连接全部结束之后才真正被关掉。
//
// OutboundManager.Create 做不到这件事：同 tag 覆盖时它当场 common.Close 掉旧对象
// （adapter/outbound/manager.go），会话被撕掉，走着它的连接全部瞬间死掉。
func TestReplacingANodeRetiresItGracefully(t *testing.T) {
	echoAddress := startEchoServer(t)
	serverPort := startShadowsocksServer(t)
	instance, ctx := startBox(t, option.Options{
		Outbounds: []option.Outbound{{Type: C.TypeDirect, Tag: "default"}},
	})
	manager, dynamic := instance.Outbound().(adapter.DynamicOutboundManager)
	if !dynamic {
		t.Fatalf("outbound manager cannot retire outbounds: %T", instance.Outbound())
	}
	newNode := func() error {
		return manager.Replace(ctx, instance.Router(),
			instance.LogFactory().NewLogger("outbound/shadowsocks[node]"),
			"node", C.TypeShadowsocks, shadowsocksNode(serverPort))
	}

	if err := newNode(); err != nil {
		t.Fatalf("create node: %v", err)
	}
	original, _ := manager.Outbound("node")

	conn, err := original.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(echoAddress))
	if err != nil {
		t.Fatalf("dial through node: %v", err)
	}
	defer conn.Close()
	roundTrip(t, conn, "before the node changed")

	// 机场改了这个节点的参数。
	if err = newNode(); err != nil {
		t.Fatalf("replace node: %v", err)
	}

	if current, _ := manager.Outbound("node"); current == original {
		t.Error("the manager still hands out the old outbound, new connections would keep using it")
	}
	roundTrip(t, conn, "survived the node being replaced")
	if retiring := manager.Retiring(); len(retiring) != 1 || retiring[0] != "node" {
		t.Errorf("Retiring() = %v, want [node] while the old connection is still open", retiring)
	}

	// 连接结束，退役才完成。
	_ = conn.Close()
	waitFor(t, "the retired outbound to be released", func() bool {
		return len(manager.Retiring()) == 0
	})
}

// 组里存的是出站「对象」，不是 tag。某个节点被换掉之后，组手里那个指针还指着旧对象——
// 新连接会继续打到机场已经废弃的那台服务器上，而不是新的那台。
//
// 与其要求每个调用方在 Replace 之后记得去刷新每个组（漏一次就是线上事故），不如让管理器
// 自己通知：它手上本来就有 dependByTag，知道谁引用了这个 tag。
//
// 判据必须能区分新旧两个对象。关掉一个 shadowsocks 出站只会掐断它现有的流，之后照样能
// 建新会话，所以「拨得通」证明不了组用的是哪一个。这里让替换后的节点指向一个没人监听的
// 端口：组跟上了就一定拨不通，没跟上就一定拨得通。
func TestReplacingANodeRefreshesTheGroupsUsingIt(t *testing.T) {
	echoAddress := startEchoServer(t)
	livePort := startShadowsocksServer(t)
	deadPort := reservePort(t)

	instance, ctx := startBox(t, option.Options{
		Outbounds: []option.Outbound{
			{Type: C.TypeShadowsocks, Tag: "node", Options: shadowsocksNode(livePort)},
			{Type: C.TypeSelector, Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Outbounds: []string{"node"},
			}},
		},
	})
	manager := instance.Outbound().(adapter.DynamicOutboundManager)
	proxy, _ := manager.Outbound("proxy")
	pointNodeAt := func(port uint16) {
		t.Helper()
		if err := manager.Replace(ctx, instance.Router(),
			instance.LogFactory().NewLogger("outbound/shadowsocks[node]"),
			"node", C.TypeShadowsocks, shadowsocksNode(port)); err != nil {
			t.Fatalf("replace node: %v", err)
		}
	}
	reachesEcho := func() bool {
		conn, err := proxy.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(echoAddress))
		if err != nil {
			return false
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err = conn.Write([]byte("ping")); err != nil {
			return false
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		buffer := make([]byte, 4)
		_, err = io.ReadFull(conn, buffer)
		return err == nil && string(buffer) == "ping"
	}

	// 先让节点变成受跟踪的那种。配置里建出来的出站数不清自己身上的连接，被顶掉时
	// 只能硬关——那是这套机制的已知边界，不是这条用例要测的东西。
	pointNodeAt(livePort)

	// 占住一条连接，等下用来验证退役不打断它。
	live, err := proxy.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(echoAddress))
	if err != nil {
		t.Fatalf("dial through the group: %v", err)
	}
	defer live.Close()
	roundTrip(t, live, "before the node was replaced")

	// 机场把这个节点换到了一台不存在的服务器上。
	pointNodeAt(deadPort)
	if reachesEcho() {
		t.Error("the group still reaches the old server, it is holding the replaced outbound")
	}
	// 与此同时，先前那条连接不受影响。
	roundTrip(t, live, "survived its own node being replaced")

	// 换回来，组同样要跟上——否则它只是碰巧坏在了正确的方向上。
	pointNodeAt(livePort)
	if !reachesEcho() {
		t.Error("the group did not follow the node back to the working server")
	}
}

// reservePort 拿一个当场释放掉的端口号：一个保证没人监听的地址。
func reservePort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	return port
}

// 订阅内容随测试变化：改完再 Update 一次，验证节点增删真的落到组里。
type subscriptionServer struct {
	access  sync.Mutex
	content string
	url     string
}

func startSubscriptionServer(t *testing.T, content string) *subscriptionServer {
	t.Helper()
	subscription := &subscriptionServer{content: content}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		subscription.access.Lock()
		defer subscription.access.Unlock()
		_, _ = w.Write([]byte(subscription.content))
	}))
	t.Cleanup(server.Close)
	subscription.url = server.URL
	return subscription
}

func (s *subscriptionServer) serve(content string) {
	s.access.Lock()
	defer s.access.Unlock()
	s.content = content
}

func node(name string, port int) string {
	return "  - name: \"" + name + "\"\n" +
		"    type: ss\n" +
		"    server: 127.0.0.1\n" +
		"    port: " + strconv.Itoa(port) + "\n" +
		"    cipher: chacha20-ietf-poly1305\n" +
		"    password: FAKE-PASSWORD-NOT-REAL\n"
}

// 整条链：sing-box 自己拉订阅、把 proxies 转成出站、按 filter 分配给策略组，
// 全程不重启进程、不重写配置文件。
func TestSubscriptionDrivesGroupMembership(t *testing.T) {
	subscription := startSubscriptionServer(t, "proxies:\n"+
		// 机场把流量信息也伪装成节点排在最前面，它绝不能进任何组。
		node("Traffic Reset：4 Days Left", 10001)+
		node("🇭🇰 Hong Kong 01", 10002)+
		node("🇯🇵 Japan 01", 10003))

	instance, ctx := startBox(t, option.Options{
		Subscriptions: []option.Subscription{{
			Tag: "airport",
			URL: subscription.url,
		}},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect, Tag: "direct"},
			{Type: C.TypeSelector, Tag: "🐉 HK", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
				Filter: []option.GroupFilter{
					{Action: "include", Keywords: []string{"🇭🇰|HK|香港"}},
				},
			}},
			{Type: C.TypeSelector, Tag: "🚀 ALL", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
				Filter: []option.GroupFilter{
					{Action: "exclude", Keywords: []string{"Traffic|Expire|Days Left"}},
				},
			}},
		},
	})

	groupMembers := func(tag string) []string {
		outbound, loaded := instance.Outbound().Outbound(tag)
		if !loaded {
			t.Fatalf("no group tagged %q", tag)
		}
		return outbound.(adapter.OutboundGroup).All()
	}

	// 箱子起来的时候订阅已经拉过并应用了。
	require.Equal(t, []string{"🇭🇰 Hong Kong 01"}, groupMembers("🐉 HK"))
	require.Equal(t, []string{"🇭🇰 Hong Kong 01", "🇯🇵 Japan 01"}, groupMembers("🚀 ALL"))
	// 节点本身也真的成了出站。
	_, loaded := instance.Outbound().Outbound("🇯🇵 Japan 01")
	require.True(t, loaded)

	// 机场加了一个香港节点，撤掉了日本那个。
	subscription.serve("proxies:\n" +
		node("Traffic Reset：3 Days Left", 10001) +
		node("🇭🇰 Hong Kong 01", 10002) +
		node("🇭🇰 Hong Kong 02", 10004))

	subscriptionManager := service.FromContext[adapter.SubscriptionManager](ctx)
	require.NotNil(t, subscriptionManager)
	airport, found := subscriptionManager.Subscription("airport")
	require.True(t, found)
	require.NoError(t, airport.Update())

	require.Equal(t, []string{"🇭🇰 Hong Kong 01", "🇭🇰 Hong Kong 02"}, groupMembers("🐉 HK"))
	require.Equal(t, []string{"🇭🇰 Hong Kong 01", "🇭🇰 Hong Kong 02"}, groupMembers("🚀 ALL"))
	// 撤掉的节点也从出站管理器里摘干净了，否则会一直挂在那儿占着资源。
	_, loaded = instance.Outbound().Outbound("🇯🇵 Japan 01")
	require.False(t, loaded)
}

// 拉订阅绝不能绕回 sing-box 自己。
//
// 默认路由指向一个由订阅供给的组时，启动那一刻它还是空的：请求想出去必须先有节点，
// 而节点要靠这个请求拉回来。实机上这会变成
// `initial update: ... group[🚀 PROXY] has no members`，箱子永远起不来。
//
// 上一版栽在这里：注释写着「默认直连」，用的却是 DefaultTransport——那个是走箱子路由的。
func TestSubscriptionFetchesWithoutRoutingThroughItself(t *testing.T) {
	subscription := startSubscriptionServer(t, "proxies:\n"+node("🇭🇰 Hong Kong 01", 10002))

	instance, _ := startBox(t, option.Options{
		Subscriptions: []option.Subscription{{Tag: "airport", URL: subscription.url}},
		Outbounds: []option.Outbound{
			{Type: C.TypeSelector, Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
		Route: &option.RouteOptions{Final: "proxy"},
	})

	outbound, loaded := instance.Outbound().Outbound("proxy")
	require.True(t, loaded)
	require.Equal(t, []string{"🇭🇰 Hong Kong 01"}, outbound.(adapter.OutboundGroup).All())
}

// startHangingServer 起一个只接受连接、永远不回应的 HTTP 服务。机场故障或被墙时
// 就是这个样子：TCP 握得上，数据一个字节都不来。
func startHangingServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		var held []net.Conn
		defer func() {
			for _, conn := range held {
				_ = conn.Close()
			}
		}()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// 收下就不管了，也不关——关掉的话客户端会立刻拿到 EOF，就测不到卡死了。
			held = append(held, conn)
		}
	}()
	return "http://" + listener.Addr().String() + "/sub"
}

// 拉取必须有超时，否则一个不应答的机场能把整个 sing-box 钉死。
//
// 首次启动没有本地存档时，Start() 会同步调一次 Update()——请求不返回，Start() 就不返回，
// box.Start() 也就不返回。路由器上的表现是服务起不来、全网断，而且不打任何日志。
// 摘掉本用例守着的那行超时，这个测试进程本身也会挂死（验证过）。
//
// 起来之后的风险稍轻但同样致命：更新循环是单个 goroutine，一次挂住的请求会把它永久
// 钉在那里，从此不再更新订阅，表面上却一切正常。
func TestSubscriptionUpdateGivesUpOnAServerThatNeverAnswers(t *testing.T) {
	instance, ctx := startBox(t, option.Options{
		Subscriptions: []option.Subscription{{
			Tag:             "airport",
			URL:             startHangingServer(t),
			DownloadTimeout: badoption.Duration(500 * time.Millisecond),
		}},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect, Tag: "direct"},
			{Type: C.TypeSelector, Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})
	_ = instance

	manager := service.FromContext[adapter.SubscriptionManager](ctx)
	airport, found := manager.Subscription("airport")
	require.True(t, found)

	done := make(chan error, 1)
	go func() { done <- airport.Update() }()
	select {
	case err := <-done:
		require.Error(t, err, "Update returned success from a server that never answered")
	case <-time.After(5 * time.Second):
		t.Fatal("Update never returned — the update loop would be dead from here on")
	}
}

// startAlternatingSubscriptionServer 每次请求交替返回两份不同的订阅，并在应答前拖一下，
// 好让并发的两次更新必然重叠。
func startAlternatingSubscriptionServer(t *testing.T, first string, second string) string {
	t.Helper()
	var access sync.Mutex
	var count int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		access.Lock()
		count++
		content := first
		if count%2 == 0 {
			content = second
		}
		access.Unlock()
		// 窗口开得足够宽，两次更新一定撞在一起。
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte(content))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// 并发更新之后，订阅报出的节点和出站管理器里的必须仍然对得上。
//
// 面板上连点两下「更新」、或者点一下恰好撞上后台定时更新，就会同时跑两次 apply。
// apply 先读下 previous、再逐个 Replace、最后摘掉 previous 里消失的；两次交错时，
// 后一次手里的 previous 已经过时，理论上会去摘别人刚装上的节点。
//
// 实测摘不掉：出站管理器的 Remove 有依赖检查，节点还被组引用着就删不动。也就是说这条
// 不变量目前是靠那个检查兜住的，而不是靠更新本身的原子性。这个用例钉的是不变量，
// 不是某个实现——兜底的东西哪天变了，它就该响。
//
// s.access 保护的只是字段读写，-race 对这种交错一言不发，判据只能是状态一致性。
func TestConcurrentUpdatesLeaveConsistentState(t *testing.T) {
	first := "proxies:\n" + node("HK 01", 10001) + node("JP 01", 10002)
	second := "proxies:\n" + node("HK 01", 10001) + node("SG 01", 10003)

	instance, ctx := startBox(t, option.Options{
		Subscriptions: []option.Subscription{{
			Tag: "airport",
			URL: startAlternatingSubscriptionServer(t, first, second),
		}},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect, Tag: "direct"},
			{Type: C.TypeSelector, Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})

	manager := service.FromContext[adapter.SubscriptionManager](ctx)
	airport, found := manager.Subscription("airport")
	require.True(t, found)

	var wait sync.WaitGroup
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = airport.Update()
		}()
	}
	wait.Wait()

	// 订阅自己报出来的节点，必须真的都在出站管理器里。
	for _, tag := range airport.Nodes() {
		if _, loaded := instance.Outbound().Outbound(tag); !loaded {
			t.Errorf("subscription reports node %q but the outbound manager has no such outbound", tag)
		}
	}
	// 组的成员同样。组里留一个不存在的 tag，下次重算就会整组失败。
	proxy, _ := instance.Outbound().Outbound("proxy")
	for _, tag := range proxy.(adapter.OutboundGroup).All() {
		if _, loaded := instance.Outbound().Outbound(tag); !loaded {
			t.Errorf("group holds member %q that is not in the outbound manager", tag)
		}
	}
	// 而且组必须还能跟着订阅走一次——踩坏之后这一步会报 outbound not found。
	require.NoError(t, airport.Update())
}

// bigSubscription 造一份足够大的订阅，大到 os.WriteFile 必须分多次 write 才写得完。
func bigSubscription(t *testing.T, prefix string, count int) string {
	t.Helper()
	var builder strings.Builder
	builder.WriteString("proxies:\n")
	for i := range count {
		builder.WriteString(node(prefix+" "+strconv.Itoa(i), 10000+i))
	}
	return builder.String()
}

// 并发更新之后，本地存档必须是完整的一份，不能是两份的混合。
//
// apply 收尾时 os.WriteFile 存档，好让下次开机立刻能用。并发跑两次就是两个 WriteFile
// 打在同一个路径上，各自 O_TRUNC 之后再写。写坏了的后果是下次开机加载一坨半截内容——
// 运气好解析失败（还能靠网络兜底），运气不好解析成功但只剩半份节点。
//
// 实测写不坏：这个尺寸下 write 一次 syscall 就落完，两份内容不会交错。也就是说这条
// 不变量目前靠的是「订阅还不够大」，而不是写入本身受了保护。用例钉的是不变量。
func TestConcurrentUpdatesDoNotCorruptTheSavedArchive(t *testing.T) {
	first := bigSubscription(t, "AAAA", 400)
	second := bigSubscription(t, "BBBB", 400)
	archive := filepath.Join(t.TempDir(), "airport.yaml")

	instance, ctx := startBox(t, option.Options{
		Subscriptions: []option.Subscription{{
			Tag:  "airport",
			URL:  startAlternatingSubscriptionServer(t, first, second),
			Path: archive,
		}},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect, Tag: "direct"},
			{Type: C.TypeSelector, Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})
	_ = instance

	manager := service.FromContext[adapter.SubscriptionManager](ctx)
	airport, _ := manager.Subscription("airport")

	var wait sync.WaitGroup
	for range 6 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = airport.Update()
		}()
	}
	wait.Wait()

	saved, err := os.ReadFile(archive)
	require.NoError(t, err)
	// 存档必须原样等于其中一份，不能是两份的混合。
	if string(saved) != first && string(saved) != second {
		t.Errorf("the saved archive is neither subscription: %d bytes, first=%d second=%d\n"+
			"it is a mix of two concurrent writes", len(saved), len(first), len(second))
	}
}

// 面板上那句「更新于 X 前」要有个来源。
func TestSubscriptionReportsWhenItLastUpdated(t *testing.T) {
	before := time.Now()
	subscription := startSubscriptionServer(t, "proxies:\n"+node("HK 01", 10001))
	_, ctx := startBox(t, option.Options{
		Subscriptions: []option.Subscription{{Tag: "airport", URL: subscription.url}},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect, Tag: "direct"},
			{Type: C.TypeSelector, Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})
	airport, _ := service.FromContext[adapter.SubscriptionManager](ctx).Subscription("airport")
	updatedAt := airport.UpdatedAt()
	require.False(t, updatedAt.Before(before), "UpdatedAt %v predates the box starting at %v", updatedAt, before)
	require.False(t, updatedAt.After(time.Now()), "UpdatedAt is in the future: %v", updatedAt)
}

// 从本地存档启动时，「上次更新」是存档落盘的那一刻，不是开机这一刻。
//
// 填 time.Now() 的话，每次重启都显示「刚刚更新」——而路由器断网重启时，那份存档可能
// 已经放了一个星期。面板会理直气壮地告诉你订阅是新的，恰恰在它最可能过期的时候。
func TestSubscriptionLoadedFromArchiveReportsTheArchivesAge(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "airport.yaml")
	require.NoError(t, os.WriteFile(archive, []byte("proxies:\n"+node("HK 01", 10001)), 0o600))
	threeDaysAgo := time.Now().Add(-72 * time.Hour)
	require.NoError(t, os.Chtimes(archive, threeDaysAgo, threeDaysAgo))

	// URL 指向一个不应答的地址：启动时存档已经供上了节点，就不该再去拉。
	_, ctx := startBox(t, option.Options{
		Subscriptions: []option.Subscription{{
			Tag:             "airport",
			URL:             startHangingServer(t),
			Path:            archive,
			DownloadTimeout: badoption.Duration(500 * time.Millisecond),
		}},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect, Tag: "direct"},
			{Type: C.TypeSelector, Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})

	airport, _ := service.FromContext[adapter.SubscriptionManager](ctx).Subscription("airport")
	require.Equal(t, []string{"HK 01"}, airport.Nodes(), "the archive did not supply the nodes")
	require.WithinDuration(t, threeDaysAgo, airport.UpdatedAt(), time.Minute,
		"UpdatedAt should be the archive's mtime, not the moment we booted")
}

// 存档是为了「立刻能用」，不是「今天不用更新了」。
//
// 装上存档之后还要照常去拉一次，只是不必挡着启动。不拉的话，有存档就等于把订阅冻结到
// 下一个 interval（默认 24 小时）——机场半夜换了密码，你重启路由器，那些节点已经全废了，
// 却要到第二天这个点才会去问一次。Start 的注释本来就写着「再去拉新的」。
func TestSubscriptionRefreshesEvenWhenTheArchiveSuppliedNodes(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "airport.yaml")
	require.NoError(t, os.WriteFile(archive, []byte("proxies:\n"+node("HK 01", 10001)), 0o600))

	// 机场那边已经多了一个节点。
	subscription := startSubscriptionServer(t, "proxies:\n"+node("HK 01", 10001)+node("JP 01", 10002))

	instance, ctx := startBox(t, option.Options{
		Subscriptions: []option.Subscription{{Tag: "airport", URL: subscription.url, Path: archive}},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect, Tag: "direct"},
			{Type: C.TypeSelector, Tag: "proxy", Options: &option.SelectorOutboundOptions{
				Subscriptions: []string{"airport"},
			}},
		},
	})
	airport, _ := service.FromContext[adapter.SubscriptionManager](ctx).Subscription("airport")

	// 存档立刻供上了节点，箱子不必等网络。
	require.NotEmpty(t, airport.Nodes(), "the archive did not supply nodes")

	// 而新的那个节点应该很快自己出现，不用等到下一个 interval。
	waitFor(t, "the subscription to refresh past the archive", func() bool {
		return len(airport.Nodes()) == 2
	})
	proxy, _ := instance.Outbound().Outbound("proxy")
	require.ElementsMatch(t, []string{"HK 01", "JP 01"}, proxy.(adapter.OutboundGroup).All())
}
