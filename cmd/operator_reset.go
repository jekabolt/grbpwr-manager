package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/spf13/cobra"
)

// operator-reset-app removes one operator's authenticator app and recovery codes from the control
// plane (wave teams, gap 4): the lost-phone path when no other operator can reset it from the
// console. It runs on the server (DO app console: `products-manager operator-reset-app --email ...`),
// never by mail. The operator's console sessions end; the next sign-in sets a new app up, which
// needs the password and a code mailed to the operator's address. Every operator gets a letter.
var (
	operatorResetEmail string

	operatorResetCmd = &cobra.Command{
		Use:   "operator-reset-app",
		Short: "Remove an operator's authenticator app (lost phone)",
		RunE:  runOperatorReset,
	}
)

func init() {
	operatorResetCmd.Flags().StringVar(&operatorResetEmail, "email", "", "the operator's email (required)")
	rootCmd.AddCommand(operatorResetCmd)
}

func runOperatorReset(cmd *cobra.Command, _ []string) error {
	email, err := platformauth.NormalizeEmail(operatorResetEmail)
	if err != nil {
		return fmt.Errorf("--email: %w", err)
	}
	cfg, err := config.LoadConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Platform.Enabled() {
		return errors.New("teams mode is off (no PLATFORM_MYSQL_DSN)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	st, err := platformstore.Open(ctx, cfg.Platform.DSN)
	if err != nil {
		return err
	}
	defer st.Close()
	sids, err := st.ResetOperatorSecondStep(ctx, email, "server", time.Now().UTC())
	if errors.Is(err, platformstore.ErrNotOperator) {
		return errors.New("no active operator with that email")
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "app removed; %d console session(s) ended; operators notified\n", len(sids))
	return nil
}
