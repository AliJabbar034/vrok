package cli

import (
	"errors"

	"github.com/AliJabbar034/vrok/internal/control"
	"github.com/AliJabbar034/vrok/internal/ui"
	"github.com/spf13/cobra"
)

func newStopCommand(a *app) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "stop [--all] [id...]",
		Short: "Stop shares, or every share on this machine",
		Long: `Stop ends sharing. With --all it shuts down every vrok process started by
this user; with ids it behaves like ` + "`vrok revoke`" + `.`,
		Example:       "  vrok stop --all\n  vrok stop a82kd9",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			a.load()

			switch {
			case all && len(args) > 0:
				return errors.New("pass either --all or a list of ids, not both")
			case all:
				return stopAll(cmd, a)
			case len(args) > 0:
				return revokeShares(cmd.Context(), a, args)
			default:
				return errors.New("pass --all or at least one share id")
			}
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "stop every vrok process")
	return cmd
}

func stopAll(cmd *cobra.Command, a *app) error {
	sessions, err := control.Sessions()
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		a.printer.Info("No active shares.")
		return nil
	}

	stopped := 0
	for _, session := range sessions {
		// A session that dies while handling the request is a success: the
		// goal was for it to stop.
		if err := control.Dial(session).Stop(cmd.Context()); err != nil {
			a.printer.Warn("could not stop process %d: %v", session.PID, err)
			continue
		}
		stopped++
	}

	a.printer.Success("Stopped %s", ui.Plural(stopped, "vrok process", "vrok processes"))
	return nil
}
