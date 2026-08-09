package box_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
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
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatalf("write %q: %v", message, err)
	}
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
