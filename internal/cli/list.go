package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/AliJabbar034/vrok/internal/control"
	"github.com/AliJabbar034/vrok/internal/humanize"
	"github.com/spf13/cobra"
)

func newListCommand(a *app) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls", "ps"},
		Short:   "Show the shares running on this machine",
		Long: `List queries every running vrok process and shows its live shares.

There is no central registry to read: each sharing process reports its own
shares, which is why a share vanishes from this list the moment its process
exits.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.load()

			shares, err := control.AllShares(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(shares)
			}
			printShares(a, shares)
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "print the shares as JSON")
	return cmd
}

func printShares(a *app, shares []control.ShareInfo) {
	if len(shares) == 0 {
		a.printer.Info("No active shares.")
		return
	}

	now := time.Now()
	table := a.printer.NewTable("id", "name", "kind", "expires", "downloads", "traffic", "last access")
	for _, share := range shares {
		table.Row(
			share.ID,
			share.Name,
			share.Kind,
			expiryText(share, now),
			downloadsText(share),
			humanize.Bytes(share.Bytes),
			humanize.ClockTime(share.LastAccess),
		)
	}
	table.Flush()
}

func expiryText(share control.ShareInfo, now time.Time) string {
	if share.ExpiresAt.IsZero() {
		return "never"
	}
	return humanize.Duration(share.ExpiresAt.Sub(now))
}

func downloadsText(share control.ShareInfo) string {
	if share.MaxDownloads > 0 {
		return fmt.Sprintf("%d/%d", share.Downloads, share.MaxDownloads)
	}
	return fmt.Sprintf("%d", share.Downloads)
}
