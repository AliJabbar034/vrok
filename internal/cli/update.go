package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/AliJabbar034/vrok/internal/control"
	"github.com/AliJabbar034/vrok/internal/update"
	"github.com/spf13/cobra"
)

// networkClient is used for the GitHub requests update and doctor make. The
// timeout covers a whole archive download on a slow link.
var networkClient = &http.Client{Timeout: 2 * time.Minute}

func newUpdateCommand(a *app, version string) *cobra.Command {
	var (
		check bool
		want  string
	)

	cmd := &cobra.Command{
		Use:   "update [--version vX.Y.Z]",
		Short: "Update vrok to the newest release, or install a chosen one",
		Long: `Update replaces this vrok with the newest release.

With --version it installs that release instead, older or newer. Use it to go
back to an earlier release if a new one breaks something, and run plain
` + "`vrok update`" + ` later to return to the newest.

If vrok was installed with Homebrew, Scoop or go install, it prints the
command for that tool instead, so the package manager stays in charge of
what it installed.

This is the only time vrok checks for a new version: it never does so in the
background.`,
		Example:       "  vrok update\n  vrok update --check\n  vrok update --version v0.6.0",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.load()
			current := releaseVersion(version)
			if want != "" {
				if check {
					return errors.New("--check and --version cannot be used together")
				}
				return runInstallVersion(cmd.Context(), a, current, want)
			}
			return runUpdate(cmd.Context(), a, current, check)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether a newer release exists")
	cmd.Flags().StringVar(&want, "version", "", "install this release instead of the newest, e.g. v0.6.0")
	return cmd
}

// releaseVersion takes the bare version from BuildInfo's
// "0.5.1 (abc123, built …, darwin/arm64)".
func releaseVersion(info string) string {
	if fields := strings.Fields(info); len(fields) > 0 {
		return fields[0]
	}
	return info
}

func runUpdate(ctx context.Context, a *app, current string, check bool) error {
	exe, err := update.Executable()
	if err != nil {
		return err
	}
	update.RemoveLeftover(exe)
	method := update.Detect(exe)

	a.printer.Step("Checking for a newer release")
	latest, err := update.Latest(ctx, networkClient)
	if err != nil {
		return fmt.Errorf("%w\n\nCheck your connection, or see https://github.com/%s/releases", err, update.Repo)
	}
	latestVersion := strings.TrimPrefix(latest, "v")

	newer, err := update.Newer(latest, current)
	if errors.Is(err, update.ErrNotARelease) {
		a.printer.Info("This vrok was built from source (version %q), so it cannot compare itself with %s.", current, latest)
		a.printer.Info("Get the newest code with:")
		a.printer.Info("    %s", update.GoInstall)
		return nil
	}
	if err != nil {
		return err
	}
	if !newer {
		a.printer.Success("vrok %s is the newest release.", current)
		return nil
	}

	a.printer.Info("vrok %s is available. You have %s.", a.printer.Bold(latestVersion), current)
	a.printer.Detail("Changes", a.printer.Link(update.ReleaseURL(latest)))
	if check {
		if method.Command() == "" {
			a.printer.Info("Run %s to install it.", a.printer.Bold("vrok update"))
		} else {
			a.printer.Info("Install it with:  %s", method.Command())
		}
		return nil
	}

	if command := method.Command(); command != "" {
		a.printer.Blank()
		a.printer.Info("This vrok was installed with %s, so update it there:", method)
		a.printer.Info("    %s", command)
		return nil
	}

	if err := install(ctx, a, exe, latest); err != nil {
		return err
	}
	a.printer.Success("Updated vrok %s → %s", current, latestVersion)
	runningNote(a)
	return nil
}

// runInstallVersion installs one chosen release, older or newer than this
// one. It is how a user steps back from a release that broke something.
func runInstallVersion(ctx context.Context, a *app, current, want string) error {
	tag, err := update.Tag(want)
	if err != nil {
		return fmt.Errorf("%q is not a release version; write it like v0.6.0", want)
	}
	target := strings.TrimPrefix(tag, "v")

	if order, _ := update.Compare(target, update.FirstWithUpdate); order < 0 {
		return fmt.Errorf("%s is older than `vrok update` itself (added in %s), so installing it this way would\n"+
			"leave you without `vrok update` to come back with. Use the install script instead:\n\n    %s",
			tag, update.FirstWithUpdate, scriptCommand(tag))
	}
	if order, err := update.Compare(target, current); err == nil && order == 0 {
		a.printer.Success("vrok %s is already installed.", current)
		return nil
	}

	exe, err := update.Executable()
	if err != nil {
		return err
	}
	update.RemoveLeftover(exe)

	switch method := update.Detect(exe); method {
	case update.MethodSelf:
	case update.MethodGo:
		a.printer.Info("This vrok was installed with go install. Install %s with:", tag)
		a.printer.Info("    go install github.com/%s/cmd/vrok@%s", update.Repo, tag)
		return nil
	default:
		a.printer.Info("%s only installs the newest release. To run %s, use the install script:", method, tag)
		a.printer.Info("    %s", scriptCommand(tag))
		a.printer.Info("Then remove the %s copy, or the first one on your PATH keeps running.", method)
		return nil
	}

	if err := install(ctx, a, exe, tag); err != nil {
		if errors.Is(err, update.ErrNoSuchRelease) {
			return fmt.Errorf("there is no release %s. The list is at %s", tag, "https://github.com/"+update.Repo+"/releases")
		}
		return err
	}

	if order, err := update.Compare(target, current); err == nil && order < 0 {
		a.printer.Success("Rolled back to vrok %s (was %s)", target, current)
		a.printer.Info("Run %s to return to the newest release.", a.printer.Bold("vrok update"))
	} else {
		a.printer.Success("Installed vrok %s (was %s)", target, current)
	}
	runningNote(a)
	return nil
}

// install downloads, verifies and swaps in release tag.
func install(ctx context.Context, a *app, exe, tag string) error {
	a.printer.Step("Downloading %s for %s/%s", tag, runtime.GOOS, runtime.GOARCH)
	if err := update.Apply(ctx, networkClient, tag, exe); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%w\n\n%s", err, permissionHint(filepath.Dir(exe)))
		}
		return err
	}
	return nil
}

func runningNote(a *app) {
	if sessions, _ := control.Sessions(); len(sessions) > 0 {
		a.printer.Info("Shares already running keep the old version until you start them again.")
	}
}

// scriptCommand installs one release with the platform's install script.
func scriptCommand(tag string) string {
	if runtime.GOOS == "windows" {
		return "$env:VROK_VERSION='" + tag + "'; irm https://raw.githubusercontent.com/" + update.Repo + "/main/install.ps1 | iex"
	}
	return "curl -fsSL https://raw.githubusercontent.com/" + update.Repo + "/main/install.sh | VROK_VERSION=" + tag + " sh"
}

func permissionHint(dir string) string {
	if runtime.GOOS == "windows" {
		return "You cannot write to " + dir + ". Run the update from a terminal opened as administrator."
	}
	return "You cannot write to " + dir + ". Run:\n    sudo vrok update"
}
