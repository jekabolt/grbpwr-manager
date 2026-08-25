package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/techcardanalysis"
	"github.com/stretchr/testify/require"
)

// tcaFullFinding is a finding with EVERY field populated with a distinguishable value — including
// the two that no machine finding carries in practice (insert_after belongs to missing_step,
// confidence to heuristics and to the model). A conversion is tested on the full shape or the
// fields that are usually empty are never tested at all.
func tcaFullFinding() techcardanalysis.Finding {
	return techcardanalysis.Finding{
		Source:      techcardanalysis.SourceMachine,
		Category:    techcardanalysis.CategoryMissingStep,
		Severity:    techcardanalysis.SeverityBlocker,
		Title:       "Nothing closes the side seam",
		Detail:      "The route joins the front and the back and never sews the side.",
		Evidence:    []string{"op 120 outputs unit \"shell\"", "no step consumes SL_L"},
		Refs:        []string{"op:120", "piece:SL_L", "card"},
		InsertAfter: "op:120",
		Suggestion:  "Add a lockstitch step joining SL_L to the shell.",
		Confidence:  techcardanalysis.ConfidenceHeuristic,
		Clause:      "no side seam",
	}
}

// TestTechCardAnalysisFindingToPbCarriesEveryField pins the conversion field by field.
//
// The interesting half is the LAST assertion. Every proto field is walked through the message
// descriptor and required to be populated, so a field added to TechCardAnalysisFinding on the wire
// and then forgotten in this converter fails HERE — naming the field — instead of shipping as a
// value that is silently always empty for every client that reads it.
func TestTechCardAnalysisFindingToPbCarriesEveryField(t *testing.T) {
	f := tcaFullFinding()
	pb := TechCardAnalysisFindingToPb(f)

	require.Equal(t, f.Source, pb.GetSource())
	require.Equal(t, f.Category, pb.GetCategory())
	require.Equal(t, f.Severity, pb.GetSeverity())
	require.Equal(t, f.Title, pb.GetTitle())
	require.Equal(t, f.Detail, pb.GetDetail())
	require.Equal(t, f.Evidence, pb.GetEvidence())
	require.Equal(t, f.Refs, pb.GetRefs())
	require.Equal(t, f.InsertAfter, pb.GetInsertAfter())
	require.Equal(t, f.Suggestion, pb.GetSuggestion())
	require.Equal(t, f.Confidence, pb.GetConfidence())

	// Slices must be copies, not aliases of the analyzer's backing arrays.
	f.Refs[0] = "op:999"
	require.Equal(t, "op:120", pb.GetRefs()[0], "the proto message aliases the analyzer's slice")

	md := pb.ProtoReflect().Descriptor()
	for i := 0; i < md.Fields().Len(); i++ {
		fd := md.Fields().Get(i)
		if !pb.ProtoReflect().Has(fd) {
			t.Errorf("proto field %q is not populated by TechCardAnalysisFindingToPb — either carry it "+
				"or say in the converter why it deliberately has no counterpart (Clause is the one such case)",
				fd.Name())
		}
	}
}

// TestTechCardAnalysisFindingsToPbIsOrderPreservingAndNeverNil: the audit's sort order is the whole
// contract of the list (the client re-sorts for display but compares runs by position), and an empty
// run must arrive as an empty list rather than as an absent field.
func TestTechCardAnalysisFindingsToPbIsOrderPreservingAndNeverNil(t *testing.T) {
	in := []techcardanalysis.Finding{
		{Title: "first", Source: techcardanalysis.SourceMachine},
		{Title: "second", Source: techcardanalysis.SourceMachine},
		{Title: "third", Source: techcardanalysis.SourceMachine},
	}
	out := TechCardAnalysisFindingsToPb(in)
	require.Len(t, out, 3)
	require.Equal(t, []string{"first", "second", "third"},
		[]string{out[0].GetTitle(), out[1].GetTitle(), out[2].GetTitle()})

	require.NotNil(t, TechCardAnalysisFindingsToPb(nil), "an empty run converts to an empty list, not to nil")
	require.Empty(t, TechCardAnalysisFindingsToPb(nil))
}

// TestTechCardAnalysisFindingToPbDoesNotInvent guards the thinness of the converter: it must not
// substitute a default for an empty field. A machine finding with no confidence means "this is a
// deterministic fact", and a converter that helpfully filled in "certain" would be inventing a
// claim the analyzer never made.
func TestTechCardAnalysisFindingToPbDoesNotInvent(t *testing.T) {
	pb := TechCardAnalysisFindingToPb(techcardanalysis.Finding{Title: "bare"})
	require.Empty(t, pb.GetSource(), "source is not defaulted here — RunAudit stamps it")
	require.Empty(t, pb.GetConfidence(), "empty confidence means a deterministic fact, not an unknown one")
	require.Empty(t, pb.GetCategory())
	require.Empty(t, pb.GetSeverity())
	require.Nil(t, pb.GetRefs())
	require.Nil(t, pb.GetEvidence())
}
