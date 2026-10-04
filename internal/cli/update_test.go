package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/AliJabbar034/vrok/internal/config"
	"github.com/AliJabbar034/vrok/internal/ui"
)

func testApp() (*app, *bytes.Buffer) {
	var out bytes.Buffer
	return &app{printer: ui.New(&out, &out), config: config.Default()}, &out
}

// Every case here is decided before any download, so none touches the network.
func TestInstallVersionGuards(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		a, _ := testApp()
		err := runInstallVersion(context.Background(), a, "0.6.0", "latest")
		if err == nil || !strings.Contains(err.Error(), "v0.6.0") {
			t.Fatalf("err = %v, want a hint showing the expected form", err)
		}
	})

	t.Run("older than vrok update itself", func(t *testing.T) {
		a, _ := testApp()
		err := runInstallVersion(context.Background(), a, "0.6.0", "v0.5.1")
		if err == nil {
			t.Fatal("a release without `vrok update` was installed by it")
		}
		// The way out has to be in the message, not just the refusal.
		if !strings.Contains(err.Error(), "VROK_VERSION=v0.5.1") {
			t.Fatalf("err does not offer the install script:\n%v", err)
		}
	})

	t.Run("already installed", func(t *testing.T) {
		a, out := testApp()
		if err := runInstallVersion(context.Background(), a, "0.6.0", "0.6.0"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "already installed") {
			t.Fatalf("output = %q", out.String())
		}
	})
}

func TestReleaseVersionStripsBuildInfo(t *testing.T) {
	if got := releaseVersion(BuildInfo("0.6.0", "abc123", "today")); got != "0.6.0" {
		t.Fatalf("releaseVersion = %q, want 0.6.0", got)
	}
}
