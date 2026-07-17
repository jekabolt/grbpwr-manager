// Package apierr is the shared bridge from domain errors to gRPC statuses for the PLM-rework
// write paths. It gives every workstream one place to turn a field-tagged
// entity.ValidationError (S24) into a google.rpc.BadRequest FieldViolation, and to map the
// optimistic-lock / not-found / precondition sentinels to the right gRPC code — so error shape
// stays consistent across handlers instead of being re-derived per RPC.
//
// TODO(plm-merge): WS3 (feat/plm/ws3-materials-bom) owns the canonical, richer version of this
// package (it also maps the material sentinels: ErrMaterialConflict / ErrMaterialNotFound /
// ErrMaterialCodeTaken / ErrMaterialUnitLocked). This WS1-local copy is a strict behavioural
// SUBSET of that one (identical Invalid/fieldViolation, a narrower Status switch), so on merge the
// WS3 version supersedes it wholesale — take theirs and delete this note.
package apierr

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Invalid maps a ValidationError to a gRPC InvalidArgument status. When the error is
// field-tagged (Field set) it attaches a google.rpc.BadRequest FieldViolation so REST/gRPC
// clients can bind the failure to the offending input; a message-only ValidationError degrades
// to a plain InvalidArgument. Never returns nil.
func Invalid(ve *entity.ValidationError) error {
	st := status.New(codes.InvalidArgument, ve.Error())
	if ve.Field != "" {
		if enriched, err := st.WithDetails(&errdetails.BadRequest{
			FieldViolations: []*errdetails.BadRequest_FieldViolation{fieldViolation(ve)},
		}); err == nil {
			st = enriched
		}
	}
	return st.Err()
}

// fieldViolation packs the structured parts of a field-tagged error into the single free-text
// Description that google.rpc.BadRequest.FieldViolation exposes, keeping reason / conflicting
// entity / how-to-fix legible to a client that only reads Field + Description.
func fieldViolation(ve *entity.ValidationError) *errdetails.BadRequest_FieldViolation {
	desc := ve.Reason
	if ve.Conflicting != "" {
		desc = strings.TrimSpace(desc + " (used by " + ve.Conflicting + ")")
	}
	if ve.HowToFix != "" {
		desc = strings.TrimSpace(desc + "; " + ve.HowToFix)
	}
	if desc == "" {
		desc = ve.Message
	}
	return &errdetails.BadRequest_FieldViolation{Field: ve.Field, Description: desc}
}

// Status maps the domain errors the PLM write paths raise to gRPC statuses. It returns
// (statusErr, true) when it recognises err, and (nil, false) when the caller should log the
// error and return codes.Internal itself (so unexpected failures still get contextual logging).
// A field-tagged ValidationError carries a BadRequest detail (see Invalid).
func Status(err error) (error, bool) {
	if err == nil {
		return nil, false
	}
	var ve *entity.ValidationError
	switch {
	case errors.As(err, &ve):
		return Invalid(ve), true
	case errors.Is(err, entity.ErrTechCardConflict):
		// Optimistic-lock mismatch: the client's expected version is stale — reload and retry.
		return status.Error(codes.Aborted, err.Error()), true
	case errors.Is(err, entity.ErrTechCardReleased), errors.Is(err, entity.ErrTechCardPurposeLocked):
		return status.Error(codes.FailedPrecondition, err.Error()), true
	case errors.Is(err, sql.ErrNoRows):
		return status.Error(codes.NotFound, err.Error()), true
	}
	return nil, false
}
