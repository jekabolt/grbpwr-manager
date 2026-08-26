package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф3.4 — THE REPORT, AFTER THE IMPORT IS OVER.
//
// An imported card is deliberately indistinguishable from one somebody typed: the create pipeline
// wrote it, it carries no badge, and nothing in its columns says a machine filled them in. The
// stored report is the ONLY memory of where it came from and of what did not fit — which makes
// these two calls the whole of the after-life of an import: one shows the report, the other says
// a human read it.
//
// THREE THINGS THIS FILE IS CAREFUL ABOUT:
//
//   - A CARD WITHOUT AN IMPORT IS NORMAL, NOT BROKEN. Every card anybody ever typed by hand
//     reaches this call with no row behind it, and the client asks on a flag rather than on
//     knowledge. So «no row» is NOT_FOUND with nothing in it — never Internal, and never an empty
//     200 either, because an empty report and an absent report are different sentences on screen
//     («nothing was lost» versus «this card was not imported»).
//   - THE REPORT IS PARSED, NOT PASSED THROUGH. It sits in the database as JSON and it answers an
//     RPC as a message; the honest way between those two is the package that owns the report
//     (techcardarchive.ParseReport). Answering with the stored bytes shovelled into a string field
//     would make this server a pipe for a payload it never checked, and would fossilise whatever
//     shape the writing binary happened to have.
//   - READING SURVIVES A NEWER WRITER. The report evolves the way the format does — additively —
//     and a rolling deploy is enough for the process reading a report to be older than the one
//     that wrote it. ParseReport therefore reads with DiscardUnknown (see report_amend.go): an
//     unknown field is dropped, not answered with a 500 on a card whose only sin is having been
//     imported an hour after the deploy started.
//
// Ф3.3 owns techcard_archive.go in the same wave; these two methods live here so the two tasks
// never touch one file (WAVES §3).
// ─────────────────────────────────────────────────────────────────────────────

// GetTechCardImportReport answers with the report of the LATEST archive this card came from, and
// with the moment a person marked it read.
//
// rd(tech_cards): this is a reading of the card's own history, in the panel, by whoever may open
// the card. (Export is wr because it carries the card OUT of the building — a different act, and
// the difference is stated in internal/rbac, not re-checked here.)
//
// acknowledged_at travels back UNSET rather than zeroed when nobody has closed the banner yet:
// the client's whole question is «is the banner still up», and an explicit null is what answers it
// (a zero Timestamp would render as 1970 and read as «closed long ago»).
func (s *Server) GetTechCardImportReport(ctx context.Context,
	req *pb_admin.GetTechCardImportReportRequest) (*pb_admin.GetTechCardImportReportResponse, error) {
	techCardID := int(req.GetTechCardId())
	if techCardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}

	rec, err := s.repo.TechCards().GetTechCardImportReport(ctx, techCardID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errTechCardImportReportAbsent
		}
		slog.Default().ErrorContext(ctx, "tech card import report: can't read the import row",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't read the import report")
	}

	// A row that names a card but carries no report cannot arise from this code — the commit
	// stamps tech_card_id and report in ONE statement — so it means somebody edited the table by
	// hand or an older writer left it. It is still not an error to the operator: the card has no
	// report to show, which is exactly the sentence NOT_FOUND already carries. Logged, because
	// nothing else would ever notice it.
	if len(rec.Report) == 0 {
		slog.Default().WarnContext(ctx, "tech card import report: the import row carries no report",
			slog.Int("tech_card_id", techCardID), slog.String("import_id", rec.ImportID),
			slog.String("status", rec.Status))
		return nil, errTechCardImportReportAbsent
	}

	parsed, err := techcardarchive.ParseReport(rec.Report)
	if err != nil {
		// The row claims a report and it does not read as one. That is corruption of a record the
		// operator is entitled to, and the one thing it must not do is masquerade as «this card was
		// not imported» — a NOT_FOUND here would quietly retire the evidence.
		slog.Default().ErrorContext(ctx, "tech card import report: the stored report does not read as one",
			slog.Int("tech_card_id", techCardID), slog.String("import_id", rec.ImportID),
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "the stored import report cannot be read")
	}
	report := parsed.Message()
	if report == nil { // unreachable: ParseReport never returns a report without a message
		return nil, status.Error(codes.Internal, "the stored import report cannot be read")
	}

	resp := &pb_admin.GetTechCardImportReportResponse{Report: report}
	if rec.AcknowledgedAt.Valid {
		resp.AcknowledgedAt = timestamppb.New(rec.AcknowledgedAt.Time)
	}
	return resp, nil
}

// errTechCardImportReportAbsent is the one answer for «this card has no import report», stated once
// so the two ways of arriving at it cannot drift into two different sentences on screen.
var errTechCardImportReportAbsent = status.Error(codes.NotFound, "this tech card has no import report")

// AcknowledgeTechCardImportReport records that a person read the report and takes its holes on.
//
// IDEMPOTENT, and idempotent in the store rather than here: the UPDATE is guarded by
// `acknowledged_at IS NULL`, so a second click writes nothing at all and the stamp keeps the moment
// the report was ACTUALLY read instead of the moment somebody last clicked. That is why this
// handler does not read the row first to decide whether to write — a read-then-write would need a
// reason to exist beyond politeness, and would introduce the race the guard already closes.
//
// wr(tech_cards) and not rd: the gesture takes a warning off the card for EVERYONE who opens it
// afterwards. It belongs to whoever owns the card, not to whoever happens to be reading it.
//
// A card that was never imported acknowledges to nothing and that is not an error: the statement
// matches no row, the response is empty either way, and inventing a NOT_FOUND here would make the
// client's «close the banner» handler depend on a race with whatever else touched the card.
func (s *Server) AcknowledgeTechCardImportReport(ctx context.Context,
	req *pb_admin.AcknowledgeTechCardImportReportRequest) (*pb_admin.AcknowledgeTechCardImportReportResponse, error) {
	techCardID := int(req.GetTechCardId())
	if techCardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}

	if err := s.repo.TechCards().AcknowledgeTechCardImport(ctx, techCardID); err != nil {
		slog.Default().ErrorContext(ctx, "tech card import report: can't acknowledge",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't close the import report")
	}
	return &pb_admin.AcknowledgeTechCardImportReportResponse{}, nil
}
