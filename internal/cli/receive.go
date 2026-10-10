package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// newReceiveCommand returns `vrok receive`, which turns the usual direction
// around: the URL opens an upload page, and whatever visitors send is
// written into a folder on this machine.
func newReceiveCommand(a *app) *cobra.Command {
	s := &sharer{app: a, opts: shareOptions{receive: true}}

	cmd := &cobra.Command{
		Use:   "receive [folder]",
		Short: "Let others send files to this computer over a temporary URL",
		Long: `Receive prints a temporary URL that opens an upload page. Files sent from
it are written straight into a folder on this machine, by default
~/Downloads/vrok. Nothing passes through a storage service.

Before anything is written, you see the names and sizes of the files
someone wants to send and press y to accept or n to decline. With --yes
every file is accepted without asking.

A file never replaces one already in the folder: a second "photo.jpg"
arrives as "photo (1).jpg". Large files are sent in pieces and pick up
where they left off if the connection drops.`,
		Example: `  vrok receive
  vrok receive ./inbox
  vrok receive --max-files 3 --ttl 1h
  vrok receive --password
  vrok receive --local
  vrok receive --yes   # accept without asking, e.g. without a terminal`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			a.load()
			if !s.opts.yes && !hasTerminal() {
				return errors.New("vrok receive asks you to accept each batch of files, which needs a terminal; pass --yes to accept every file without asking")
			}
			dir, err := receiveFolder(args)
			if err != nil {
				return err
			}
			return s.run(cmd.Context(), []string{dir})
		},
	}

	f := cmd.Flags()
	f.StringVar(&s.opts.ttl, "ttl", "", "how long the link accepts files, e.g. 30m, 2h, 1d (default: until stopped)")
	f.IntVar(&s.opts.downloads, "max-files", 0, "stop receiving after this many files (0 for unlimited)")
	f.BoolVar(&s.opts.password, "password", false, "ask for a password that senders must enter")
	f.BoolVarP(&s.opts.yes, "yes", "y", false, "accept every file without asking first")
	bindReachFlags(f, &s.opts)
	return cmd
}

// receiveFolder is the folder named on the command line, or
// ~/Downloads/vrok, which keeps received files together and out of the way
// of everything else in Downloads.
func receiveFolder(args []string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads", "vrok"), nil
}

// displayPath shortens a path under the home directory to ~/…, which is how
// people recognise their own folders.
func displayPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	if rel == "." {
		return "~"
	}
	return "~" + string(filepath.Separator) + rel
}
