package relay

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AliJabbar034/vrok/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestHostLabelExtraction(t *testing.T) {
	r, err := New(Options{Domain: "vrok.example.com"})
	if err != nil {
		t.Fatal(err)
	}

	valid := map[string]string{
		"a82kd9.vrok.example.com":      "a82kd9",
		"A82KD9.VROK.EXAMPLE.COM":      "a82kd9",
		"a82kd9.vrok.example.com:8787": "a82kd9",
		"a82kd9.vrok.example.com.":     "a82kd9",
	}
	for host, want := range valid {
		got, ok := r.hostLabel(host)
		if !ok {
			t.Errorf("hostLabel(%q) was rejected", host)
			continue
		}
		if got != want {
			t.Errorf("hostLabel(%q) = %q, want %q", host, got, want)
		}
	}

	invalid := []string{
		"vrok.example.com",                // the apex is not a share
		"other.com",                       // a different domain
		"a82kd9.vrok.example.com.evil.co", // suffix must match exactly
		// A nested label must not resolve: accepting it would let one share
		// claim hostnames that look like they belong to another.
		"deep.a82kd9.vrok.example.com",
		"",
	}
	for _, host := range invalid {
		if got, ok := r.hostLabel(host); ok {
			t.Errorf("hostLabel(%q) = %q, want rejection", host, got)
		}
	}
}

func TestValidLabel(t *testing.T) {
	// A share id becomes part of a hostname, so the relay validates it rather
	// than trusting whatever the agent sends.
	for _, label := range []string{"a82kd9", "abcd", "share-one", "x7y8z9abc"} {
		if !ValidLabel(label) {
			t.Errorf("ValidLabel(%q) = false, want true", label)
		}
	}
	for _, label := range []string{"", "ab", "-abcd", "AB82KD9", "a.b.c", "a_b_c", "a82kd9/../x"} {
		if ValidLabel(label) {
			t.Errorf("ValidLabel(%q) = true, want false", label)
		}
	}
}

func TestNewRequiresADomain(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Error("New accepted options with no domain")
	}
}

func TestAgentRegistryOwnership(t *testing.T) {
	registry := NewAgentRegistry(2)
	first := &Agent{ID: "share1"}
	second := &Agent{ID: "share1"}

	if err := registry.Add(first); err != nil {
		t.Fatal(err)
	}
	if err := registry.Add(second); !errors.Is(err, ErrLabelTaken) {
		t.Errorf("Add of a duplicate label returned %v, want ErrLabelTaken", err)
	}

	// A late Remove from a replaced agent must not evict the current one.
	registry.Remove(second)
	if _, ok := registry.Get("share1"); !ok {
		t.Error("removing a stale agent evicted the live one")
	}

	registry.Remove(first)
	if _, ok := registry.Get("share1"); ok {
		t.Error("the agent is still registered after removal")
	}
}

