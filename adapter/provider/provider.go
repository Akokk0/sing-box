// Package provider 让 sing-box 自己订阅机场：拉取、转换、在运行中增删替换节点。
//
// 与「生成一份新配置再重启」的做法相比，这里全程不重启进程、不重写配置文件；没有被
// 改动的节点，它们的出站对象一动不动，正在走它们的连接一条都不会断。
package provider

import (
	"context"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/convertor/mihomo"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
)

var _ adapter.OutboundProvider = (*Provider)(nil)

// Provider 是一份订阅。
type Provider struct {
	ctx        context.Context
	ctxCancel  context.CancelFunc
	logger     log.ContextLogger
	logFactory log.Factory
	router     adapter.Router
	outbound   adapter.OutboundManager
	options    option.OutboundProvider
	interval   time.Duration
	httpClient *http.Client
	// onUpdated 在节点集合变化之后通知管理器去重算各个组的成员。
	onUpdated func()

	access sync.RWMutex
	nodes  []string
	// content 是上一次成功应用的订阅原文。机场大多数时候节点不变，比一比就能整轮跳过。
	content []byte
}

func New(ctx context.Context, router adapter.Router, logFactory log.Factory, options option.OutboundProvider, onUpdated func()) (*Provider, error) {
	if options.Tag == "" {
		return nil, E.New("missing tag")
	}
	if options.URL == "" {
		return nil, E.New("provider[", options.Tag, "]: missing url")
	}
	switch options.Format {
	case "", "mihomo":
	default:
		return nil, E.New("provider[", options.Tag, "]: unknown format: ", options.Format)
	}
	interval := time.Duration(options.Interval)
	if interval == 0 {
		interval = 24 * time.Hour
	}
	if interval < time.Minute {
		return nil, E.New("provider[", options.Tag, "]: interval must be at least 1m")
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Provider{
		ctx:        ctx,
		ctxCancel:  cancel,
		logger:     logFactory.NewLogger("provider[" + options.Tag + "]"),
		logFactory: logFactory,
		router:     router,
		outbound:   service.FromContext[adapter.OutboundManager](ctx),
		options:    options,
		interval:   interval,
		onUpdated:  onUpdated,
	}, nil
}

func (p *Provider) Tag() string {
	return p.options.Tag
}

func (p *Provider) Nodes() []string {
	p.access.RLock()
	defer p.access.RUnlock()
	return p.nodes
}

// Start 先用本地那份存档把节点立刻装上，再去拉新的。
//
// 拉取失败不能拖垮启动：路由器开机时网络往往还没通，而这份订阅正是连上网所需要的东西。
// 失败只记一笔，后台循环会继续重试。
func (p *Provider) Start() error {
	transport, err := p.resolveTransport()
	if err != nil {
		return err
	}
	p.httpClient = &http.Client{Transport: transport}

	if p.options.Path != "" {
		content, readErr := os.ReadFile(p.options.Path)
		if readErr == nil {
			if applyErr := p.apply(content); applyErr != nil {
				p.logger.Error("load saved subscription: ", applyErr)
			} else {
				p.logger.Info("loaded ", len(p.Nodes()), " nodes from ", p.options.Path)
			}
		}
	}
	if len(p.Nodes()) == 0 {
		if err = p.Update(); err != nil {
			// 起不来也要让箱子跑起来：其余出站和规则照常工作，组暂时是空的。
			p.logger.Error("initial update: ", err)
		}
	}
	go p.loop()
	return nil
}

func (p *Provider) Close() error {
	p.ctxCancel()
	return nil
}

func (p *Provider) loop() {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			if err := p.Update(); err != nil {
				p.logger.Error("update: ", err)
			}
		}
	}
}

// Update 拉一次订阅并应用。
func (p *Provider) Update() error {
	request, err := http.NewRequestWithContext(p.ctx, http.MethodGet, p.options.URL, nil)
	if err != nil {
		return err
	}
	response, err := p.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return E.New("unexpected status: ", response.Status)
	}
	content, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	return p.apply(content)
}

// apply 把一份订阅原文变成运行中的节点。
//
// 顺序不能反：先把新节点装上、再让各个组重算成员、最后才摘掉消失的节点。反过来的话，
// 组会有一瞬间指着已经被摘掉的出站。
func (p *Provider) apply(content []byte) error {
	p.access.RLock()
	unchanged := len(p.content) > 0 && string(p.content) == string(content)
	previous := p.nodes
	p.access.RUnlock()
	if unchanged {
		p.logger.Debug("subscription is unchanged")
		return nil
	}

	outbounds, skipped, err := mihomo.ToOptions(p.ctx, content)
	if err != nil {
		return err
	}
	if len(outbounds) == 0 {
		// 一个都没转出来就装上去等于把所有组清空，那和断网没区别。
		return E.New("no usable node out of ", len(outbounds)+len(skipped), " in the subscription")
	}
	if len(skipped) > 0 && !p.options.ExcludeSkipped {
		for _, reason := range skipped {
			p.logger.Warn("skipped: ", reason)
		}
	}

	tags := make([]string, 0, len(outbounds))
	for _, outbound := range outbounds {
		err = p.outbound.(adapter.DynamicOutboundManager).Replace(
			p.ctx, p.router,
			p.logFactory.NewLogger("outbound/"+outbound.Type+"["+outbound.Tag+"]"),
			outbound.Tag, outbound.Type, outbound.Options,
		)
		if err != nil {
			return E.Cause(err, "apply node ", outbound.Tag)
		}
		tags = append(tags, outbound.Tag)
	}

	p.access.Lock()
	p.nodes = tags
	p.content = content
	p.access.Unlock()

	if p.onUpdated != nil {
		p.onUpdated()
	}

	// 组已经不再引用它们了，现在摘掉才安全。
	live := make(map[string]bool, len(tags))
	for _, tag := range tags {
		live[tag] = true
	}
	for _, tag := range previous {
		if live[tag] {
			continue
		}
		if err = p.outbound.Remove(tag); err != nil {
			p.logger.Error("remove node ", tag, ": ", err)
		}
	}

	if p.options.Path != "" {
		if err = os.WriteFile(p.options.Path, content, 0o600); err != nil {
			// 存不下只影响下次开机的启动速度，不影响这一次。
			p.logger.Error("save subscription: ", err)
		}
	}
	p.logger.Info("applied ", len(tags), " nodes")
	return nil
}

func (p *Provider) resolveTransport() (adapter.HTTPTransport, error) {
	httpClientManager := service.FromContext[adapter.HTTPClientManager](p.ctx)
	if httpClientManager == nil {
		return nil, E.New("missing http client manager")
	}
	// 刻意不用 DefaultTransport：那个是走箱子自己的路由的。默认路由指向一个由订阅供给的
	// 组时，启动那一刻它还空着——请求想出去得先有节点，节点却要靠这个请求拉回来，箱子
	// 永远起不来。detour 留空 + DisableEmptyDirectCheck 得到的才是真正的直连拨号。
	return httpClientManager.ResolveTransport(p.ctx, p.logger, option.HTTPClientOptions{
		DialerOptions:           option.DialerOptions{Detour: p.options.DownloadDetour},
		DisableEmptyDirectCheck: true,
	})
}
