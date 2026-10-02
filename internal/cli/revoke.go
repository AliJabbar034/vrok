package cli

import (
	"context"
	"fmt"

	"github.com/AliJabbar034/vrok/internal/control"
	"github.com/spf13/cobra"
)

func newRevokeCommand(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <id>...",
		Short: "Stop one or more shares by id",
		Long: `Revoke makes a share URL stop working immediately. Any download already in
progress is cut off, and the id is taken from the ID column of ` + "`vrok list`" + `.

A process left with no shares exits on its own.`,
		Example:       "  vrok revoke a82kd9\n  vrok revoke a82kd9 b18ks2",
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			a.load()
			return revokeShares(cmd.Context(), a, args)
		},
	}
}

// revokeShares asks every session about each id, because the caller has no way
// to know which process owns a share.
func revokeShares(ctx context.Context, a *app, ids []string) error {
	sessions, err := control.Sessions()
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		return fmt.Errorf("no vrok process is running")
	}

	var missing []string
	for _, id := range ids {
		revoked := false
		for _, session := range sessions {
			ok, err := control.Dial(session).Revoke(ctx, id)
			if err != nil {
				continue
			}
			if ok {
				revoked = true
				break
			}
		}
		if revoked {
			a.printer.Success("Revoked %s", a.printer.Bold(id))
		} else {
			missing = append(missing, id)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("no such share: %v", missing)
	}
	return nil
}
