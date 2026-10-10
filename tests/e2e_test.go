// Package e2e exercises vrok through its real moving parts: a share registry,
// the HTTP server, the reverse proxy and a live relay with an agent attached.
//
// These tests are deliberately coarse. The unit tests next to each package
// check behaviour in isolation; what matters here is that the layers still fit
// together, because the properties vrok promises — a URL that works, Range
// support end to end, a share that disappears — only exist in the assembled
// system.
package e2e_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AliJabbar034/vrok/internal/relay"
	"github.com/AliJabbar034/vrok/internal/security"
	"github.com/AliJabbar034/vrok/internal/server"
	"github.com/AliJabbar034/vrok/internal/sharing"
	"github.com/AliJabbar034/vrok/internal/tunnel"
)

// stack is an assembled vrok: a registry, a factory and a running HTTP server.
type stack struct {
	t        *testing.T
	registry *sharing.Registry
	factory  *sharing.Factory
	server   *httptest.Server
}

func newStack(t *testing.T) *stack {
	t.Helper()

	registry := sharing.NewRegistry()
	// Argon2's real cost would dominate the runtime of these tests without
	// exercising anything they are about.
	hasher := security.NewArgon2Hasher(security.Argon2Params{Time: 1, Memory: 8 << 10, Threads: 1})

	key, err := security.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(server.Options{
		Resolver: registry,
		Hasher:   hasher,
		Signer:   security.NewHMACSigner(key),
	})
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return &stack{
		t:        t,
		registry: registry,
		factory:  sharing.NewFactory(security.NewCryptoTokenSource(), hasher, sharing.SystemClock{}),
		server:   ts,
	}
}

// share creates and registers a share, returning its absolute URL.
func (s *stack) share(args []string, opts sharing.Options) (*sharing.Share, string) {
	s.t.Helper()

	source, err := sharing.Classify(args)
	if err != nil {
		s.t.Fatalf("classify %v: %v", args, err)
	}
	share, err := s.factory.Create(source, opts)
	if err != nil {
		s.t.Fatalf("create share: %v", err)
	}
	if err := s.registry.Add(share); err != nil {
		s.t.Fatal(err)
	}
	return share, s.server.URL + server.SharePath(share.Token())
}

func TestShareAFileAndFetchItBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.mp4")
	content := randomBytes(t, 512*1024)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	s := newStack(t)
	_, url := s.share([]string{path}, sharing.Options{TTL: time.Hour})

	t.Run("the whole file comes back byte for byte", func(t *testing.T) {
		got := mustGet(t, url+"?raw=1")
		if len(got) != len(content) {
			t.Fatalf("downloaded %d bytes, want %d", len(got), len(content))
		}
		if string(got) != string(content) {
			t.Error("the downloaded bytes differ from the file on disk")
		}
	})

	t.Run("a resumed download stitches back together", func(t *testing.T) {
		// This is the property that matters for large files: fetch in two
		// ranged pieces and reassemble.
		half := len(content) / 2
		first := mustGetRange(t, url+"?raw=1", fmt.Sprintf("bytes=0-%d", half-1))
		second := mustGetRange(t, url+"?raw=1", fmt.Sprintf("bytes=%d-", half))

		joined := append(append([]byte{}, first...), second...)
		if string(joined) != string(content) {
			t.Errorf("reassembled %d bytes that do not match the original %d", len(joined), len(content))
		}
	})
}

func TestShareDisappearsWhenRevoked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newStack(t)
	share, url := s.share([]string{path}, sharing.Options{TTL: time.Hour})

	if code := statusOf(t, url); code != http.StatusOK {
		t.Fatalf("status before revoking = %d, want 200", code)
	}

	// This is vrok's central promise: stopping the share is the same thing as
	// the URL ceasing to exist.
	s.registry.Remove(share.ID())

	if code := statusOf(t, url); code != http.StatusNotFound {
		t.Errorf("status after revoking = %d, want 404", code)
	}
}

func TestDirectoryShareServesAReportTree(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, "screenshots"))
	mkdirAll(t, filepath.Join(root, "traces"))
	writeFile(t, filepath.Join(root, "index.html"), `<html><body>Report</body></html>`)
	writeFile(t, filepath.Join(root, "screenshots", "one.png"), "png")
	writeFile(t, filepath.Join(root, "traces", "trace.zip"), "zip")

	s := newStack(t)
	_, url := s.share([]string{root}, sharing.Options{TTL: time.Hour})

	// The common case for a directory share is a built artefact, so the root
	// serves its index rather than a file listing.
	if got := string(mustGet(t, url)); !strings.Contains(got, "Report") {
		t.Errorf("the share root did not serve index.html: %q", got)
	}
	if got := string(mustGet(t, url+"screenshots/one.png?raw=1")); got != "png" {
		t.Errorf("nested file = %q, want png", got)
	}
	if got := string(mustGet(t, url+"?list=1")); !strings.Contains(got, "traces") {
		t.Error("the forced listing does not show every entry")
	}
}

