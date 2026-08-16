package providerproxy

import (
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
)

type Config struct {
	Upstream *url.URL
	APIKey   string
}

func New(cfg Config) (http.Handler, error) {
	if cfg.Upstream == nil || cfg.Upstream.Scheme == "" || cfg.Upstream.Host == "" {
		return nil, errors.New("provider proxy: valid upstream URL is required")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("provider proxy: API key is required")
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = cfg.Upstream.Scheme
			req.URL.Host = cfg.Upstream.Host
			req.URL.Path = joinURLPath(cfg.Upstream.Path, req.URL.Path)
			req.Host = cfg.Upstream.Host
			stripCredentialHeaders(req.Header)
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "provider upstream unavailable", http.StatusBadGateway)
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/", proxy)
	return mux, nil
}

func stripCredentialHeaders(header http.Header) {
	for _, name := range []string{
		"Authorization",
		"Proxy-Authorization",
		"X-Api-Key",
		"Api-Key",
		"X-Dashscope-Api-Key",
	} {
		header.Del(name)
	}
}

func joinURLPath(basePath, requestPath string) string {
	joined := path.Join("/", basePath, requestPath)
	if strings.HasSuffix(requestPath, "/") && !strings.HasSuffix(joined, "/") {
		joined += "/"
	}
	return joined
}
