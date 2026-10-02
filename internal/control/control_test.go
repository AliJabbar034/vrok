package control_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AliJabbar034/vrok/internal/control"
)

// fakeProvider stands in for a running share process.
type fakeProvider struct {
	mu      sync.Mutex
	shares  []control.ShareInfo
	revoked []string
	stopped bool
}

func (p *fakeProvider) Shares() []control.ShareInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.shares
}

func (p *fakeProvider) Revoke(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.revoked = append(p.revoked, id)
	for i, s := range p.shares {
		if s.ID == id {
			p.shares = append(p.shares[:i], p.shares[i+1:]...)
			return true
		}
	}
	return false
}

func (p *fakeProvider) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
}

func (p *fakeProvider) wasStopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

// startSession exposes a provider on a socket inside a temporary state
// directory, skipping the test when the environment forbids Unix sockets.
func startSession(t *testing.T, provider control.Provider) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", shortStateDir(t))

	server := control.NewServer(provider, nil)
	if err := server.Start(); err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("the environment does not allow Unix sockets: %v", err)
		}
		t.Fatalf("start control server: %v", err)
	}
	t.Cleanup(func() { server.Close() })
}

func TestSessionsSeeAnotherProcessShares(t *testing.T) {
	provider := &fakeProvider{shares: []control.ShareInfo{
		{ID: "a82kd9", Name: "demo.mp4", Kind: "file", Downloads: 2, MaxDownloads: 5, CreatedAt: time.Now()},
		{ID: "b18ks2", Name: "report.pdf", Kind: "file", CreatedAt: time.Now().Add(time.Second)},
	}}
	startSession(t, provider)

	ctx := context.Background()
	sessions, err := control.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("found %d sessions, want 1", len(sessions))
	}

	shares, err := control.AllShares(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 2 {
		t.Fatalf("listed %d shares, want 2", len(shares))
	}
	// The client fills in which process owns each share.
	if shares[0].PID != sessions[0].PID {
		t.Errorf("share PID = %d, want %d", shares[0].PID, sessions[0].PID)
	}
	if shares[0].ID != "a82kd9" || shares[0].Downloads != 2 {
		t.Errorf("share data did not survive the round trip: %+v", shares[0])
	}
}

func TestRevokeAndStopReachTheProvider(t *testing.T) {
	provider := &fakeProvider{shares: []control.ShareInfo{{ID: "a82kd9", Name: "demo.mp4"}}}
	startSession(t, provider)

	sessions, err := control.Sessions()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("Sessions returned %v, %v", sessions, err)
	}
	client := control.Dial(sessions[0])
	ctx := context.Background()

	revoked, err := client.Revoke(ctx, "a82kd9")
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Error("Revoke reported the share as unknown")
	}

	// An id this process does not own must be reported as such, so the CLI
	// can ask the next session.
	revoked, err = client.Revoke(ctx, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if revoked {
		t.Error("Revoke claimed an unknown share")
	}

	if err := client.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	// Stop is answered before the process tears itself down, so the call
	// returns first and the shutdown follows.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if provider.wasStopped() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("Stop never reached the provider")
}

func TestNoSessionsWhenNothingIsRunning(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", shortStateDir(t))

	sessions, err := control.Sessions()
	if err != nil {
		t.Fatalf("Sessions on an empty state directory returned %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("found %d sessions, want none", len(sessions))
	}

	shares, err := control.AllShares(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 0 {
		t.Errorf("listed %d shares, want none", len(shares))
	}
}

func TestStaleSocketsAreCleanedUp(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", shortStateDir(t))

	// A process killed with SIGKILL leaves its socket behind. It must not
	// show up as a live session, and it must not linger.
	provider := &fakeProvider{}
	server := control.NewServer(provider, nil)
	if err := server.Start(); err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("the environment does not allow Unix sockets: %v", err)
		}
		t.Fatal(err)
	}
	if sessions, _ := control.Sessions(); len(sessions) != 1 {
		t.Fatalf("the live session was not discovered")
	}

	// Closing without removing the socket file is not possible through the
	// API, so emulate a crash by closing the listener and leaving the path.
	server.Close()

	sessions, err := control.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Errorf("found %d sessions after the process went away, want none", len(sessions))
	}
}