func TestProxyingALocalApplication(t *testing.T) {
	// A stand-in for a dev server: it echoes the path it was asked for and
	// sets a cookie scoped to the root, like most frameworks do.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc", Path: "/"})
			fmt.Fprint(w, "home")
		case "/api/echo":
			body, _ := io.ReadAll(r.Body)
			fmt.Fprintf(w, "echo:%s", body)
		case "/old":
			http.Redirect(w, r, "/new", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	s := newStack(t)
	_, url := s.share([]string{strings.TrimPrefix(upstream.URL, "http://")}, sharing.Options{TTL: time.Hour})

	t.Run("requests reach the application", func(t *testing.T) {
		if got := string(mustGet(t, url)); got != "home" {
			t.Errorf("body = %q, want home", got)
		}
	})

	t.Run("request bodies are forwarded", func(t *testing.T) {
		resp, err := http.Post(url+"api/echo", "text/plain", strings.NewReader("ping"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "echo:ping" {
			t.Errorf("body = %q, want echo:ping", body)
		}
	})

	t.Run("redirects stay inside the share", func(t *testing.T) {
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Get(url + "old")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		location := resp.Header.Get("Location")
		// Without rewriting, the visitor would be sent to /new and leave the
		// share prefix behind.
		if !strings.Contains(location, server.SharePath(tokenOf(url))) {
			t.Errorf("Location = %q, want it prefixed with the share path", location)
		}
	})

	t.Run("cookies are rescoped to the share", func(t *testing.T) {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		for _, cookie := range resp.Cookies() {
			if cookie.Name == "session" && !strings.HasPrefix(cookie.Path, "/s/") {
				t.Errorf("cookie path = %q, want it scoped to the share", cookie.Path)
			}
		}
	})

	t.Run("upstream 404s pass through", func(t *testing.T) {
		if code := statusOf(t, url+"missing"); code != http.StatusNotFound {
			t.Errorf("status = %d, want the upstream 404", code)
		}
	})
}

func TestRelayCarriesAShareEndToEnd(t *testing.T) {
	// The full public path: visitor -> relay -> tunnel -> agent -> local
	// server -> file on disk, with nothing stored in between.
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mp4")
	content := randomBytes(t, 256*1024)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	s := newStack(t)
	share, _ := s.share([]string{path}, sharing.Options{TTL: time.Hour})

	// A share id is also a DNS label, so it must satisfy the relay's rules.
	if !relay.ValidLabel(share.ID()) {
		t.Fatalf("share id %q is not a usable hostname label", share.ID())
	}

	const domain = "vrok.test"
	relayServer, err := relay.New(relay.Options{Domain: domain, Scheme: "http"})
	if err != nil {
		t.Fatal(err)
	}
	relayHTTP := httptest.NewServer(relayServer.Handler())
	defer relayHTTP.Close()

	agent, err := tunnel.Open("relay", tunnel.Config{
		RelayURL:   relayHTTP.URL,
		Label:      share.ID(),
		ShareToken: share.Token(),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	public, err := agent.Start(ctx, s.server.URL)
	if err != nil {
		t.Fatalf("start tunnel: %v", err)
	}
	defer agent.Stop(context.Background())

	wantURL := "http://" + share.ID() + "." + domain
	if public.String() != wantURL {
		t.Errorf("public URL = %q, want %q", public, wantURL)
	}

	// The relay routes on the Host header, so requests go to its real address
	// with the share's hostname attached.
	client := relayClient(relayHTTP.Listener.Addr().String())
	sharePath := public.Join(server.SharePath(share.Token()))

	t.Run("the file arrives intact through the tunnel", func(t *testing.T) {
		resp, err := client.Get(sharePath + "?raw=1")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		got, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(content) {
			t.Fatalf("received %d bytes through the relay, want %d", len(got), len(content))
		}
		if string(got) != string(content) {
			t.Error("the bytes received through the relay differ from the file")
		}
	})

	t.Run("range requests survive the relay", func(t *testing.T) {
		// Preserving Range across the tunnel is what keeps video seeking and
		// resumable downloads working on a public URL.
		req, _ := http.NewRequest(http.MethodGet, sharePath+"?raw=1", nil)
		req.Header.Set("Range", "bytes=1000-1099")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", resp.StatusCode)
		}
		got, _ := io.ReadAll(resp.Body)
		if string(got) != string(content[1000:1100]) {
			t.Error("the ranged bytes from the relay are not the ones requested")
		}
	})

	t.Run("an unknown hostname is not found", func(t *testing.T) {
		resp, err := client.Get("http://nosuchshare." + domain + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("the tunnel closes on Stop", func(t *testing.T) {
		if err := agent.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		// Give the relay a moment to notice the connection went away.
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := client.Get(sharePath)
			if err == nil {
				code := resp.StatusCode
				resp.Body.Close()
				if code == http.StatusNotFound {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Error("the share was still routable after the tunnel stopped")
	})
}

func TestRelayCarriesAnUploadIntoAnInbox(t *testing.T) {
	// The receiving direction over the public path: visitor -> relay ->
	// tunnel -> agent -> local server -> file in the inbox. The 8 MiB chunk
	// is the size upload.js sends.
	dir := t.TempDir()
	s := newStack(t)
	source, err := sharing.Inbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	share, err := s.factory.Create(source, sharing.Options{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.registry.Add(share); err != nil {
		t.Fatal(err)
	}

	const domain = "vrok.test"
	relayServer, err := relay.New(relay.Options{Domain: domain, Scheme: "http"})
	if err != nil {
		t.Fatal(err)
	}
	relayHTTP := httptest.NewServer(relayServer.Handler())
	defer relayHTTP.Close()

	agent, err := tunnel.Open("relay", tunnel.Config{
		RelayURL:   relayHTTP.URL,
		Label:      share.ID(),
		ShareToken: share.Token(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	public, err := agent.Start(ctx, s.server.URL)
	if err != nil {
		t.Fatalf("start tunnel: %v", err)
	}
	defer agent.Stop(context.Background())

	client := relayClient(relayHTTP.Listener.Addr().String())
	api := public.Join(server.SharePath(share.Token())) + "_upload"
	call := func(method, url string, body []byte) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, url, bytes.NewReader(body))
		req.Header.Set("X-Vrok-Upload", "1")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	content := randomBytes(t, 9<<20)
	offerURL := public.Join(server.SharePath(share.Token())) + "_offer"
	status, raw := call(http.MethodPost, offerURL, []byte(fmt.Sprintf(`{"files":[{"name":"clip.mp4","size":%d}]}`, len(content))))
	if status != http.StatusCreated {
		t.Fatalf("offer: status %d, body %s", status, raw)
	}
	var offered struct{ ID string }
	if err := json.Unmarshal([]byte(raw), &offered); err != nil {
		t.Fatal(err)
	}
	status, raw = call(http.MethodPost, api, []byte(fmt.Sprintf(`{"offer":%q,"name":"clip.mp4","size":%d}`, offered.ID, len(content))))
	if status != http.StatusCreated {
		t.Fatalf("begin: status %d, body %s", status, raw)
	}
	var begun struct{ ID string }
	if err := json.Unmarshal([]byte(raw), &begun); err != nil {
		t.Fatal(err)
	}
	upload := api + "/" + begun.ID

	const chunk = 8 << 20
	if status, raw := call(http.MethodPut, upload+"?offset=0", content[:chunk]); status != http.StatusOK {
		t.Fatalf("first chunk: status %d, body %s", status, raw)
	}
	if status, raw := call(http.MethodPut, fmt.Sprintf("%s?offset=%d", upload, chunk), content[chunk:]); status != http.StatusOK {
		t.Fatalf("second chunk: status %d, body %s", status, raw)
	}
	if status, raw := call(http.MethodPost, upload, nil); status != http.StatusOK {
		t.Fatalf("finish: status %d, body %s", status, raw)
	}

	got, err := os.ReadFile(filepath.Join(dir, "clip.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("the file saved through the relay differs from the one sent (%d bytes, want %d)", len(got), len(content))
	}
}

func TestLocalTunnelReportsAReachableAddress(t *testing.T) {
	local, err := tunnel.Open("local", tunnel.Config{})
	if err != nil {
		t.Fatal(err)
	}

	// A loopback target is already correct and must be left alone.
	got, err := local.Start(context.Background(), "http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "http://127.0.0.1:8080" {
		t.Errorf("Start = %q, want the target unchanged", got)
	}

	// A wildcard bind is useless to another device, so it is replaced with
	// the machine's LAN address.
	wildcard, err := local.Start(context.Background(), "http://0.0.0.0:8080")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wildcard.String(), "0.0.0.0") {
		t.Errorf("Start = %q, want a routable address", wildcard)
	}
	if !strings.HasSuffix(wildcard.String(), ":8080") {
		t.Errorf("Start = %q, want the port preserved", wildcard)
	}
}

// relayClient returns a client that resolves every hostname to the relay's
// address, which is how a wildcard DNS record behaves in production.
func relayClient(addr string) *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func tokenOf(shareURL string) string {
	_, rest, _ := strings.Cut(shareURL, "/s/")
	token, _, _ := strings.Cut(rest, "/")
	return token
}

func mustGet(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s returned %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func mustGetRange(t *testing.T, url, spec string) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", spec)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range %s returned %d, want 206", spec, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func statusOf(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return buf
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}
