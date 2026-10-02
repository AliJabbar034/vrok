package sharing_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/AliJabbar034/vrok/internal/sharing"
)

// fixedClock makes expiry testable without sleeping.
type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

// stubTokens hands out predictable identifiers.
type stubTokens struct{ n int }

func (s *stubTokens) NewID() (string, error) {
	s.n++
	return "id" + string(rune('a'+s.n-1)), nil
}

func (s *stubTokens) NewToken() (string, error) {
	return "token" + string(rune('a'+s.n-1)), nil
}

type stubHasher struct{}

func (stubHasher) Hash(password string) (string, error) { return "hashed:" + password, nil }

func TestGuardsEnforceAvailability(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	guards := sharing.DefaultGuards()

	cases := []struct {
		name string
		snap sharing.Snapshot
		want error
	}{
		{
			name: "live share passes",
			snap: sharing.Snapshot{Spec: sharing.Spec{ExpiresAt: now.Add(time.Hour)}},
		},
		{
			name: "no ttl never expires",
			snap: sharing.Snapshot{},
		},
		{
			name: "past expiry fails",
			snap: sharing.Snapshot{Spec: sharing.Spec{ExpiresAt: now.Add(-time.Second)}},
			want: sharing.ErrExpired,
		},
		{
			name: "download limit reached fails",
			snap: sharing.Snapshot{Spec: sharing.Spec{MaxDownloads: 2}, Downloads: 2},
			want: sharing.ErrDownloadLimit,
		},
		{
			name: "revoked fails",
			snap: sharing.Snapshot{Revoked: true},
			want: sharing.ErrRevoked,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := guards.Check(tc.snap, now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Check returned %v, want %v", err, tc.want)
			}
		})
	}
}

func TestClaimDownloadIsAHardLimitUnderConcurrency(t *testing.T) {
	// The limit has to hold when many visitors arrive at once, which is why
	// claiming is a single locked read-modify-write rather than a check
	// followed by an increment.
	const limit = 5
	share := sharing.New(sharing.Spec{ID: "x", Token: "t", MaxDownloads: limit})

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		granted int
		refused int
	)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := share.ClaimDownload(time.Now())
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				granted++
			} else {
				refused++
			}
		}()
	}
	wg.Wait()

	if granted != limit {
		t.Errorf("granted %d downloads, want exactly %d", granted, limit)
	}
	if refused != 200-limit {
		t.Errorf("refused %d requests, want %d", refused, 200-limit)
	}
	if got := share.Snapshot().Downloads; got != limit {
		t.Errorf("snapshot reports %d downloads, want %d", got, limit)
	}
}

func TestReleaseDownloadReturnsTheAllowance(t *testing.T) {
	share := sharing.New(sharing.Spec{ID: "x", Token: "t", MaxDownloads: 1})

	if _, err := share.ClaimDownload(time.Now()); err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	if _, err := share.ClaimDownload(time.Now()); !errors.Is(err, sharing.ErrDownloadLimit) {
		t.Fatalf("second claim returned %v, want ErrDownloadLimit", err)
	}

	// A transfer that delivered nothing must not consume the allowance.
	share.ReleaseDownload()
	if _, err := share.ClaimDownload(time.Now()); err != nil {
		t.Fatalf("claim after release failed: %v", err)
	}
}

func TestRegistryLookupAndRemoval(t *testing.T) {
	reg := sharing.NewRegistry()
	share := sharing.New(sharing.Spec{ID: "abc123", Token: "secret-token"})

	if err := reg.Add(share); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := reg.Add(share); !errors.Is(err, sharing.ErrDuplicate) {
		t.Errorf("adding the same share twice returned %v, want ErrDuplicate", err)
	}

	if got, err := reg.ByToken("secret-token"); err != nil || got != share {
		t.Errorf("ByToken returned (%v, %v)", got, err)
	}
	if got, err := reg.ByID("abc123"); err != nil || got != share {
		t.Errorf("ByID returned (%v, %v)", got, err)
	}
	if _, err := reg.ByToken("wrong"); !errors.Is(err, sharing.ErrNotFound) {
		t.Errorf("ByToken with a wrong token returned %v, want ErrNotFound", err)
	}

	if !reg.Remove("abc123") {
		t.Error("Remove reported the share as missing")
	}
	if _, err := reg.ByToken("secret-token"); !errors.Is(err, sharing.ErrNotFound) {
		t.Error("the token still resolves after removal")
	}
	// Removal must also revoke, so a request already holding the share stops.
	if !share.Snapshot().Revoked {
		t.Error("Remove did not revoke the share")
	}
}

