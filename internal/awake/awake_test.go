package awake

import (
	"errors"
	"testing"
	"time"
)

type fakeInhibitor struct {
	holds, releases int
	err             error
}

func (f *fakeInhibitor) hold() (func(), error) {
	if f.err != nil {
		return nil, f.err
	}
	f.holds++
	return func() { f.releases++ }, nil
}

func TestKeeperHoldsWhileBusyAndReleasesAfterGrace(t *testing.T) {
	f := &fakeInhibitor{}
	k := &Keeper{hold: f.hold}
	now := time.Now()

	k.Update(false, now)
	if f.holds != 0 {
		t.Fatal("held the machine awake with nothing transferring")
	}

	k.Update(true, now)
	k.Update(true, now.Add(time.Second))
	if f.holds != 1 || !k.Holding() {
		t.Fatalf("holds = %d, want exactly 1 while busy", f.holds)
	}

	// A short pause between requests must not let the machine sleep.
	k.Update(false, now.Add(Grace/2))
	if f.releases != 0 {
		t.Fatal("released inside the grace period")
	}

	k.Update(false, now.Add(time.Second+Grace))
	if f.releases != 1 || k.Holding() {
		t.Fatalf("releases = %d, want 1 after the grace period", f.releases)
	}
}

func TestKeeperDoesNotRetryAFailingPlatform(t *testing.T) {
	f := &fakeInhibitor{err: errors.New("no")}
	calls := 0
	k := &Keeper{hold: func() (func(), error) { calls++; return f.hold() }}
	now := time.Now()
	for i := range 5 {
		k.Update(true, now.Add(time.Duration(i)*time.Second))
	}
	if calls != 1 {
		t.Fatalf("hold called %d times, want 1", calls)
	}
}

func TestCloseReleases(t *testing.T) {
	f := &fakeInhibitor{}
	k := &Keeper{hold: f.hold}
	k.Update(true, time.Now())
	k.Close()
	k.Close()
	if f.releases != 1 {
		t.Fatalf("releases = %d, want 1", f.releases)
	}
}
