// Package update finds the newest vrok release and, for installs that vrok
// owns, replaces the running binary with it.
//
// It only ever runs because the user typed `vrok update` or `vrok doctor`:
// vrok never checks for updates in the background. An install that belongs to
// a package manager is left to that package manager, because replacing a
// binary Homebrew or Scoop tracks would leave the manager believing an old
// version is installed.
package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Repo is the GitHub repository releases come from.
const Repo = "AliJabbar034/vrok"

// releasesBase is where releases are looked up and downloaded. It is a
// variable so tests can point it at a local server.
var releasesBase = "https://github.com/" + Repo + "/releases"

// ReleaseURL is the page describing a release.
func ReleaseURL(tag string) string { return releasesBase + "/tag/" + tag }

// Method is how this copy of vrok was installed, which decides who updates it.
type Method int

const (
	// MethodSelf is the install script, an archive, or anything unrecognised:
	// vrok replaces its own binary.
	MethodSelf Method = iota
	MethodHomebrew
	MethodScoop
	MethodGo
)

// String names the method the way a user would.
func (m Method) String() string {
	switch m {
	case MethodHomebrew:
		return "Homebrew"
	case MethodScoop:
		return "Scoop"
	case MethodGo:
		return "go install"
	default:
		return "the install script"
	}
}

// Command is what updates an install vrok does not own, or "" when vrok
// updates itself.
func (m Method) Command() string {
	switch m {
	case MethodHomebrew:
		return "brew update && brew upgrade --cask " + "AliJabbar034/tap/vrok"
	case MethodScoop:
		return "scoop update; scoop update vrok"
	case MethodGo:
		return GoInstall
	default:
		return ""
	}
}

// GoInstall builds the newest vrok from source.
const GoInstall = "go install github.com/" + Repo + "/cmd/vrok@latest"

// Executable returns the real path of the running binary, with symlinks
// resolved: Homebrew's /opt/homebrew/bin/vrok is a link into its Caskroom,
// and only the resolved path says who installed it.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("update: locate the running binary: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, nil
}

// Detect works out the install method from the binary's resolved path.
func Detect(exe string) Method {
	// Both separators, explicitly: filepath.ToSlash only converts on Windows,
	// and the method should not depend on which OS reads the path.
	p := strings.ToLower(strings.ReplaceAll(exe, `\`, "/"))
	switch {
	case strings.Contains(p, "/caskroom/"), strings.Contains(p, "/cellar/"),
		strings.Contains(p, "/homebrew/"), strings.Contains(p, "/linuxbrew/"):
		return MethodHomebrew
	case strings.Contains(p, "/scoop/"):
		return MethodScoop
	}
	for _, dir := range goBinDirs() {
		if dir != "" && strings.EqualFold(filepath.Clean(filepath.Dir(exe)), filepath.Clean(dir)) {
			return MethodGo
		}
	}
	return MethodSelf
}

// goBinDirs lists where `go install` puts binaries, without running go.
func goBinDirs() []string {
	dirs := []string{os.Getenv("GOBIN")}
	for _, gopath := range filepath.SplitList(os.Getenv("GOPATH")) {
		dirs = append(dirs, filepath.Join(gopath, "bin"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "go", "bin"))
	}
	return dirs
}

// Latest returns the newest release's tag, such as "v0.5.2".
//
// It reads the redirect from /releases/latest rather than calling the JSON
// API, the same way install.sh does: that avoids the API's per-IP rate limit,
// which shared offices and CI runners hit quickly.
func Latest(ctx context.Context, client *http.Client) (string, error) {
	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, releasesBase+"/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		return "", fmt.Errorf("update: reach GitHub: %w", err)
	}
	resp.Body.Close()

	_, tag, found := strings.Cut(resp.Header.Get("Location"), "/tag/")
	if !found || tag == "" {
		return "", fmt.Errorf("update: GitHub did not name a latest release (HTTP %d)", resp.StatusCode)
	}
	return tag, nil
}

// ErrNotARelease reports a version string that is not a release number, such
// as "dev" from a build made without release flags.
var ErrNotARelease = errors.New("update: not a release version")

// FirstWithUpdate is the first release that has `vrok update`. Installing an
// older one through `vrok update --version` would leave the user with no
// `vrok update` to come back with, so those go through the install script.
const FirstWithUpdate = "0.6.0"

// Tag normalises a version a user typed ("0.5.1" or "v0.5.1") into a release
// tag. It rejects anything that is not a release number, so a typo is caught
// before any download.
func Tag(version string) (string, error) {
	if _, err := Parse(version); err != nil {
		return "", err
	}
	return "v" + strings.TrimPrefix(strings.TrimSpace(version), "v"), nil
}

// Compare orders two release versions: -1, 0 or 1 as a is older than, the
// same as, or newer than b.
func Compare(a, b string) (int, error) {
	x, err := Parse(a)
	if err != nil {
		return 0, err
	}
	y, err := Parse(b)
	if err != nil {
		return 0, err
	}
	for i := range x {
		switch {
		case x[i] < y[i]:
			return -1, nil
		case x[i] > y[i]:
			return 1, nil
		}
	}
	return 0, nil
}

// Parse reads "v0.5.1", "0.5.1" or "0.5.2-next" into its three numbers.
func Parse(version string) ([3]int, error) {
	var out [3]int
	core, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(version), "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("%w: %q", ErrNotARelease, version)
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return out, fmt.Errorf("%w: %q", ErrNotARelease, version)
		}
		out[i] = n
	}
	return out, nil
}

// Newer reports whether latest is a later release than current. Both must
// parse; a caller holding a "dev" build has nothing to compare.
func Newer(latest, current string) (bool, error) {
	order, err := Compare(latest, current)
	return order > 0, err
}

// archiveName is the release asset for this platform, as GoReleaser names it.
func archiveName(tag string) string {
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("vrok_%s_%s_%s.%s", tag, runtime.GOOS, runtime.GOARCH, ext)
}

// binaryName is the CLI's file name inside an archive.
func binaryName() string {
	if runtime.GOOS == "windows" {
		return "vrok.exe"
	}
	return "vrok"
}
