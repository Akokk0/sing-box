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

func providerInfo(provider adapter.OutboundProvider) render.M {
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
		if manager := service.FromContext[adapter.OutboundProviderManager](ctx); manager != nil {
			for _, provider := range manager.Providers() {
				providers[provider.Tag()] = providerInfo(provider)
			}
		}
		render.JSON(w, r, render.M{"providers": providers})
	}
}

func getProvider(w http.ResponseWriter, r *http.Request) {
	provider := r.Context().Value(CtxKeyProvider).(adapter.OutboundProvider)
	render.JSON(w, r, providerInfo(provider))
}

func updateProvider(w http.ResponseWriter, r *http.Request) {
	provider := r.Context().Value(CtxKeyProvider).(adapter.OutboundProvider)
	if err := provider.Update(); err != nil {
		render.Status(r, http.StatusServiceUnavailable)
		render.JSON(w, r, newError(err.Error()))
		return
	}
	render.NoContent(w, r)
}

// healthCheckProvider 无事可做：测速是 urltest 组自己的事，provider 只管节点从哪来。
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
			manager := service.FromContext[adapter.OutboundProviderManager](ctx)
			if manager == nil {
				render.Status(r, http.StatusNotFound)
				render.JSON(w, r, ErrNotFound)
				return
			}
			name := r.Context().Value(CtxKeyProviderName).(string)
			provider, found := manager.Provider(name)
			if !found {
				render.Status(r, http.StatusNotFound)
				render.JSON(w, r, ErrNotFound)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), CtxKeyProvider, provider)))
		})
	}
}
