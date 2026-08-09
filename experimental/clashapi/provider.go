package clashapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/protocol/group"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/batch"
	"github.com/sagernet/sing/service"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

// 订阅在这里露面，主要是为了那个 PUT：不然强制更新一次的唯一办法是重启 sing-box，
// 而自动更新的间隔通常是一天。
//
// 对外一律沿用 Clash 的 provider 叫法——/providers/proxies 是 Clash 的既定接口，
// 面板照着它请求，改成 subscriptions 只会让所有面板都认不出来。配置里叫 subscription、
// API 上叫 provider，这层的职责就是把两边对上。
func proxyProviderRouter(server *Server, ctx context.Context) http.Handler {
	r := chi.NewRouter()
	r.Get("/", getProviders(server, ctx))

	r.Route("/{name}", func(r chi.Router) {
		r.Use(parseProviderName, findProviderByName(ctx))
		r.Get("/", getProvider(server))
		r.Put("/", updateProvider)
		r.Get("/healthcheck", healthCheckProvider(server))
		// 单个节点。chi 优先匹配上面那条静态的 /healthcheck，所以两者不打架。
		r.Route("/{proxyName}", func(r chi.Router) {
			r.Use(findProviderProxyByName(server))
			r.Get("/", getProxy(server))
			r.Get("/healthcheck", getProxyDelay(server))
		})
	})
	return r
}

// findProviderProxyByName 在这份订阅给出的节点里找，而不是在整个出站管理器里找。
//
// 这条路由挂在某一份订阅底下，语义就该限定在它自己的节点上；否则
// /providers/proxies/airport/<任意出站> 都能应答，面板拿到的东西也就不再有边界。
func findProviderProxyByName(server *Server) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provider := r.Context().Value(CtxKeyProvider).(adapter.Subscription)
			name := getEscapeParam(r, "proxyName")
			if !common.Contains(provider.Nodes(), name) {
				render.Status(r, http.StatusNotFound)
				render.JSON(w, r, ErrNotFound)
				return
			}
			detour, loaded := server.outbound.Outbound(name)
			if !loaded {
				render.Status(r, http.StatusNotFound)
				render.JSON(w, r, ErrNotFound)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), CtxKeyProxy, detour)))
		})
	}
}

// providerInfo 里的 proxies 必须是完整的 proxy 对象,和 /proxies 给出的形状一致——
// 面板要靠每个对象的 name / type / history 才画得出那一列。填一串裸 tag 字符串的话，
// 面板读 proxy.name 得到 undefined，provider 卡片在、里面一个节点都显示不出来。
func providerInfo(server *Server, provider adapter.Subscription) render.M {
	nodes := provider.Nodes()
	proxies := make([]any, 0, len(nodes))
	for _, tag := range nodes {
		detour, loaded := server.outbound.Outbound(tag)
		if !loaded {
			// 订阅刚换过一轮、出站还没跟上时会短暂出现。少画一个节点即可，
			// 不该让整个面板拿不到列表。
			continue
		}
		proxies = append(proxies, proxyInfo(server, detour))
	}
	info := render.M{
		"name":        provider.Tag(),
		"type":        "Proxy",
		"vehicleType": "HTTP",
		"proxies":     proxies,
		// Clash 无条件下发这两个字段（没有 omitempty），面板会直接读。给不出来的话
		// 面板拿到 undefined，前端一个属性访问就能让整张卡片渲染失败。
		//
		// testUrl 是 healthcheck 不带 url 参数时实际用的目标；expectedStatus 报 "*"
		// 是照实说：urltest 只看请求成不成，不校验状态码。
		"testUrl":        urltest.DefaultURL,
		"expectedStatus": "*",
	}
	// 零值不往外发。面板会拿它当日期渲染，发出去就是「更新于 1970 年」。
	if updatedAt := provider.UpdatedAt(); !updatedAt.IsZero() {
		info["updatedAt"] = updatedAt
	}
	// 同理：机场没报流量就整个字段不发，发一份全零出去等于告诉面板套餐已用光。
	if subscriptionInfo := provider.Info(); subscriptionInfo != nil {
		info["subscriptionInfo"] = subscriptionInfo
	}
	return info
}

