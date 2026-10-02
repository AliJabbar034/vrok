package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AliJabbar034/vrok/internal/config"
	"golang.org/x/term"
)

// hasTerminal reports whether both stdin and stdout are attached to a real
// terminal. Scripts and CI fail this, and that is how the hotkey bar and
// `vrok -i` know to stay out of the way.
func hasTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// promptInteractive asks who can open the share, how long it lasts, and the
// download limit. Sensible answers are preselected so Enter is enough.
// Afterwards it prints the equivalent flags, so the next share is one command.
func (s *sharer) promptInteractive(args []string) error {
	if !hasTerminal() {
		return fmt.Errorf("vrok -i needs a terminal")
	}

	out := s.app.printer.Out()
	in := bufio.NewReader(os.Stdin)

	who, err := askWho(out, in, defaultWho(s.opts))
	if err != nil {
		return err
	}
	applyWho(&s.opts, who)

	ttl, err := askTTL(out, in, defaultTTLChoice(s.opts, time.Duration(s.app.config.TTL)))
	if err != nil {
		return err
	}
	s.opts.ttl = ttl

	downloads, err := askDownloads(out, in, s.opts.downloads)
	if err != nil {
		return err
	}
	s.opts.downloads = downloads

	s.app.printer.Blank()
	s.app.printer.Info("Next time:")
	s.app.printer.Info("  %s", equivalentCommand(args, s.opts, time.Duration(s.app.config.TTL)))
	s.app.printer.Blank()
	return nil
}

func defaultWho(opts shareOptions) int {
	if opts.local {
		return 2
	}
	if opts.tunnelName == "local" {
		return 3
	}
	return 1
}

func applyWho(opts *shareOptions, who int) {
	switch who {
	case 2:
		opts.local = true
		if opts.tunnelName == "local" {
			opts.tunnelName = ""
		}
	case 3:
		opts.local = false
		opts.tunnelName = "local"
	default:
		opts.local = false
		if opts.tunnelName == "local" {
			opts.tunnelName = ""
		}
	}
}

func defaultTTLChoice(opts shareOptions, configured time.Duration) string {
	if opts.ttl != "" {
		return canonicalTTLFlag(opts.ttl)
	}
	if configured <= 0 {
		return "0"
	}
	return config.FormatTTL(configured)
}

func askWho(out io.Writer, in *bufio.Reader, def int) (int, error) {
	fmt.Fprintln(out, "Who can open it?")
	fmt.Fprintln(out, "  1) anyone with the link")
	fmt.Fprintln(out, "  2) your local network")
	fmt.Fprintln(out, "  3) this machine only")
	for {
		line, err := promptChoice(out, in, def)
		if err != nil {
			return 0, err
		}
		n, ok := parseWhoChoice(line, def)
		if ok {
			return n, nil
		}
		fmt.Fprintln(out, "  pick 1, 2 or 3")
	}
}

func askTTL(out io.Writer, in *bufio.Reader, def string) (string, error) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "How long should it last?")
	fmt.Fprintln(out, "  1) until I stop it")
	fmt.Fprintln(out, "  2) 2 hours")
	fmt.Fprintln(out, "  3) 30 minutes")
	fmt.Fprintln(out, "  4) 1 day")
	defNum := ttlDefaultNumber(def)
	if defNum == 0 {
		fmt.Fprintf(out, "  Enter keeps %s\n", def)
	}
	for {
		line, err := promptChoice(out, in, defNum)
		if err != nil {
			return "", err
		}
		got, ok := parseTTLChoice(line, def)
		if ok {
			return got, nil
		}
		fmt.Fprintln(out, "  pick 1-4, or a duration like 45m")
	}
}

func askDownloads(out io.Writer, in *bufio.Reader, def int) (int, error) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Download limit?")
	fmt.Fprintln(out, "  1) unlimited")
	fmt.Fprintln(out, "  2) 1 (one-time)")
	fmt.Fprintln(out, "  3) 5")
	defNum := downloadsDefaultNumber(def)
	if defNum == 0 && def > 0 {
		fmt.Fprintf(out, "  Enter keeps %d\n", def)
	}
	for {
		line, err := promptChoice(out, in, defNum)
		if err != nil {
			return 0, err
		}
		n, ok := parseDownloadsChoice(line, def)
		if ok {
			return n, nil
		}
		fmt.Fprintln(out, "  pick 1-3, or a number")
	}
}

