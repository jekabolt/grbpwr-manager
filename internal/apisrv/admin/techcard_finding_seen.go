package admin

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/techcardanalysis"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// findingFingerprintRe is the shape of TechCardAnalysisFinding.fingerprint.
var findingFingerprintRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// SetTechCardFindingSeen marks or unmarks ONE machine finding of the construction audit as seen (T9).
//
// THE SERVER STORES WHAT THE CLIENT SAW and does not re-run the audit to check it: the match is
// decided on read (GetTechCardConstructionAudit → techcardanalysis.ApplySeen), so a mark made on
// text that has since changed simply does not count. Idempotent both ways; no lock_version and no
// released-card refusal — a mark is a note about the audit, not card content.
func (s *Server) SetTechCardFindingSeen(ctx context.Context, req *pb_admin.SetTechCardFindingSeenRequest) (*pb_admin.SetTechCardFindingSeenResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	key := req.GetKey()
	if strings.TrimSpace(key) == "" {
		return nil, status.Error(codes.InvalidArgument, "key is required")
	}
	if len(key) > techcardanalysis.MaxFindingKeyBytes || !utf8.ValidString(key) {
		return nil, status.Errorf(codes.InvalidArgument, "key must be valid UTF-8 of at most %d bytes", techcardanalysis.MaxFindingKeyBytes)
	}
	techCardID := int(req.GetTechCardId())

	if !req.GetSeen() {
		if err := s.repo.TechCards().UnmarkTechCardFindingSeen(ctx, techCardID, key); err != nil {
			slog.Default().ErrorContext(ctx, "can't unmark finding seen",
				slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "can't unmark finding seen")
		}
		return &pb_admin.SetTechCardFindingSeenResponse{}, nil
	}

	fingerprint := req.GetFingerprint()
	if !findingFingerprintRe.MatchString(fingerprint) {
		return nil, status.Error(codes.InvalidArgument, "fingerprint must be 16 lowercase hex characters")
	}
	if err := s.repo.TechCards().MarkTechCardFindingSeen(ctx, techCardID, key, fingerprint, authsrv.GetAdminUsername(ctx)); err != nil {
		// tech_card_id is the only foreign key on the row: a violation means the card is gone.
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't mark finding seen",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't mark finding seen")
	}
	return &pb_admin.SetTechCardFindingSeenResponse{}, nil
}
