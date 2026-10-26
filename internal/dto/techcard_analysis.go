package dto

import (
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardanalysis"
)

// TechCardAnalysisFindingToPb carries ONE finding of the CONSTRUCTION review onto the wire.
//
// IT IS DELIBERATELY THIN AND DUMB — string to string, slice to slice, and not one decision. Every
// judgement about a finding (which category, which severity, whether it is dropped, whether the
// readiness class collapses) has already been made: by the analyzer for a machine finding, by the
// verifier for a model one. A conversion that "improved" anything here would be a second, invisible
// policy layer sitting between the layer that decided and the client that shows the decision.
//
// techcardanalysis.Finding.Clause has NO wire counterpart and that is on purpose: it is the short
// phrase the draft collapse builds its enumeration from, it is consumed inside the analyzer, and it
// carries nothing the client could do anything with. Finding.Subject is the same kind of field: it
// is already folded into Key and is not sent on its own. seen_at stays absent unless the finding is
// seen (a zero time is not a moment anybody saw it).
func TechCardAnalysisFindingToPb(f techcardanalysis.Finding) *pb_admin.TechCardAnalysisFinding {
	pb := &pb_admin.TechCardAnalysisFinding{
		Source:      f.Source,
		Category:    f.Category,
		Severity:    f.Severity,
		Title:       f.Title,
		Detail:      f.Detail,
		Evidence:    copyStrings(f.Evidence),
		Refs:        copyStrings(f.Refs),
		InsertAfter: f.InsertAfter,
		Suggestion:  f.Suggestion,
		Confidence:  f.Confidence,
		Code:        f.Code,
		Key:         f.Key,
		Fingerprint: f.Fingerprint,
		Seen:        f.Seen,
		SeenBy:      f.SeenBy,
	}
	if !f.SeenAt.IsZero() {
		pb.SeenAt = timestamppb.New(f.SeenAt)
	}
	return pb
}

// TechCardAnalysisFindingsToPb converts a run's findings, preserving order. An empty run returns an
// empty slice rather than nil so the JSON body carries `findings: []` — a client that has to tell
// "no findings" from "the field is missing" should not have to.
func TechCardAnalysisFindingsToPb(findings []techcardanalysis.Finding) []*pb_admin.TechCardAnalysisFinding {
	out := make([]*pb_admin.TechCardAnalysisFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, TechCardAnalysisFindingToPb(f))
	}
	return out
}

// TechCardFindingSeenMarksToPb carries the bare «seen» marks of client findings
// (techcardanalysis.ClientSeenMarks). Empty, never nil, like the findings: `seen_marks: []`.
func TechCardFindingSeenMarksToPb(marks []entity.TechCardFindingSeen) []*pb_admin.TechCardFindingSeenMark {
	out := make([]*pb_admin.TechCardFindingSeenMark, 0, len(marks))
	for _, m := range marks {
		pb := &pb_admin.TechCardFindingSeenMark{
			Key:         m.FindingKey,
			Fingerprint: m.Fingerprint,
			SeenBy:      m.SeenBy,
		}
		if !m.SeenAt.IsZero() {
			pb.SeenAt = timestamppb.New(m.SeenAt)
		}
		out = append(out, pb)
	}
	return out
}

// copyStrings copies a string slice so the proto message never aliases the analyzer's backing
// array, and turns nil into nil (an absent list is absent, not an empty one).
func copyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