func promptChoice(out io.Writer, in *bufio.Reader, def int) (string, error) {
	if def > 0 {
		fmt.Fprintf(out, "[%d] ", def)
	} else {
		fmt.Fprint(out, "> ")
	}
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func parseWhoChoice(text string, def int) (int, bool) {
	text = strings.TrimSpace(strings.ToLower(text))
	if text == "" {
		return def, def >= 1 && def <= 3
	}
	switch text {
	case "1", "anyone", "a", "link", "public":
		return 1, true
	case "2", "network", "lan", "net":
		return 2, true
	case "3", "machine", "me", "loopback":
		return 3, true
	}
	return 0, false
}

func parseTTLChoice(text, def string) (string, bool) {
	text = strings.TrimSpace(strings.ToLower(text))
	if text == "" {
		return def, def != ""
	}
	switch text {
	case "1", "0", "never", "none", "stop", "until stopped":
		return "0", true
	case "2", "2h", "2 hours", "2 hour":
		return "2h", true
	case "3", "30m", "30 minutes", "30 minute":
		return "30m", true
	case "4", "1d", "1 day":
		return "1d", true
	}
	if _, err := config.ParseTTL(text); err == nil {
		return canonicalTTLFlag(text), true
	}
	return "", false
}

func parseDownloadsChoice(text string, def int) (int, bool) {
	text = strings.TrimSpace(strings.ToLower(text))
	if text == "" {
		if def < 0 {
			return 0, false
		}
		return def, true
	}
	switch text {
	case "1", "unlimited", "none", "off":
		return 0, true
	case "2", "one", "one-time", "onetime":
		return 1, true
	case "3":
		return 5, true
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func ttlDefaultNumber(ttl string) int {
	switch canonicalTTLFlag(ttl) {
	case "0", "never":
		return 1
	case "2h":
		return 2
	case "30m":
		return 3
	case "1d":
		return 4
	default:
		return 0
	}
}

func downloadsDefaultNumber(n int) int {
	switch n {
	case 0:
		return 1
	case 1:
		return 2
	case 5:
		return 3
	default:
		return 0
	}
}

// equivalentCommand is the one-liner printed after -i, so the next share
// skips the questions.
func equivalentCommand(args []string, opts shareOptions, defaultTTL time.Duration) string {
	parts := []string{"vrok"}
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	if opts.local {
		parts = append(parts, "--local")
	} else if opts.tunnelName != "" && opts.tunnelName != config.DefaultTunnel {
		parts = append(parts, "--tunnel", opts.tunnelName)
	}
	if opts.ttl != "" {
		parsed, err := config.ParseTTL(opts.ttl)
		if err == nil && parsed != defaultTTL {
			flag := opts.ttl
			if parsed == 0 {
				flag = "0"
			} else {
				flag = config.FormatTTL(parsed)
			}
			parts = append(parts, "--ttl", flag)
		}
	}
	if opts.downloads > 0 {
		parts = append(parts, "--downloads", strconv.Itoa(opts.downloads))
	}
	if opts.password {
		parts = append(parts, "--password")
	}
	if opts.qr {
		parts = append(parts, "--qr")
	}
	if opts.name != "" {
		parts = append(parts, "--name", shellQuote(opts.name))
	}
	if opts.port != 0 {
		parts = append(parts, "--port", strconv.Itoa(opts.port))
	}
	if opts.listenAddr != "" {
		parts = append(parts, "--listen", opts.listenAddr)
	}
	if opts.relayURL != "" {
		parts = append(parts, "--relay-url", opts.relayURL)
	}
	if opts.relayToken != "" {
		parts = append(parts, "--relay-token", opts.relayToken)
	}
	if opts.domain != "" {
		parts = append(parts, "--domain", opts.domain)
	}
	return strings.Join(parts, " ")
}

func canonicalTTLFlag(text string) string {
	parsed, err := config.ParseTTL(text)
	if err != nil {
		return text
	}
	if parsed == 0 {
		return "0"
	}
	return config.FormatTTL(parsed)
}

func shellQuote(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\n'\"\\") {
		return s
	}
	return strconv.Quote(s)
}
