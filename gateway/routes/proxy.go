package routes

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func NewProxy(target *url.URL, stripPrefix string) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	logger := log.Default()
	basePath := strings.TrimSuffix(stripPrefix, "/")
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
		path := req.URL.Path
		if basePath != "" && strings.HasPrefix(path, basePath) {
			path = strings.TrimPrefix(path, basePath)
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		req.URL.Path = singleJoiningSlash(target.Path, path)
		req.URL.RawPath = req.URL.EscapedPath()
		otel.GetTextMapPropagator().Inject(req.Context(), propagation.HeaderCarrier(req.Header))
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Printf("proxy error: %v", err)
		http.Error(w, "upstream error", http.StatusBadGateway)
	}
	proxy.Transport = otelhttp.NewTransport(http.DefaultTransport)
	return proxy
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
