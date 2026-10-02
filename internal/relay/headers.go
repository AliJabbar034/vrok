package relay

import (
	"net"
	"net/http"
	"strings"
)

// hopByHop headers describe one TCP hop and must not be forwarded across a
// proxy. Passing Transfer-Encoding or Connection through would make the
// receiving side frame the message twice.
var hopByHop = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"proxy-connection":    true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// forwardedHeader prepares a visitor's headers for the agent: hop-by-hop
// fields are dropped and the standard forwarding fields are added so the
// share's own server can tell how the visitor reached it.
func forwardedHeader(r *http.Request) map[string][]string {
	out := make(map[string][]string, len(r.Header)+3)
	for name, values := range r.Header {
		if hopByHop[strings.ToLower(name)] {
			continue
		}
		copied := make([]string, len(values))
		copy(copied, values)
		out[name] = copied
	}

	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		// Append rather than replace: the relay may itself sit behind a load
		// balancer that already recorded a hop.
		out["X-Forwarded-For"] = append(out["X-Forwarded-For"], host)
	}
	// The relay always terminates TLS, so the visitor's scheme is https even
	// though the tunnel itself carries plain frames.
	out["X-Forwarded-Proto"] = []string{"https"}
	out["X-Forwarded-Host"] = []string{r.Host}
	return out
}

// copyHeader moves response headers from the agent to the visitor, dropping
// hop-by-hop fields.
func copyHeader(dst http.Header, src map[string][]string) {
	for name, values := range src {
		if hopByHop[strings.ToLower(name)] {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

// isUpgrade reports whether a request asks to switch protocols.
//
// The relay forwards HTTP request/response pairs, not arbitrary bidirectional
// byte streams, so a WebSocket upgrade cannot be carried across it yet. Saying
// so explicitly is better than letting the handshake stall.
func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Connection"), "upgrade") ||
		r.Header.Get("Upgrade") != ""
}