func TestPurgeExpiredDropsOnlyDeadShares(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	reg := sharing.NewRegistry()

	live := sharing.New(sharing.Spec{ID: "live", Token: "t1", ExpiresAt: now.Add(time.Hour)})
	dead := sharing.New(sharing.Spec{ID: "dead", Token: "t2", ExpiresAt: now.Add(-time.Hour)})
	forever := sharing.New(sharing.Spec{ID: "forever", Token: "t3"})

	for _, s := range []*sharing.Share{live, dead, forever} {
		if err := reg.Add(s); err != nil {
			t.Fatal(err)
		}
	}

	expired := reg.PurgeExpired(now)
	if len(expired) != 1 || expired[0] != dead {
		t.Fatalf("PurgeExpired returned %d shares, want only the expired one", len(expired))
	}
	if reg.Len() != 2 {
		t.Errorf("registry holds %d shares, want 2", reg.Len())
	}
}

func TestFactoryMintsUniqueCredentialsAndHashesPasswords(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
	factory := sharing.NewFactory(&stubTokens{}, stubHasher{}, clock)

	share, err := factory.Create(
		sharing.Source{Kind: sharing.KindFile, Entries: []sharing.Entry{{Name: "a.txt", Path: "/tmp/a.txt"}}, Name: "a.txt"},
		sharing.Options{TTL: 30 * time.Minute, MaxDownloads: 3, Password: "hunter2"},
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	spec := share.Spec()
	if spec.ExpiresAt != clock.now.Add(30*time.Minute) {
		t.Errorf("ExpiresAt = %v, want now+30m", spec.ExpiresAt)
	}
	if spec.PasswordHash != "hashed:hunter2" {
		t.Errorf("PasswordHash = %q, want the hashed form", spec.PasswordHash)
	}
	if !spec.Protected() {
		t.Error("a share created with a password is not marked protected")
	}

	// A zero TTL must mean "no expiry", not "expired immediately".
	noTTL, err := factory.Create(
		sharing.Source{Kind: sharing.KindHTTP, Target: "http://127.0.0.1:3000"},
		sharing.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !noTTL.Spec().ExpiresAt.IsZero() {
		t.Error("a share with no TTL has an expiry time")
	}
}

func TestParseHTTPTarget(t *testing.T) {
	cases := map[string]string{
		"http://localhost:3000":  "http://localhost:3000",
		"https://localhost:8443": "https://localhost:8443",
		"localhost:3000":         "http://localhost:3000",
		"127.0.0.1:8080":         "http://127.0.0.1:8080",
		":3000":                  "http://127.0.0.1:3000",
		"3000":                   "http://127.0.0.1:3000",
		"localhost:3000/api":     "http://localhost:3000/api",
		"http://localhost:3000/": "http://localhost:3000",
	}
	for input, want := range cases {
		got, ok := sharing.ParseHTTPTarget(input)
		if !ok {
			t.Errorf("ParseHTTPTarget(%q) was rejected", input)
			continue
		}
		if got != want {
			t.Errorf("ParseHTTPTarget(%q) = %q, want %q", input, got, want)
		}
	}

	for _, input := range []string{"", "localhost", "./file.mp4", "notaport:abc", "localhost:99999"} {
		if got, ok := sharing.ParseHTTPTarget(input); ok {
			t.Errorf("ParseHTTPTarget(%q) = %q, want rejection", input, got)
		}
	}
}

func TestClassifyPrefersTheFilesystem(t *testing.T) {
	dir := t.TempDir()

	file := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A file literally called "8080" must be shared as a file, not mistaken
	// for a port.
	portName := filepath.Join(dir, "8080")
	if err := os.WriteFile(portName, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "build")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	t.Run("single file", func(t *testing.T) {
		src, err := sharing.Classify([]string{file})
		if err != nil || src.Kind != sharing.KindFile {
			t.Fatalf("Classify returned kind %v, err %v", src.Kind, err)
		}
		if src.Name != "video.mp4" {
			t.Errorf("Name = %q, want video.mp4", src.Name)
		}
	})

	t.Run("directory", func(t *testing.T) {
		src, err := sharing.Classify([]string{sub})
		if err != nil || src.Kind != sharing.KindDirectory {
			t.Fatalf("Classify returned kind %v, err %v", src.Kind, err)
		}
	})

	t.Run("existing file wins over port shorthand", func(t *testing.T) {
		src, err := sharing.Classify([]string{portName})
		if err != nil {
			t.Fatal(err)
		}
		if src.Kind != sharing.KindFile {
			t.Errorf("a file named 8080 was classified as %v", src.Kind)
		}
	})

	t.Run("http target", func(t *testing.T) {
		src, err := sharing.Classify([]string{"localhost:3000"})
		if err != nil || src.Kind != sharing.KindHTTP {
			t.Fatalf("Classify returned kind %v, err %v", src.Kind, err)
		}
	})

	t.Run("multiple files get unique names", func(t *testing.T) {
		other := filepath.Join(t.TempDir(), "video.mp4")
		if err := os.WriteFile(other, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		src, err := sharing.Classify([]string{file, other})
		if err != nil {
			t.Fatal(err)
		}
		if src.Kind != sharing.KindFiles {
			t.Fatalf("kind = %v, want KindFiles", src.Kind)
		}
		// Both files are called video.mp4, but a visitor addresses them by
		// name, so the names must differ.
		if src.Entries[0].Name == src.Entries[1].Name {
			t.Errorf("both entries are named %q", src.Entries[0].Name)
		}
	})

	t.Run("a directory among several arguments is refused", func(t *testing.T) {
		if _, err := sharing.Classify([]string{file, sub}); err == nil {
			t.Error("mixing a directory into a multi-file share was accepted")
		}
	})
}

func TestReaperStopsWhenTheLastShareExpires(t *testing.T) {
	clock := &fixedClock{now: time.Now()}
	reg := sharing.NewRegistry()
	share := sharing.New(sharing.Spec{ID: "a", Token: "t", ExpiresAt: clock.now.Add(-time.Second)})
	if err := reg.Add(share); err != nil {
		t.Fatal(err)
	}

	reaper := sharing.NewReaper(reg, clock, 5*time.Millisecond)
	expired := make(chan *sharing.Share, 1)
	emptied := make(chan struct{}, 1)
	reaper.OnExpire = func(s *sharing.Share) { expired <- s }
	reaper.OnEmpty = func() { emptied <- struct{}{} }

	ctx, cancel := contextWithTimeout(time.Second)
	defer cancel()
	go reaper.Run(ctx)

	select {
	case got := <-expired:
		if got != share {
			t.Error("the reaper reported a different share")
		}
	case <-ctx.Done():
		t.Fatal("the reaper never expired the share")
	}

	select {
	case <-emptied:
	case <-ctx.Done():
		t.Fatal("the reaper never reported the registry as empty")
	}
}

// Reaching --downloads stops new downloads at once, but the reaper must not
// stop the process while the last permitted one is still being delivered:
// `vrok big.iso --downloads 1` would otherwise cut off the only download it
// allows. An expired TTL is different and ends transfers immediately.
func TestDownloadLimitedShareOutlivesItsLastTransfer(t *testing.T) {
	now := time.Now()
	reg := sharing.NewRegistry()
	share := sharing.New(sharing.Spec{ID: "lim", Token: "t-lim", MaxDownloads: 1})
	reg.Add(share)

	if _, err := share.ClaimDownload(now); err != nil {
		t.Fatal(err)
	}
	share.BeginTransfer()

	if purged := reg.PurgeExpired(now.Add(time.Hour)); len(purged) != 0 {
		t.Fatal("a share was purged while its last permitted download was in flight")
	}

	finished := now.Add(time.Hour)
	share.EndTransfer(finished)
	if purged := reg.PurgeExpired(finished.Add(sharing.DownloadLimitGrace / 2)); len(purged) != 0 {
		t.Fatal("a share was purged inside the grace period, which would break a paused video")
	}
	if purged := reg.PurgeExpired(finished.Add(sharing.DownloadLimitGrace)); len(purged) != 1 {
		t.Fatal("a spent, idle share was not purged after the grace period")
	}

	expiring := sharing.New(sharing.Spec{ID: "ttl", Token: "t-ttl", ExpiresAt: now.Add(time.Minute)})
	reg.Add(expiring)
	expiring.BeginTransfer()
	if purged := reg.PurgeExpired(now.Add(2 * time.Minute)); len(purged) != 1 {
		t.Fatal("an expired share survived because a transfer was in flight; its TTL is a promise")
	}
}
