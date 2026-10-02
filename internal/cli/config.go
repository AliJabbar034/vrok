package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/AliJabbar034/vrok/internal/config"
	"github.com/AliJabbar034/vrok/internal/tunnel"
	"github.com/spf13/cobra"
)

func newConfigCommand(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or change stored defaults",
		Long: `Config manages the optional defaults file. vrok needs no configuration;
this exists so that flags you always pass can be stored once.`,
	}
	cmd.AddCommand(newConfigPathCommand(a), newConfigShowCommand(a), newConfigSetCommand(a))
	return cmd
}

func newConfigPathCommand(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the configuration file location",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.Path()
			if err != nil {
				return err
			}
			a.printer.Info("%s", path)
			return nil
		},
	}
}

func newConfigShowCommand(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.load()
			cfg := a.config

			table := a.printer.NewTable("setting", "value")
			table.Row("ttl", cfg.TTL.String())
			table.Row("tunnel", cfg.Tunnel)
			table.Row("addr", cfg.Addr)
			table.Row("qr", strconv.FormatBool(cfg.QR))
			table.Row("domain", orDash(cfg.Domain))
			table.Row("relay_url", orDash(cfg.RelayURL))
			// The token is never printed: the whole point of storing it with
			// restrictive permissions is that it does not end up on a screen
			// or in a terminal scrollback.
			table.Row("relay_token", secretState(cfg.RelayToken))
			return table.Flush()
		},
	}
}

func newConfigSetCommand(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Change one setting",
		Long: `Set writes one key to the configuration file.

Keys: ttl, tunnel, addr, qr, domain, relay_url, relay_token`,
		Example: "  vrok config set ttl 30m\n  vrok config set tunnel cloudflare",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a.load()
			cfg := a.config
			key, value := args[0], args[1]

			if err := applySetting(&cfg, key, value); err != nil {
				return err
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			a.printer.Success("Set %s to %s", a.printer.Bold(key), value)
			return nil
		},
	}
}

// settingKeys are the writable configuration keys, in the order `config show`
// prints them.
var settingKeys = []string{"ttl", "tunnel", "addr", "qr", "domain", "relay_url", "relay_token"}

// applySetting validates and assigns one configuration key. Validation happens
// here rather than at use time so a typo is reported when it is made, not the
// next time a share fails to start.
func applySetting(cfg *config.Config, key, value string) error {
	// The flags spell these `--relay-url`, and so does every message that
	// suggests storing one, so a hyphen is what a user types. Accepting it
	// costs one line and saves them guessing at the separator.
	key = strings.ReplaceAll(key, "-", "_")

	switch key {
	case "ttl":
		parsed, err := config.ParseTTL(value)
		if err != nil {
			return err
		}
		cfg.TTL = config.Duration(parsed)

	case "tunnel":
		if _, err := tunnel.Open(value, tunnel.Config{Label: "validate", RelayURL: "http://localhost"}); err != nil {
			return fmt.Errorf("unknown tunnel provider %q (available: %v)", value, tunnel.Available())
		}
		cfg.Tunnel = value

	case "addr":
		cfg.Addr = value

	case "qr":
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("qr must be true or false")
		}
		cfg.QR = enabled

	case "domain":
		cfg.Domain = value

	case "relay_url":
		cfg.RelayURL = value

	case "relay_token":
		cfg.RelayToken = value

	default:
		return fmt.Errorf("unknown setting %q (available: %s)", key, strings.Join(settingKeys, ", "))
	}
	return nil
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func secretState(value string) string {
	if value == "" {
		return "-"
	}
	return "(set)"
}
