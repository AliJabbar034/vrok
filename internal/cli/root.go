package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"
)

// NewRootCommand builds the vrok command tree.
//
// `vrok share ./file` and `vrok ./file` are the same command: the root accepts
// the share flags and arguments directly, because the shortest useful thing a
// user can type should be the thing that works.
func NewRootCommand(version string) *cobra.Command {
	a := newApp()
	share, runner := newShareCommand(a)

	root := &cobra.Command{
		Use:   "vrok [flags] <path>... | <localhost:port>",
		Short: "Share local files, folders and dev servers over temporary URLs",
		Long: `vrok exposes local files, directories and development servers through
temporary URLs.

	vrok ./video.mp4
	vrok ./playwright-report
	vrok localhost:3000
	vrok -i ./video.mp4

Nothing is uploaded and nothing is stored: requests are served from this
machine while the command runs. Stop the process and the URL stops working.`,
		Version:       version,
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          share.RunE,
		// Flags must be parsed before a subcommand is matched so that
		// `vrok --ttl 1h ./file` works as well as `vrok ./file --ttl 1h`.
		TraverseChildren: true,
	}

	root.PersistentFlags().BoolVarP(&a.verbose, "verbose", "v", false, "print request and tunnel diagnostics")
	root.PersistentFlags().BoolVar(&a.noColor, "no-color", false, "disable coloured output")

	// The root declares the share flags again, bound to the same options
	// struct, so both spellings of the command behave identically.
	bindShareFlags(root, &runner.opts)
	root.AddCommand(share, newListCommand(a), newRevokeCommand(a), newStopCommand(a), newConfigCommand(a),
		newUpdateCommand(a, version), newDoctorCommand(a, version))
	return root
}

// Execute runs vrok and returns the process exit code.
//
// Signal handling lives here so that every command, not just share, stops
// promptly on Ctrl+C.
func Execute(version string) int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	root := NewRootCommand(version)
	if err := root.ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "vrok: %v\n", err)
		return 1
	}
	return 0
}

// BuildInfo returns a version string for --version.
func BuildInfo(version, commit, date string) string {
	return fmt.Sprintf("%s (%s, built %s, %s/%s)", version, commit, date, runtime.GOOS, runtime.GOARCH)
}
