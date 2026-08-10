// Package subscription 让 sing-box 自己订阅机场：拉取、转换、在运行中增删替换节点。
//
// 与「生成一份新配置再重启」的做法相比，这里全程不重启进程、不重写配置文件；没有被
// 改动的节点，它们的出站对象一动不动，正在走它们的连接一条都不会断。
package subscription

import (
	"context"
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

// defaultDownloadTimeout 是单次拉取的默认时限。路由器的上行往往很慢，给得比
// 一般的 HTTP 请求宽一些。
const defaultDownloadTimeout = 30 * time.Second

var _ adapter.Subscription = (*Subscription)(nil)

// Subscription 是一份订阅。
type Subscription struct {
	ctx        context.Context
	ctxCancel  context.CancelFunc
	logger     log.ContextLogger
	logFactory log.Factory
	router     adapter.Router
	outbound   adapter.OutboundManager
	options    option.Subscription
	interval   time.Duration
	timeout    time.Duration
	httpClient *http.Client
	// onUpdated 在节点集合变化之后通知管理器去重算各个组的成员。
	onUpdated func()

	// updateAccess 让更新一次只跑一个。面板点一下「更新」恰好撞上后台的定时更新，
	// 两次 apply 就会交错：后一次手里的 previous 已经过时，删节点会删错；两次还会
	// 同时 os.WriteFile 同一个存档。
	//
	// 目前这两件事都没真的坏过——删错被出站管理器的依赖检查挡下了，存档没写坏是因为
	// 这个尺寸下 write 一次就落完。但那是两处巧合，不是保证。串起来才是保证。
	updateAccess sync.Mutex

	access    sync.RWMutex
	nodes     []string
	updatedAt time.Time
	info      *adapter.SubscriptionInfo
	// content 是上一次成功应用的订阅原文。机场大多数时候节点不变，比一比就能整轮跳过。
	content []byte
}

func New(ctx context.Context, router adapter.Router, logFactory log.Factory, options option.Subscription, onUpdated func()) (*Subscription, error) {
	if options.Tag == "" {
		return nil, E.New("missing tag")
	}
	if options.URL == "" {
		return nil, E.New("subscription[", options.Tag, "]: missing url")
	}
	switch options.Format {
	case "", "mihomo":
	default:
		return nil, E.New("subscription[", options.Tag, "]: unknown format: ", options.Format)
	}
	interval := time.Duration(options.Interval)
	if interval == 0 {
		interval = 24 * time.Hour
	}
	if interval < time.Minute {
		return nil, E.New("subscription[", options.Tag, "]: interval must be at least 1m")
	}
	timeout := time.Duration(options.DownloadTimeout)
	if timeout == 0 {
		timeout = defaultDownloadTimeout
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Subscription{
		ctx:        ctx,
		ctxCancel:  cancel,
		logger:     logFactory.NewLogger("subscription[" + options.Tag + "]"),
		logFactory: logFactory,
		router:     router,
		outbound:   service.FromContext[adapter.OutboundManager](ctx),
		options:    options,
		interval:   interval,
		timeout:    timeout,
		onUpdated:  onUpdated,
	}, nil
}

func (s *Subscription) Tag() string {
	return s.options.Tag
}

func (s *Subscription) Nodes() []string {
	s.access.RLock()
	defer s.access.RUnlock()
	return s.nodes
}

func (s *Subscription) UpdatedAt() time.Time {
	s.access.RLock()
	defer s.access.RUnlock()
	return s.updatedAt
}

func (s *Subscription) Info() *adapter.SubscriptionInfo {
	s.access.RLock()
	defer s.access.RUnlock()
	return s.info
}

// Start 先用本地那份存档把节点立刻装上，再去拉新的。
//
// 拉取失败不能拖垮启动：路由器开机时网络往往还没通，而这份订阅正是连上网所需要的东西。
// 失败只记一笔，后台循环会继续重试。
func (s *Subscription) Start() error {
	transport, err := s.resolveTransport()
	if err != nil {
		return err
	}
	s.httpClient = &http.Client{Transport: transport}

	if s.options.Path != "" {
		content, readErr := os.ReadFile(s.options.Path)
		if readErr == nil {
			// 存档的年龄就是这批节点的年龄。断网重启时它可能已经放了一个星期，
			// 面板该照实说，不能显示成刚刚更新。
			savedAt := time.Now()
			if info, statErr := os.Stat(s.options.Path); statErr == nil {
				savedAt = info.ModTime()
			}
			if applyErr := s.applyAsOf(content, savedAt); applyErr != nil {
				s.logger.Error("load saved subscription: ", applyErr)
			} else {
				s.logger.Info("loaded ", len(s.Nodes()), " nodes from ", s.options.Path)
			}
		}
	}
	if len(s.Nodes()) == 0 {
		// 一个节点都没有，组是空的，路由无处可去——这一次必须当场拉，哪怕要等。
		if err = s.Update(); err != nil {
			// 起不来也要让箱子跑起来：其余出站和规则照常工作，组暂时是空的。
			s.logger.Error("initial update: ", err)
		}
		go s.loop()
		return nil
	}
	// 存档已经把节点供上了，箱子可以立刻跑起来，新的在后台拉。
	//
	// 不拉是不行的：那等于把订阅冻结到下一个 interval（默认一整天）。机场半夜换了密码，
	// 开机时装上的那批节点已经全废，却要到第二天这个点才会去问一次。
	go func() {
		if err := s.Update(); err != nil {
			s.logger.Error("initial update: ", err)
		}
		s.loop()
	}()
	return nil
}

func (s *Subscription) Close() error {
	s.ctxCancel()
	return nil
}

func (s *Subscription) loop() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if err := s.Update(); err != nil {
				s.logger.Error("update: ", err)
			}
		}
	}
}

