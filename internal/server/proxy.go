package server

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/AliJabbar034/vrok/web/viewer"
)

// ProxyHandler exposes a local HTTP service through a share URL.
//
// It is a thin reverse proxy on purpose: HTTP semantics are preserved
// end-to-end so WebSockets, server-sent events, streaming responses and range
// requests all keep working exactly as they do against localhost.
type ProxyHandler struct {
	pages  *pages
	logger *slog.Logger

	mu      sync.RWMutex
	proxies map[string]*httputil.ReverseProxy
}

// NewProxyHandler returns a handler that proxies HTTP shares.
func NewProxyHandler(p *pages, logger *slog.Logger) *ProxyHandler {
	return &ProxyHandler{pages: p, logger: logger, proxies: make(map[string]*httputil.ReverseProxy)}
}

// proxyTransport is tuned for a local upstream: connect fast, fail fast, and
// never buffer or re-compress, since the hop is over loopback.
var proxyTransport = &http.Transport{
	DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	MaxIdleConnsPerHost: 32,
	IdleConnTimeout:     90 * time.Second,
	DisableCompression:  true,
	// No ResponseHeaderTimeout: a dev server holding a long-poll or SSE
	// connection open is normal behaviour, not a stall.
}

// ServeShare implements ShareHandler.
func (h *ProxyHandler) ServeShare(w http.ResponseWriter, r *http.Request, sr *shareRequest) {
	proxy, err := h.proxyFor(sr)
	if err != nil {
		h.pages.serverError(w, r, err)
		return
	}

	// The upstream knows nothing about vrok's /s/<token> prefix, so the
	// request is rewritten to the path the app actually serves. Everything
	// else about the request is passed through untouched.
	out := r.Clone(r.Context())
	out.URL.Path = "/" + strings.TrimPrefix(sr.Rel, "/")
	out.URL.RawPath = ""

	sr.Share.Touch(sr.Now)
	cw := &countingWriter{ResponseWriter: w}
	proxy.ServeHTTP(cw, out)
	sr.Share.AddBytes(cw.n)
}

// proxyFor returns the cached proxy for a share, building it on first use.
func (h *ProxyHandler) proxyFor(sr *shareRequest) (*httputil.ReverseProxy, error) {
	h.mu.RLock()
	proxy, ok := h.proxies[sr.Spec.ID]
	h.mu.RUnlock()
	if ok {
		return proxy, nil
	}

	targetURL, err := url.Parse(sr.Spec.Target)
	if err != nil {
		return nil, err
	}
	built := h.build(targetURL, NewLinks(sr.Spec.Token))

	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.proxies[sr.Spec.ID]; ok {
		return existing, nil
	}
	h.proxies[sr.Spec.ID] = built
	return built, nil
}

func (h *ProxyHandler) build(target *url.URL, links Links) *httputil.ReverseProxy {
	base := strings.TrimSuffix(links.Root(), "/")

	return &httputil.ReverseProxy{
		Transport: proxyTransport,
		// Flush every write so streaming responses, hot reload and SSE are
		// not held back by proxy buffering.
		FlushInterval: -1,

		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// Dev servers and frameworks route on Host; give them the one
			// they are configured for rather than the share's hostname.
			pr.Out.Host = target.Host
			pr.SetXForwarded()
			stripVrokCookies(pr.Out.Header)
		},

		ModifyResponse: func(resp *http.Response) error {
			rewriteLocation(resp, target, base)
			rewriteCookiePaths(resp, base+"/")
			return nil
		},

		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			h.upstreamUnavailable(w, r, target, err)
		},
	}
}

// stripVrokCookies removes vrok's own cookies from a request bound for the
// upstream app. The unlock proof belongs to vrok; the shared app has no use for
// it and should not be handed it. The header is filtered as text rather than
// parsed and re-encoded, so the app's own cookies arrive byte for byte.
func stripVrokCookies(h http.Header) {
	lines := h.Values("Cookie")
	if len(lines) == 0 {
		return
	}
	h.Del("Cookie")
	for _, line := range lines {
		kept := make([]string, 0, strings.Count(line, ";")+1)
		for _, pair := range strings.Split(line, ";") {
			pair = strings.TrimSpace(pair)
			if pair == "" || strings.HasPrefix(pair, cookiePrefix) {
				continue
			}
			kept = append(kept, pair)
		}
		if len(kept) > 0 {
			h.Add("Cookie", strings.Join(kept, "; "))
		}
	}
}

// rewriteLocation keeps redirects inside the share.
//
// An app that redirects to /login would otherwise send the visitor out of the
// share prefix and into a 404.
func rewriteLocation(resp *http.Response, target *url.URL, base string) {
	location := resp.Header.Get("Location")
	if location == "" {
		return
	}

	u, err := url.Parse(location)
	if err != nil {
		return
	}
	switch {
	case u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/"):
		u.Path = base + u.Path
	case u.Host == target.Host:
		// Absolute redirect back to the upstream origin: make it relative to
		// the share so the visitor never sees localhost.
		u.Scheme, u.Host = "", ""
		u.Path = base + u.Path
	default:
		return // off-site redirect; leave it alone
	}
	resp.Header.Set("Location", u.String())
}

// rewriteCookiePaths rescopes upstream cookies to the share prefix, so a
// session cookie set for "/" is still sent back on share URLs.
func rewriteCookiePaths(resp *http.Response, base string) {
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) == 0 {
		return
	}

	rewritten := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		parts := strings.Split(cookie, ";")
		replaced := false
		for i, part := range parts {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(part)), "path=") {
				parts[i] = " Path=" + base
				replaced = true
			}
		}
		if !replaced {
			parts = append(parts, " Path="+base)
		}
		rewritten = append(rewritten, strings.Join(parts, ";"))
	}

	resp.Header.Del("Set-Cookie")
	for _, cookie := range rewritten {
		resp.Header.Add("Set-Cookie", cookie)
	}
}

// upstreamUnavailable explains a dead upstream instead of showing a bare 502.
// The usual cause is that the owner's dev server is still starting up or has
// been stopped, and the visitor can simply retry.
func (h *ProxyHandler) upstreamUnavailable(w http.ResponseWriter, r *http.Request, target *url.URL, err error) {
	h.logger.Debug("upstream unavailable",
		slog.String("target", target.String()),
		slog.String("path", r.URL.Path),
		slog.String("error", err.Error()))

	noStore(w)
	w.Header().Set("Retry-After", "5")
	data := viewer.GonePage{
		Meta:    viewer.Meta{Title: "Service unavailable · vrok", StaticBase: staticPrefix},
		Icon:    "🔌",
		Heading: "Service unavailable",
		Message: "The shared application is not responding. It may still be starting up.",
	}
	if renderErr := h.pages.render.Gone(w, http.StatusBadGateway, data); renderErr != nil {
		h.pages.fail(w)
	}
}
