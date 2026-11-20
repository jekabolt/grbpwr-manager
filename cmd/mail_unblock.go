package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// mail-unblock takes one address off the platform letter block list (platform_mail_suppression,
// lane X6), so confirm and invite letters reach it again. Entries also expire by themselves
// (PLATFORM_MAIL_SUPPRESS_BOUNCE_DAYS / _COMPLAINT_DAYS); this is the way out before that, for an
// operator who checked that the address is real. One address at a time; no HTTP route on purpose.
func init() {
	var email string
	cmd := &cobra.Command{
		Use:   "mail-unblock",
		Short: "Take one address off the platform letter block list",
		RunE: func(cmd *cobra.Command, _ []string) error {
			email = strings.TrimSpace(email)
			if email == "" || !strings.Contains(email, "@") {
				return errors.New("--email <address> is required")
			}
			cfg, err := config.LoadConfig(cfgFile)
			if err != nil {
				return fmt.Errorf("read config: %w", err)
			}
			if !cfg.Platform.Enabled() {
				return errors.New("the platform control plane is not configured (PLATFORM_MYSQL_DSN)")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			st, err := platformstore.Open(ctx, cfg.Platform.DSN)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			removed, err := st.UnblockMailAddress(ctx, email)
			if err != nil {
				return err
			}
			if removed {
				fmt.Fprintln(cmd.OutOrStdout(), "unblocked")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "not on the block list")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "the address to unblock")
	rootCmd.AddCommand(cmd)
}