// Update 拉一次订阅并应用。
//
// 整个过程持锁，拉取也算在内：撞上的那一次会排队等待，而不是并行跑第二遍。等到之后
// 它照样会自己拉一次，但内容多半没变，apply 一比就整轮跳过，代价只有一次请求。
func (s *Subscription) Update() error {
	s.updateAccess.Lock()
	defer s.updateAccess.Unlock()
	// 超时挂在这次请求上，而不是 http.Client 上：Client 的 Timeout 会把读 body 也算进去，
	// 但它是给所有请求共用的，改起来影响面更大。
	ctx, cancel := context.WithTimeout(s.ctx, s.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.options.URL, nil)
	if err != nil {
		return redactError(err)
	}
	if s.options.UserAgent != "" {
		request.Header.Set("User-Agent", s.options.UserAgent)
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		// 遮蔽在这里做，而不是在打日志的地方：这个错误还会经 Clash API 原样发给面板，
		// 每多一个消费方就多一次漏掉的机会。
		return redactError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return E.New("unexpected status: ", response.Status)
	}
	// 在读 body 之前取头：body 读坏了也不影响这条信息，而且它跟节点内容无关。
	info := parseUserInfo(response.Header.Get(userInfoHeader))
	content, err := readAtMost(response.Body, maxSubscriptionSize)
	if err != nil {
		return err
	}
	if err = s.apply(content); err != nil {
		return err
	}
	// 节点没变时 apply 会整轮跳过，但流量一直在涨，这条照样要更新。
	// 机场偶尔不发这个头时保留上一次的值——清空会让面板显示成套餐用光了。
	if info != nil {
		s.access.Lock()
		s.info = info
		s.access.Unlock()
	}
	return nil
}

// apply 把一份订阅原文变成运行中的节点。
//
// 顺序不能反：先把新节点装上、再让各个组重算成员、最后才摘掉消失的节点。反过来的话，
// 组会有一瞬间指着已经被摘掉的出站。
func (s *Subscription) apply(content []byte) error {
	return s.applyAsOf(content, time.Now())
}

