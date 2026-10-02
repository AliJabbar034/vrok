package cli

import (
	"testing"

	"github.com/AliJabbar034/vrok/internal/config"
)

func TestParseWhoChoice(t *testing.T) {
	cases := []struct {
		in   string
		def  int
		want int
		ok   bool
	}{
		{"", 1, 1, true},
		{"1", 2, 1, true},
		{"anyone", 2, 1, true},
		{"2", 1, 2, true},
		{"network", 1, 2, true},
		{"3", 1, 3, true},
		{"machine", 1, 3, true},
		{"nope", 1, 0, false},
	}
	for _, tc := range cases {
		got, ok := parseWhoChoice(tc.in, tc.def)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseWhoChoice(%q, %d) = %d, %v; want %d, %v", tc.in, tc.def, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseTTLChoice(t *testing.T) {
	cases := []struct {
		in   string
		def  string
		want string
		ok   bool
	}{
		{"", "0", "0", true},
		{"1", "2h", "0", true},
		{"2", "0", "2h", true},
		{"3", "0", "30m", true},
		{"4", "0", "1d", true},
		{"45m", "0", "45m", true},
		{"nope", "0", "", false},
	}
	for _, tc := range cases {
		got, ok := parseTTLChoice(tc.in, tc.def)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseTTLChoice(%q, %q) = %q, %v; want %q, %v", tc.in, tc.def, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseDownloadsChoice(t *testing.T) {
	cases := []struct {
		in   string
		def  int
		want int
		ok   bool
	}{
		{"", 0, 0, true},
		{"1", 5, 0, true},
		{"2", 0, 1, true},
		{"3", 0, 5, true},
		{"10", 0, 10, true},
		{"one-time", 0, 1, true},
		{"-3", 0, 0, false},
	}
	for _, tc := range cases {
		got, ok := parseDownloadsChoice(tc.in, tc.def)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseDownloadsChoice(%q, %d) = %d, %v; want %d, %v", tc.in, tc.def, got, ok, tc.want, tc.ok)
		}
	}
}

func TestEquivalentCommandOmitsDefaults(t *testing.T) {
	got := equivalentCommand([]string{"./file"}, shareOptions{ttl: "0"}, config.DefaultTTL)
	if got != "vrok ./file" {
		t.Fatalf("defaults should print a bare command, got %q", got)
	}
}

func TestEquivalentCommandPrintsTheFlagsTheWizardChose(t *testing.T) {
	got := equivalentCommand(
		[]string{"./Wuthering Heights.mkv"},
		shareOptions{local: true, ttl: "30m", downloads: 1},
		config.DefaultTTL,
	)
	want := `vrok "./Wuthering Heights.mkv" --local --ttl 30m --downloads 1`
	if got != want {
		t.Fatalf("equivalentCommand = %q, want %q", got, want)
	}
}

func TestEquivalentCommandPrintsTTLWhenNotDefault(t *testing.T) {
	got := equivalentCommand([]string{"./file"}, shareOptions{ttl: "2h", tunnelName: "local"}, config.DefaultTTL)
	if got != "vrok ./file --tunnel local --ttl 2h" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyWho(t *testing.T) {
	opts := shareOptions{tunnelName: "local"}
	applyWho(&opts, 1)
	if opts.local || opts.tunnelName == "local" {
		t.Fatalf("anyone should clear a loopback-only choice: %+v", opts)
	}

	applyWho(&opts, 2)
	if !opts.local {
		t.Fatal("network should set --local")
	}

	applyWho(&opts, 3)
	if opts.local || opts.tunnelName != "local" {
		t.Fatalf("machine should set --tunnel local: %+v", opts)
	}
}

func TestInteractiveFlagIsOnTheRootCommand(t *testing.T) {
	root := NewRootCommand("test")
	if root.Flags().Lookup("interactive") == nil {
		t.Fatal("root is missing --interactive")
	}
	short := root.Flags().ShorthandLookup("i")
	if short == nil {
		t.Fatal("root is missing -i")
	}
}
