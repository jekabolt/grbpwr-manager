package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsolationWorkerAndStripeSwitches(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("DELIVERY_SYNC_ENABLED", "false")
	t.Setenv("STRIPE_RECONCILE_ENABLED", "false")
	t.Setenv("STRIPE_PAYMENT_ENABLED", "false")
	t.Setenv("STRIPE_PAYMENT_TEST_ENABLED", "true")

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	require.False(t, cfg.DeliverySync.Enabled)
	require.False(t, cfg.StripeReconcile.Enabled)
	require.False(t, cfg.StripePayment.Enabled)
	require.True(t, cfg.StripePaymentTest.Enabled)
}

func TestIsolationSwitchesDefaultToExistingBehavior(t *testing.T) {
	t.Setenv("AUTH_JWT_SECRET", "test-secret")
	t.Setenv("DELIVERY_SYNC_ENABLED", "")
	t.Setenv("STRIPE_RECONCILE_ENABLED", "")
	t.Setenv("STRIPE_PAYMENT_ENABLED", "")
	t.Setenv("STRIPE_PAYMENT_TEST_ENABLED", "")

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	require.True(t, cfg.DeliverySync.Enabled)
	require.True(t, cfg.StripeReconcile.Enabled)
	require.True(t, cfg.StripePayment.Enabled)
	require.True(t, cfg.StripePaymentTest.Enabled)
}