// applyAsOf 与 apply 相同，但显式指定这批内容的来源时刻。
func (s *Subscription) applyAsOf(content []byte, asOf time.Time) error {
	s.access.RLock()
	unchanged := len(s.content) > 0 && string(s.content) == string(content)
	previous := s.nodes
	s.access.RUnlock()
	if unchanged {
		// 内容没变不等于什么都没发生：我们确实成功拉到了东西。这个字段的意思是
		// 「上次成功拉取的时刻」，不是「节点上次变化的时刻」——面板拿它显示
		// 「更新于 X 前」，不动的话点了刷新看起来毫无反应，按钮就像是坏的。
		s.access.Lock()
		s.updatedAt = asOf
		s.access.Unlock()
		// 存档的 mtime 一起跟上。重启之后「上次更新」是从这个 mtime 恢复的，
		// 不动的话会退回到内容最后一次变化的时刻——可能是几星期前，而这期间
		// 我们一直在正常检查。
		if s.options.Path != "" {
			if err := os.Chtimes(s.options.Path, asOf, asOf); err != nil {
				s.logger.Debug("touch saved subscription: ", err)
			}
		}
		s.logger.Debug("subscription is unchanged")
		return nil
	}

	outbounds, skipped, err := mihomo.ToOptions(s.ctx, content)
	if err != nil {
		return err
	}
	if len(outbounds) == 0 {
		// 一个都没转出来就装上去等于把所有组清空，那和断网没区别。
		return E.New("no usable node out of ", len(outbounds)+len(skipped), " in the subscription")
	}
	if len(skipped) > 0 && !s.options.ExcludeSkipped {
		for _, reason := range skipped {
			s.logger.Warn("skipped: ", reason)
		}
	}

	tags := make([]string, 0, len(outbounds))
	for _, outbound := range outbounds {
		err = s.outbound.(adapter.DynamicOutboundManager).Replace(
			s.ctx, s.router,
			s.logFactory.NewLogger("outbound/"+outbound.Type+"["+outbound.Tag+"]"),
			outbound.Tag, outbound.Type, outbound.Options,
		)
		if err != nil {
			return E.Cause(err, "apply node ", outbound.Tag)
		}
		tags = append(tags, outbound.Tag)
	}

	s.access.Lock()
	s.nodes = tags
	s.content = content
	s.updatedAt = asOf
	s.access.Unlock()

	if s.onUpdated != nil {
		s.onUpdated()
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
		if err = s.outbound.Remove(tag); err != nil {
			s.logger.Error("remove node ", tag, ": ", err)
		}
	}

	if s.options.Path != "" {
		if err = os.WriteFile(s.options.Path, content, 0o600); err != nil {
			// 存不下只影响下次开机的启动速度，不影响这一次。
			s.logger.Error("save subscription: ", err)
		}
	}
	s.logger.Info("applied ", len(tags), " nodes")
	return nil
}

func (s *Subscription) resolveTransport() (adapter.HTTPTransport, error) {
	httpClientManager := service.FromContext[adapter.HTTPClientManager](s.ctx)
	if httpClientManager == nil {
		return nil, E.New("missing http client manager")
	}
	// 刻意不用 DefaultTransport：那个是走箱子自己的路由的。默认路由指向一个由订阅供给的
	// 组时，启动那一刻它还空着——请求想出去得先有节点，节点却要靠这个请求拉回来，箱子
	// 永远起不来。detour 留空 + DisableEmptyDirectCheck 得到的才是真正的直连拨号。
	return httpClientManager.ResolveTransport(s.ctx, s.logger, option.HTTPClientOptions{
		DialerOptions:           option.DialerOptions{Detour: s.options.DownloadDetour},
		DisableEmptyDirectCheck: true,
	})
}
