package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

// The Runtime console owns the native DSH connection. Go only connects to
// the private Unix socket and never sends the workbench session to DSH.
func registerDSHBot(mux *http.ServeMux, origin string, identities *identity.Service, roles *access.Service) {
	public, _ := url.Parse(origin)
	authorize := func(r *http.Request) int {
		p, status := identities.NativeBrowserSession(r)
		if status != 0 {
			return status
		}
		allowed, err := roles.Check(r.Context(), p, "dsh.debug", "")
		if err != nil {
			return http.StatusServiceUnavailable
		}
		if !allowed {
			return http.StatusForbidden
		}
		return 0
	}
	socket := os.Getenv("OMNIRELAY_DSHBOT_SOCKET")
	if socket == "" {
		socket = "/run/omnirelay-dshbot/web.sock"
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}, IdleConnTimeout: 30 * time.Second}
	handler := nativeDSHProxy(public, transport, authorize)
	mux.Handle("/dshbot/", handler)
}

func nativeDSHProxy(public *url.URL, transport http.RoundTripper, authorize func(*http.Request) int) http.Handler {
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(&url.URL{Scheme: "http", Host: "dshbot"})
			p.Out.Host = public.Host
			p.Out.Header.Del("Authorization")
			p.Out.Header.Del("Referer")
			p.Out.Header.Del("Cookie")
			p.Out.Header.Del("X-CSRF-Token")
		},
		ModifyResponse: func(res *http.Response) error {
			res.Header.Del("Set-Cookie")
			res.Header.Set("Cache-Control", "no-store")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "DSH 调试服务暂不可用，请稍后刷新。", http.StatusServiceUnavailable)
		},
	}
	handler := http.StripPrefix("/dshbot", proxy)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status := authorize(r); status != 0 {
			http.Error(w, http.StatusText(status), status)
			return
		}
		origins := r.Header.Values("Origin")
		upgrade := r.Header.Get("Upgrade") != ""
		needsOrigin := upgrade || (r.Method != http.MethodGet && r.Method != http.MethodHead)
		wrongOrigin := (needsOrigin || len(origins) > 0) && (len(origins) != 1 || origins[0] != public.String())
		site := r.Header.Get("Sec-Fetch-Site")
		if wrongOrigin || site == "cross-site" || site == "same-site" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		// Prevent a browser worker from caching an authenticated console after logout.
		if r.Header.Get("Service-Worker") != "" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		if upgrade || strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			go func() {
				ticker := time.NewTicker(2 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if authorize(r.WithContext(ctx)) != 0 {
							cancel()
							return
						}
					}
				}
			}()
		}
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'; base-uri 'self'")
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
}