func TestAgentRegistryCapacity(t *testing.T) {
	registry := NewAgentRegistry(1)
	if err := registry.Add(&Agent{ID: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Add(&Agent{ID: "two"}); !errors.Is(err, ErrAtCapacity) {
		t.Errorf("Add beyond capacity returned %v, want ErrAtCapacity", err)
	}
}

func TestHopByHopHeadersAreNotForwarded(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://a82kd9.vrok.test/s/tok/", nil)
	req.RemoteAddr = "203.0.113.7:51234"
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Transfer-Encoding", "chunked")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Range", "bytes=0-99")
	req.Header.Set("Cookie", "vrok_abc=proof")

	forwarded := forwardedHeader(req)

	for _, name := range []string{"Connection", "Transfer-Encoding", "Upgrade"} {
		if _, present := forwarded[name]; present {
			t.Errorf("%s was forwarded, but it describes a single hop", name)
		}
	}
	// End-to-end headers must survive: Range is what makes seeking work, and
	// the cookie is what carries the unlock proof.
	if forwarded["Range"][0] != "bytes=0-99" {
		t.Error("the Range header was not forwarded")
	}
	if forwarded["Cookie"][0] != "vrok_abc=proof" {
		t.Error("the Cookie header was not forwarded")
	}
	if forwarded["X-Forwarded-For"][0] != "203.0.113.7" {
		t.Errorf("X-Forwarded-For = %v, want the visitor's address", forwarded["X-Forwarded-For"])
	}
	if forwarded["X-Forwarded-Proto"][0] != "https" {
		t.Error("X-Forwarded-Proto was not set to https")
	}
}

func TestResponseHeadersStripHopByHop(t *testing.T) {
	dst := http.Header{}
	copyHeader(dst, map[string][]string{
		"Content-Type":      {"video/mp4"},
		"Content-Range":     {"bytes 0-99/1000"},
		"Transfer-Encoding": {"chunked"},
		"Connection":        {"close"},
	})

	if dst.Get("Content-Range") != "bytes 0-99/1000" {
		t.Error("Content-Range was dropped, which breaks ranged responses")
	}
	if dst.Get("Transfer-Encoding") != "" || dst.Get("Connection") != "" {
		t.Error("a hop-by-hop header was copied to the visitor")
	}
}

func TestUnknownHostReturnsNotFound(t *testing.T) {
	r, err := New(Options{Domain: "vrok.test"})
	if err != nil {
		t.Fatal(err)
	}

	for _, host := range []string{"vrok.test", "nosuchshare.vrok.test"} {
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil)
		req.Host = host
		rec := httptest.NewRecorder()

		r.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s returned %d, want 404", host, rec.Code)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Error("a missing share response is cacheable")
		}
	}
}

func TestProtocolUpgradesAreRefusedClearly(t *testing.T) {
	r, err := New(Options{Domain: "vrok.test"})
	if err != nil {
		t.Fatal(err)
	}
	agent := &Agent{ID: "abcd1234"}
	if err := r.agents.Add(agent); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://abcd1234.vrok.test/ws", nil)
	req.Host = "abcd1234.vrok.test"
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()

	r.Handler().ServeHTTP(rec, req)

	// Saying "not implemented" is better than letting the handshake hang on a
	// relay that cannot carry it.
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("a WebSocket upgrade returned %d, want 501", rec.Code)
	}
}

// /healthz is reachable by anyone, so it reports liveness and nothing else.
// How many agents are connected is how many people are currently sharing,
// which is not a stranger's business.
func TestHealthRevealsNothingButLiveness(t *testing.T) {
	r, err := New(Options{Domain: "vrok.test"})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "ok\n" {
		t.Errorf("body = %q, want %q", body, "ok\n")
	}
	if strings.Contains(rec.Body.String(), "agents") {
		t.Error("/healthz discloses the number of connected agents")
	}
}

// A relay configured with only half of a TLS pair would bind the port a
// browser speaks TLS to and answer in plaintext. Refusing at startup turns a
// confusing production failure into a message the operator reads immediately.
func TestHalfConfiguredTLSIsRefused(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{"certificate without key", Options{Domain: "x.example.com", TLSCert: "c.pem"}},
		{"key without certificate", Options{Domain: "x.example.com", TLSKey: "k.pem"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.opts); err == nil {
				t.Fatal("New accepted a half-configured TLS pair")
			}
		})
	}
}

// Terminating TLS is optional: a relay behind a load balancer that already
// does it must still start.
func TestTLSIsOptional(t *testing.T) {
	r, err := New(Options{Domain: "x.example.com"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if r.opts.servesTLS() {
		t.Error("a relay with no certificate should not claim to terminate TLS")
	}
}

// One oversized message must cost the relay a dropped connection, not its
// memory: a relay with no auth token accepts agents from anyone.
func TestOversizedAgentMessagesAreRefused(t *testing.T) {
	r, err := New(Options{Domain: "vrok.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(r.Handler())
	defer server.Close()

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+protocol.AgentPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	huge := make([]byte, protocol.MaxMessageSize+1)
	// The relay may close mid-write; either outcome of the write is fine.
	_ = ws.WriteMessage(websocket.TextMessage, huge)

	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) && closeErr.Code != websocket.CloseMessageTooBig {
				t.Errorf("relay closed with %d, want %d", closeErr.Code, websocket.CloseMessageTooBig)
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				t.Fatal("relay kept reading an oversized message instead of dropping the agent")
			}
			return
		}
	}
}
