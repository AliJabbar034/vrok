package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AliJabbar034/vrok/internal/config"
	"github.com/AliJabbar034/vrok/internal/ui"
	"github.com/AliJabbar034/vrok/internal/update"
)

func TestNoticeStaysOutOfScriptsAndOptOuts(t *testing.T) {
	off := false
	env := func(vars map[string]string) func(string) string {
		return func(key string) string { return vars[key] }
	}
	cases := []struct {
		name     string
		getenv   func(string) string
		terminal bool
		command  string
		current  string
		cfg      config.Config
		want     bool
	}{
		{"share at a terminal", env(nil), true, "vrok", "0.6.1", config.Config{}, true},
		{"piped or scripted", env(nil), false, "vrok", "0.6.1", config.Config{}, false},
		{"CI", env(map[string]string{"CI": "true"}), true, "vrok", "0.6.1", config.Config{}, false},
		{"env opt-out", env(map[string]string{"VROK_NO_UPDATE_NOTIFIER": "1"}), true, "vrok", "0.6.1", config.Config{}, false},
		{"config opt-out", env(nil), true, "vrok", "0.6.1", config.Config{UpdateCheck: &off}, false},
		{"vrok update", env(nil), true, "update", "0.6.1", config.Config{}, false},
		{"vrok doctor", env(nil), true, "doctor", "0.6.1", config.Config{}, false},
		{"shell completion", env(nil), true, "__complete", "0.6.1", config.Config{}, false},
		{"dev build", env(nil), true, "vrok", "dev", config.Config{}, false},
	}
	for _, tc := range cases {
		if got := noticeAllowed(tc.getenv, tc.terminal, tc.command, tc.current, tc.cfg); got != tc.want {
			t.Errorf("%s: noticeAllowed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func stubLatest(t *testing.T, tag string, err error) *int {
	t.Helper()
	calls := 0
	old := latestRelease
	latestRelease = func(context.Context, *http.Client) (string, error) {
		calls++
		return tag, err
	}
	t.Cleanup(func() { latestRelease = old })
	return &calls
}

func runNotifier(path string, now time.Time) string {
	var errOut bytes.Buffer
	p := ui.New(&bytes.Buffer{}, &errOut)
	p.SetColor(false)
	n := &updateNotifier{current: "0.6.1", now: func() time.Time { return now }}
	n.begin(path)
	n.finish(p)
	return errOut.String()
}

// The first run checks and shows the notice; the next run the same day
// neither asks GitHub again nor repeats itself; a day later it reminds.
func TestNoticeChecksAndRemindsOnceADay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	calls := stubLatest(t, "v0.7.0", nil)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	if got := runNotifier(path, now); !strings.Contains(got, "0.6.1 → 0.7.0") {
		t.Fatalf("first run printed no notice:\n%s", got)
	}
	if got := runNotifier(path, now.Add(time.Hour)); got != "" {
		t.Errorf("second run within the day printed again:\n%s", got)
	}
	if *calls != 1 {
		t.Errorf("GitHub was asked %d times within a day, want 1", *calls)
	}
	if got := runNotifier(path, now.Add(25*time.Hour)); !strings.Contains(got, "0.7.0") {
		t.Errorf("no reminder a day later:\n%s", got)
	}
}

func TestNoticeIsSilentWhenOfflineOrCurrent(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	path := filepath.Join(t.TempDir(), "update-check.json")
	stubLatest(t, "", errors.New("no network"))
	if got := runNotifier(path, now); got != "" {
		t.Errorf("offline check printed:\n%s", got)
	}
	if state := update.LoadState(path); !state.CheckedAt.Equal(now) {
		t.Errorf("a failed check was not recorded, so it would retry on every command: %+v", state)
	}

	path = filepath.Join(t.TempDir(), "update-check.json")
	stubLatest(t, "v0.6.1", nil)
	if got := runNotifier(path, now); got != "" {
		t.Errorf("up-to-date install printed:\n%s", got)
	}
}

func waitForCheck(t *testing.T, n *updateNotifier) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(n.result) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("background check never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A share that shows the notice in its banner must not repeat it in the box
// when it stops.
func TestNoticeShownInBannerIsNotRepeated(t *testing.T) {
	stubLatest(t, "v0.7.0", nil)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	n := &updateNotifier{current: "0.6.1", now: func() time.Time { return now }}
	n.begin(filepath.Join(t.TempDir(), "update-check.json"))
	waitForCheck(t, n)

	notice, ok := n.claim()
	if !ok || notice.Latest != "0.7.0" || notice.Current != "0.6.1" {
		t.Fatalf("claim = %+v, %v; want the 0.7.0 notice", notice, ok)
	}
	if _, again := n.claim(); again {
		t.Error("a second claim in the same run returned the notice again")
	}

	var errOut bytes.Buffer
	p := ui.New(&bytes.Buffer{}, &errOut)
	n.finish(p)
	if errOut.Len() != 0 {
		t.Errorf("box repeated a notice the banner already showed:\n%s", errOut.String())
	}
}

// A share whose banner printed before the check finished still hears about
// the release, once, when it stops.
func TestLateCheckFallsBackToTheBox(t *testing.T) {
	release := make(chan struct{})
	old := latestRelease
	latestRelease = func(context.Context, *http.Client) (string, error) {
		<-release
		return "v0.7.0", nil
	}
	t.Cleanup(func() { latestRelease = old })

	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	n := &updateNotifier{current: "0.6.1", now: func() time.Time { return now }}
	n.begin(filepath.Join(t.TempDir(), "update-check.json"))

	if _, ok := n.claim(); ok {
		t.Fatal("claim returned a notice before the check finished")
	}
	close(release)

	var errOut bytes.Buffer
	p := ui.New(&bytes.Buffer{}, &errOut)
	p.SetColor(false)
	n.finish(p)
	if strings.Count(errOut.String(), "0.6.1 → 0.7.0") != 1 {
		t.Errorf("want the notice exactly once after the share:\n%s", errOut.String())
	}
}

func TestClaimWithoutANotifierIsSafe(t *testing.T) {
	var n *updateNotifier
	if _, ok := n.claim(); ok {
		t.Error("a nil notifier returned a notice")
	}
}