func getProviders(server *Server, ctx context.Context) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		providers := render.M{}
		if manager := service.FromContext[adapter.SubscriptionManager](ctx); manager != nil {
			for _, provider := range manager.Subscriptions() {
				providers[provider.Tag()] = providerInfo(server, provider)
			}
		}
		render.JSON(w, r, render.M{"providers": providers})
	}
}

func getProvider(server *Server) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		provider := r.Context().Value(CtxKeyProvider).(adapter.Subscription)
		render.JSON(w, r, providerInfo(server, provider))
	}
}

func updateProvider(w http.ResponseWriter, r *http.Request) {
	provider := r.Context().Value(CtxKeyProvider).(adapter.Subscription)
	if err := provider.Update(); err != nil {
		render.Status(r, http.StatusServiceUnavailable)
		render.JSON(w, r, newError(err.Error()))
		return
	}
	render.NoContent(w, r)
}

// healthCheckProvider 把这份订阅的节点整批测一遍，结果写进延迟历史——面板 Proxy
// Providers 页面上的「检查延迟」按钮走的就是这里。
//
// 测速本身是 urltest 组的日常工作，但那按它自己的节奏来；用户点这个按钮是要「现在就
// 给我一个数」。写进的是同一份历史，所以两边看到的是同一批数字。
func healthCheckProvider(server *Server) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		provider := r.Context().Value(CtxKeyProvider).(adapter.Subscription)
		query := r.URL.Query()
		link := query.Get("url")
		if strings.HasPrefix(link, "http://") {
			// 与 /proxies/{name}/delay 保持一致：明文目标容易被劫持或缓存，
			// 测出来的数不作数，宁可退回默认目标。
			link = ""
		}
		timeout := C.TCPTimeout
		if parsed, err := strconv.ParseInt(query.Get("timeout"), 10, 64); err == nil && parsed > 0 {
			timeout = time.Duration(parsed) * time.Millisecond
		}
		// 用箱子自己的 context，不是 r.Context()，更不是 context.Background()：
		// urltest 要从 ctx 里取根证书池和 NTP 时间函数。取不到的话，用户在 certificate
		// 里配的私有 CA 对测速完全无效，而路由器上时钟不准时连公网证书都会验不过。
		// 顺带的好处是点完测速就关掉面板，这一轮也会跑完，结果照样留在历史里。
		ctx, cancel := context.WithTimeout(server.ctx, timeout)
		defer cancel()

		// 并发测但要有上限：一份机场订阅动辄几十个节点，全放出去会把路由器的
		// 连接数和内存一起打满。10 是 urltest 组用的同一个数。
		batchRun, _ := batch.New(ctx, batch.WithConcurrencyNum[any](10))
		for _, tag := range provider.Nodes() {
			detour, loaded := server.outbound.Outbound(tag)
			if !loaded {
				// 订阅刚换过一轮、出站还没跟上时会短暂出现。
				continue
			}
			batchRun.Go(tag, func() (any, error) {
				realTag := group.RealTag(detour)
				delay, testErr := urltest.URLTest(ctx, link, detour)
				if testErr != nil {
					// 测不通就把旧数字删掉。留着会让一个已经死掉的节点在面板上
					// 继续挂着上次那个漂亮的延迟。
					server.urlTestHistory.DeleteURLTestHistory(realTag)
				} else {
					server.urlTestHistory.StoreURLTestHistory(realTag, &adapter.URLTestHistory{
						Time:  time.Now(),
						Delay: delay,
					})
				}
				return nil, nil
			})
		}
		batchRun.Wait()
		render.NoContent(w, r)
	}
}

func parseProviderName(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := getEscapeParam(r, "name")
		ctx := context.WithValue(r.Context(), CtxKeyProviderName, name)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func findProviderByName(ctx context.Context) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			manager := service.FromContext[adapter.SubscriptionManager](ctx)
			if manager == nil {
				render.Status(r, http.StatusNotFound)
				render.JSON(w, r, ErrNotFound)
				return
			}
			name := r.Context().Value(CtxKeyProviderName).(string)
			provider, found := manager.Subscription(name)
			if !found {
				render.Status(r, http.StatusNotFound)
				render.JSON(w, r, ErrNotFound)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), CtxKeyProvider, provider)))
		})
	}
}
