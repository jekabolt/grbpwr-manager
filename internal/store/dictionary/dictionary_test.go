package dictionary

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
)

// txMustNotRun is a TxFunc that fails the test if invoked — used to prove a mutation rejects invalid
// input during pre-transaction validation, before any DB work is attempted.
func txMustNotRun(t *testing.T) TxFunc {
	t.Helper()
	return func(ctx context.Context, f func(context.Context, dependency.Repository) error) error {
		t.Fatalf("txFunc must not run when pre-tx validation fails")
		return nil
	}
}

func TestColorMutationValidation(t *testing.T) {
	ctx := context.Background()
	s := &Store{txFunc: txMustNotRun(t)}

	if _, _, err := s.CreateColor(ctx, "xx", "black", "", 0); err == nil {
		t.Error("CreateColor with 2-char code should fail")
	}
	if _, _, err := s.CreateColor(ctx, "bl k", "black", "", 0); err == nil {
		t.Error("CreateColor with non-alnum code should fail")
	}
	if _, _, err := s.CreateColor(ctx, "BLK", "  ", "", 0); err == nil {
		t.Error("CreateColor with empty name should fail")
	}
	if _, err := s.UpdateColor(ctx, "toolong", "x", "", 0); err == nil {
		t.Error("UpdateColor with invalid code should fail")
	}
}

func TestCollectionAndTagNameValidation(t *testing.T) {
	ctx := context.Background()
	s := &Store{txFunc: txMustNotRun(t)}

	if _, _, err := s.CreateCollection(ctx, "   ", 0); err == nil {
		t.Error("CreateCollection with blank name should fail")
	}
	if _, _, err := s.CreateCollection(ctx, "!!!", 0); err == nil {
		t.Error("CreateCollection with no alphanumeric content should fail")
	}
	if _, err := s.UpdateCollection(ctx, 1, "", 0); err == nil {
		t.Error("UpdateCollection with blank name should fail")
	}
	if _, _, err := s.CreateTag(ctx, "", 0); err == nil {
		t.Error("CreateTag with blank name should fail")
	}
	if _, err := s.UpdateTag(ctx, 1, "  ", 0); err == nil {
		t.Error("UpdateTag with blank name should fail")
	}
}

func TestSetCountryActiveValidation(t *testing.T) {
	ctx := context.Background()
	s := &Store{txFunc: txMustNotRun(t)}

	if _, err := s.SetCountryActive(ctx, "USA", true, 0); err == nil {
		t.Error("SetCountryActive with 3-char code should fail (ISO alpha-2 only)")
	}
	if _, err := s.SetCountryActive(ctx, "", true, 0); err == nil {
		t.Error("SetCountryActive with empty code should fail")
	}
}
