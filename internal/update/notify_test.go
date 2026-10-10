package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateRoundTripsAndToleratesDamage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vrok", "update-check.json")
	if got := LoadState(path); got != (State{}) {
		t.Fatalf("missing file gave %+v, want the zero State", got)
	}

	want := State{Latest: "v0.7.0", CheckedAt: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)}
	if err := SaveState(path, want); err != nil {
		t.Fatal(err)
	}
	if got := LoadState(path); !got.CheckedAt.Equal(want.CheckedAt) || got.Latest != want.Latest {
		t.Errorf("round trip gave %+v, want %+v", got, want)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadState(path); got != (State{}) {
		t.Errorf("damaged file gave %+v, want the zero State", got)
	}
}

func TestStaleAfterADayOrWhenTheClockMovesBack(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		checked time.Time
		want    bool
	}{
		{"never checked", time.Time{}, true},
		{"an hour ago", now.Add(-time.Hour), false},
		{"a day ago", now.Add(-CheckInterval), true},
		{"in the future", now.Add(time.Hour), true},
	}
	for _, tc := range cases {
		if got := (State{CheckedAt: tc.checked}).Stale(now); got != tc.want {
			t.Errorf("%s: Stale = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPendingAnnouncesANewerReleaseOnceADay(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		state   State
		current string
		want    string
	}{
		{"nothing known", State{}, "0.6.1", ""},
		{"newer release", State{Latest: "v0.7.0"}, "0.6.1", "v0.7.0"},
		{"already current", State{Latest: "v0.6.1"}, "0.6.1", ""},
		{"running a newer build", State{Latest: "v0.6.1"}, "0.7.0", ""},
		{"dev build", State{Latest: "v0.7.0"}, "dev", ""},
		{"shown an hour ago", State{Latest: "v0.7.0", NotifiedFor: "v0.7.0", NotifiedAt: now.Add(-time.Hour)}, "0.6.1", ""},
		{"shown yesterday", State{Latest: "v0.7.0", NotifiedFor: "v0.7.0", NotifiedAt: now.Add(-RemindInterval)}, "0.6.1", "v0.7.0"},
		{"an even newer release", State{Latest: "v0.8.0", NotifiedFor: "v0.7.0", NotifiedAt: now.Add(-time.Hour)}, "0.6.1", "v0.8.0"},
	}
	for _, tc := range cases {
		if got := tc.state.Pending(tc.current, now); got != tc.want {
			t.Errorf("%s: Pending = %q, want %q", tc.name, got, tc.want)
		}
	}
}
