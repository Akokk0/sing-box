package clashapi

import (
	"context"
	"net/http"

	"github.com/sagernet/sing-box/adapter"
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
func proxyProviderRouter(ctx context.Context) http.Handler {
	r := chi.NewRouter()
	r.Get("/", getProviders(ctx))

	r.Route("/{name}", func(r chi.Router) {
		r.Use(parseProviderName, findProviderByName(ctx))
		r.Get("/", getProvider)
		r.Put("/", updateProvider)
		r.Get("/healthcheck", healthCheckProvider)
	})
	return r
}

func providerInfo(provider adapter.Subscription) render.M {
	return render.M{
		"name":        provider.Tag(),
		"type":        "Proxy",
		"vehicleType": "HTTP",
		"proxies":     provider.Nodes(),
	}
}

func getProviders(ctx context.Context) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		providers := render.M{}
		if manager := service.FromContext[adapter.SubscriptionManager](ctx); manager != nil {
			for _, provider := range manager.Subscriptions() {
				providers[provider.Tag()] = providerInfo(provider)
			}
		}
		render.JSON(w, r, render.M{"providers": providers})
	}
}

func getProvider(w http.ResponseWriter, r *http.Request) {
	provider := r.Context().Value(CtxKeyProvider).(adapter.Subscription)
	render.JSON(w, r, providerInfo(provider))
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

// healthCheckProvider 无事可做：测速是 urltest 组自己的事，订阅只管节点从哪来。
func healthCheckProvider(w http.ResponseWriter, r *http.Request) {
	render.NoContent(w, r)
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
