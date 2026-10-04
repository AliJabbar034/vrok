package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/AliJabbar034/vrok/internal/config"
	"github.com/AliJabbar034/vrok/internal/control"
	"github.com/AliJabbar034/vrok/internal/tunnel"
	"github.com/AliJabbar034/vrok/internal/ui"
	"github.com/AliJabbar034/vrok/internal/update"
	"github.com/spf13/cobra"
)

// probeTimeout bounds each network check. Doctor is run by someone already
// having trouble; it must answer, not hang on the network it is diagnosing.
const probeTimeout = 6 * time.Second

// cloudflareProbe is the service quick tunnels are requested from. Any HTTP
// answer proves it is reachable; only a network failure counts against it.
// It and latestRelease are variables so tests run without the internet.
var cloudflareProbe = "https://api.trycloudflare.com"

var latestRelease = update.Latest

func newDoctorCommand(a *app, version string) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check this machine for common problems",
		Long: `Doctor checks the things that most often stop vrok from working: an
old or duplicate install, a broken config file, the tunnel provider, and
whether Cloudflare can be reached from this network.

It changes nothing. Paste its output into a bug report.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.load()
			if runDoctor(cmd.Context(), a.printer, releaseVersion(version)) {
				return errors.New("doctor found a problem; see the ✗ lines above")
			}
			return nil
		},
	}
}

// runDoctor prints every check and reports whether any failed. Warnings are
// things worth knowing that do not stop vrok from working.
func runDoctor(ctx context.Context, p *ui.Printer, current string) (failed bool) {
	// The two network checks run while the local ones print, so an offline
	// machine waits for one timeout, not two.
	latestCh := make(chan probe[string], 1)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		tag, err := latestRelease(ctx, networkClient)
		latestCh <- probe[string]{tag, err}
	}()
	cloudflareCh := make(chan probe[time.Duration], 1)
	go func() { cloudflareCh <- reach(ctx, cloudflareProbe) }()

	check := func(s ui.Status, label, value string) {
		if s == ui.StatusFail {
			failed = true
		}
		p.Check(s, label, value)
	}

	p.Info("%s %s on %s/%s", p.Bold("vrok"), current, runtime.GOOS, runtime.GOARCH)
	p.Blank()

	// Where this binary is and who manages it.
	exe, err := update.Executable()
	method := update.MethodSelf
	if err != nil {
		check(ui.StatusWarn, "Install", "could not locate the running binary: "+err.Error())
	} else {
		method = update.Detect(exe)
		check(ui.StatusOK, "Install", fmt.Sprintf("%s (%s)", exe, method))
	}

	// Two vroks on PATH is the usual reason an update "did not work".
	copies := onPath(binaryFileName())
	switch {
	case len(copies) == 0:
		check(ui.StatusWarn, "PATH", "vrok is not on PATH; a new terminal will not find it")
	case len(copies) > 1:
		check(ui.StatusWarn, "PATH", fmt.Sprintf("%d copies of vrok; the first one runs:", len(copies)))
		for _, c := range copies {
			p.Hint("%s", c)
		}
		p.Hint("Delete the ones you do not use.")
	case exe != "" && !samePath(copies[0], exe):
		check(ui.StatusWarn, "PATH", "a new terminal runs "+copies[0]+", not this binary")
	default:
		check(ui.StatusOK, "PATH", "one copy of vrok")
	}

	// A broken config is ignored at share time with a warning that is easy
	// to miss; here it is named.
	cfgPath, _ := config.Path()
	if _, err := config.Load(); err != nil {
		check(ui.StatusFail, "Config", err.Error())
		p.Hint("Fix the file, or delete it to go back to the defaults.")
	} else if _, statErr := os.Stat(cfgPath); statErr == nil {
		check(ui.StatusOK, "Config", cfgPath)
	} else {
		check(ui.StatusOK, "Config", "defaults (no config file)")
	}

	if path, fetched := tunnel.Cloudflared(); path == "" {
		check(ui.StatusOK, "cloudflared", "not installed; the first public share downloads it (~40 MB)")
	} else if fetched {
		check(ui.StatusOK, "cloudflared", path+" (fetched by vrok)")
	} else {
		check(ui.StatusOK, "cloudflared", path)
	}

	// --local listens on a fixed port and does not fall back.
	if ln, err := net.Listen("tcp", ":"+strconv.Itoa(defaultLocalPort)); err != nil {
		check(ui.StatusWarn, "Port "+strconv.Itoa(defaultLocalPort),
			"in use; `vrok --local` needs it free, or pass --port")
	} else {
		ln.Close()
		check(ui.StatusOK, "Port "+strconv.Itoa(defaultLocalPort), "free for --local")
	}

	if sessions, err := control.Sessions(); err == nil {
		if len(sessions) == 0 {
			check(ui.StatusOK, "Shares", "none running")
		} else {
			check(ui.StatusOK, "Shares", fmt.Sprintf("%d running; see `vrok list`", len(sessions)))
		}
	}

	// Network results, now that the local checks have had time to cover the
	// wait.
	if cf := <-cloudflareCh; cf.err != nil {
		check(ui.StatusFail, "Cloudflare", "unreachable: "+cf.err.Error())
		p.Hint("Public links need it. On a locked-down network, share with --local instead.")
	} else {
		check(ui.StatusOK, "Cloudflare", fmt.Sprintf("reachable (%d ms)", cf.value.Milliseconds()))
	}

	latest := <-latestCh
	switch {
	case latest.err != nil:
		check(ui.StatusWarn, "Version", "could not check for a newer release: "+latest.err.Error())
	default:
		newer, err := update.Newer(latest.value, current)
		switch {
		case errors.Is(err, update.ErrNotARelease):
			check(ui.StatusOK, "Version", "built from source; newest release is "+latest.value)
		case err != nil:
			check(ui.StatusWarn, "Version", err.Error())
		case newer:
			check(ui.StatusWarn, "Version", strings.TrimPrefix(latest.value, "v")+" is available")
			if cmd := method.Command(); cmd != "" {
				p.Hint("Update with: %s", cmd)
			} else {
				p.Hint("Update with: vrok update")
			}
		default:
			check(ui.StatusOK, "Version", "newest release")
		}
	}

	p.Blank()
	p.Info("Something still wrong? Include this output in a report:")
	p.Info("  %s", p.Link("https://github.com/"+update.Repo+"/issues/new?template=bug_report.yml"))
	return failed
}

type probe[T any] struct {
	value T
	err   error
}

// reach times one HTTPS request. Any status counts as reachable: the point is
// whether this network lets the connection through.
func reach(ctx context.Context, url string) probe[time.Duration] {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return probe[time.Duration]{err: err}
	}
	start := time.Now()
	resp, err := networkClient.Do(req)
	if err != nil {
		return probe[time.Duration]{err: errors.Unwrap(err)}
	}
	resp.Body.Close()
	return probe[time.Duration]{value: time.Since(start)}
}

func binaryFileName() string {
	if runtime.GOOS == "windows" {
		return "vrok.exe"
	}
	return "vrok"
}

// onPath lists every file named name in PATH order, as a shell would search.
func onPath(name string) []string {
	var found []string
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		real := candidate
		if r, err := filepath.EvalSymlinks(candidate); err == nil {
			real = r
		}
		// The same file reached twice (a symlinked dir, a repeated PATH
		// entry) is one install, not two.
		if seen[real] {
			continue
		}
		seen[real] = true
		found = append(found, candidate)
	}
	return found
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return ra == rb
}
