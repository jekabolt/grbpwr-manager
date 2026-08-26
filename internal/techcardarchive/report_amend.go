package techcardarchive

import (
	"fmt"
	"sort"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE SECOND HALF OF A REPORT: what the WRITE dropped.
//
// BuildReport answers the DRY RUN — the resolver's holes and the resolver's tally, both produced
// before anything is written. The write then drops rows of its own, and it cannot help it: only
// the transaction knows the imported card's own size range, the purpose of a component in this
// base, or that a measurement grid names a size this particular card does not make. Those rows go
// away INSIDE the transaction, after the report was built.
//
// A report stamped unchanged onto tech_card_import therefore states that rows were imported which
// were not. That is the worst kind of wrong an import report can be: an operator reads it exactly
// once, believes it, and never looks at the card again.
//
// So the write amends the report before it stamps it, in the same transaction as the writes it
// describes — and it does it HERE, in the package that owns the report, because a line assembled
// anywhere else would carry no action text (the sentence for a reason code lives in report.go and
// nowhere else) and would drift from the closed dictionary the moment either side moved.
//
// The store is deliberately left with no way to write the report WITHOUT passing through this
// file: it holds the report as an opaque *ImportReport from the moment it parses it, and the only
// thing that turns one back into bytes is Amend.
// ─────────────────────────────────────────────────────────────────────────────

// reportMarshalOptions is the shape a report has on the wire, and it is one shape on purpose: the
// dry run answers the upload route with these bytes and the commit stores them, and a client that
// renders zero counters from the first would find them missing from the second. EmitUnpopulated is
// what keeps «we counted none» visibly different from «we never looked» after the JSON round trip
// — the same distinction NewCounters exists for.
//
// The upload route (internal/apisrv/admin/techcard_archive_upload.go) spells the same two options
// out by hand; when that file is next touched it should marshal through MarshalReport instead, so
// there is one statement of the wire shape rather than two that agree today.
var reportMarshalOptions = protojson.MarshalOptions{EmitUnpopulated: true, UseProtoNames: false}

// reportUnmarshalOptions reads a report the way §3 of FORMAT.md says a MINOR is read: unknown
// fields are IGNORED, not fatal.
//
// The report is not a wire message that lives for one call — it is stored in tech_card_import.report
// and read back from the card months later, by whichever binary happens to be running then. A
// rolling deploy alone is enough to have one process write a report carrying a field the process
// reading it has no member for, and a strict parse would answer that with a 500 on a card whose
// only sin is having been imported by a newer server. The report evolves the way the format does —
// new fields only — so the field a reader does not know is exactly the field it can afford to drop.
//
// What this does NOT relax is whether the payload is a report at all: a wrong TYPE on a known field
// still fails, which is what keeps ParseReport a gate in front of the write's transaction.
var reportUnmarshalOptions = protojson.UnmarshalOptions{DiscardUnknown: true}

// ImportReport is a report that has already been built once and is on its way into
// tech_card_import.report — parsed, so the write can add to it, and opaque, so the write cannot
// assemble a line by hand.
//
// It is parsed OUTSIDE the transaction and amended inside it. That split is not cosmetic: parsing
// is where a malformed report is caught, and catching it inside the transaction would mean
// catching it at the last statement, after the card, its children, its chart and its markers are
// all written — the exact shape of failure the import path spends its whole design avoiding.
type ImportReport struct {
	msg *pb_admin.TechCardImportReport
}

// ParseReport reads the bytes BuildReport produced and MarshalReport wrote.
//
// It is stricter than a JSON well-formedness check on purpose: «valid JSON» says nothing about
// whether the payload is a report, and the write has to be able to count what is in it. An empty
// or absent payload is refused here rather than defaulted to an empty report — a card whose gaps
// nobody can explain is precisely what storing the report in the same transaction prevents.
func ParseReport(b []byte) (*ImportReport, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("the import report is empty")
	}
	msg := &pb_admin.TechCardImportReport{}
	if err := reportUnmarshalOptions.Unmarshal(b, msg); err != nil {
		return nil, fmt.Errorf("the import report does not read as one: %w", err)
	}
	return &ImportReport{msg: msg}, nil
}

// Message returns the parsed report as the message the RPC answers with.
//
// It exists so that the READ path (GetTechCardImportReport) parses stored reports through the same
// door the write path does, instead of standing up a second protojson call with its own options —
// two parsers of one payload are two chances to disagree about whether an unknown field is fatal.
//
// A COPY, because ImportReport is otherwise opaque on purpose: handing out the pointer would let a
// caller edit the report the amend is built from, and Amend's promise not to modify its receiver
// (a deadlock retry must not count one dropped row twice) would stop being the package's to keep.
func (r *ImportReport) Message() *pb_admin.TechCardImportReport {
	if r == nil || r.msg == nil {
		return nil
	}
	out, ok := proto.Clone(r.msg).(*pb_admin.TechCardImportReport)
	if !ok { // unreachable: Clone returns the same concrete type it was given
		return nil
	}
	return out
}

// MarshalReport writes a report the way both routes answer with one.
func MarshalReport(rep *pb_admin.TechCardImportReport) ([]byte, error) {
	b, err := reportMarshalOptions.Marshal(rep)
	if err != nil {
		return nil, fmt.Errorf("encode the import report: %w", err)
	}
	return b, nil
}

// Amend folds the WRITE's own losses into the report and returns the bytes to store.
//
// holes are lines, one per thing the write could not do, with the same closed reason codes and the
// same action sentences every other line carries — an operator must not be able to tell which side
// of the transaction a hole came from, because the answer to «what do I do about it» does not
// depend on that.
//
// lost is counted with the SAME Counters the resolver uses, and is read as a MOVE rather than an
// addition: AddSkipped(entity, n) on it means «n rows of that entity, which the dry run counted as
// imported, did not land after all», so n leaves the imported column and enters skipped.
// AddDegraded moves into degraded the same way. AddImported has no meaning here — a write cannot
// import a row the plan never carried — and is REFUSED rather than ignored, because a number
// nobody reads is how a counter starts lying.
//
// Moving rather than adding is what keeps the positive control valid on the stamped report: a move
// leaves EntityTally.Sum() alone, so an archive that claims fourteen media still has fourteen media
// accounted for after the write took some of them away.
//
// A move is CAPPED at what the imported column actually holds, and never goes negative: if the two
// halves disagree about how many rows there were, the report says «none of them imported», which is
// the honest end of the disagreement — the alternative, a negative count, is refused by
// ValidateReportAgainstManifest and would turn a counting bug into a failed import.
//
// The receiver is not modified: Amend is called inside a SERIALIZABLE transaction whose closure is
// re-entered on a deadlock retry, and a report that accumulated one attempt's losses on top of the
// previous attempt's would count every dropped row twice.
func (r *ImportReport) Amend(holes []ImportHole, lost Counters) ([]byte, error) {
	if r == nil || r.msg == nil {
		return nil, fmt.Errorf("no import report to amend")
	}
	out, ok := proto.Clone(r.msg).(*pb_admin.TechCardImportReport)
	if !ok { // unreachable: Clone returns the same concrete type it was given
		return nil, fmt.Errorf("the import report did not clone as one")
	}

	for _, h := range holes {
		status := h.Status
		if status == "" {
			status = DefaultStatusFor(h.Reason)
		}
		out.Lines = append(out.Lines, reportLine(h.Entity, h.Ref, status, h.Reason, h.Detail))
	}
	if err := moveLostCounters(out, lost); err != nil {
		return nil, err
	}
	return MarshalReport(out)
}

// ApplyColorways REPLACES the colourway half of a stored report, and it is the one operation on a
// report that removes lines rather than adding them.
//
// It exists for the second, explicit step of an import: colourways travel as reference, the import
// creates none and writes one `colorways_not_applied` line per colour, and later somebody presses
// «create colourways from archive». After that press those lines are FALSE — they say the colours
// are not on the card, standing next to the colours that now are. An Amend cannot fix that: Amend
// only appends lines and only moves rows OUT of the imported column, so the card would end up
// carrying both verdicts and a tally that counts every colour twice.
//
// So: the lines about the colourways the caller PRONOUNCED ON go, whatever their reason, and the
// caller's lines take their place. Whatever the import said about those has just been superseded —
// by an action that looked at every one of them — and keeping a stale half of it would be the same
// lie in smaller print. Lines about anything else are untouched; a media hole is not news this
// action has any standing to revise.
//
// WHICH colourway lines those are is the CALLER'S to say, through `superseded`, and it is not «all
// of them». Two kinds of colourway line are about something the press did not decide:
//
//   - a line filed against a CUT PIECE rather than a colour (ref piece_line_key=…, written by the
//     resolver for a piece that named its cloth per colourway). A press where every colour turned
//     out to be standing decides nothing, and used to erase that line and put nothing in its place
//     — the mapping still had not arrived and the report had stopped saying so.
//   - a line about a colour the press deliberately left alone, which is every colour a PREVIOUS
//     press already created. Replacing those turned a clean first press into a degraded second one
//     and erased the first press's record of what it could not write — a record nothing re-attempts.
//
// A nil `superseded` keeps the old meaning (every colourway line goes), which is what a caller that
// really did look at all of them wants.
//
// tally REPLACES the colourway counter rather than moving rows within it, for the same reason: the
// import counted every colour as skipped, and after this action each one is imported, degraded or
// still skipped. The caller is required to have counted each colour exactly once — this function
// cannot check that, because a colour legitimately produces SEVERAL lines (its own verdict plus one
// per lost pin) and lines have never been the counter's source (see report.go). A colour the caller
// left alone still counts: it counts as whatever the report already said about it.
//
// The receiver is not modified, exactly like Amend: a caller that retries must not accumulate.
func (r *ImportReport) ApplyColorways(holes []ImportHole, tally EntityTally, superseded func(ref string) bool) ([]byte, error) {
	if r == nil || r.msg == nil {
		return nil, fmt.Errorf("no import report to apply colourways to")
	}
	out, ok := proto.Clone(r.msg).(*pb_admin.TechCardImportReport)
	if !ok { // unreachable: Clone returns the same concrete type it was given
		return nil, fmt.Errorf("the import report did not clone as one")
	}

	kept := make([]*pb_admin.TechCardImportReportLine, 0, len(out.GetLines())+len(holes))
	for _, l := range out.GetLines() {
		if l.GetEntity() == EntityColorway && (superseded == nil || superseded(l.GetRef())) {
			continue
		}
		kept = append(kept, l)
	}
	for _, h := range holes {
		status := h.Status
		if status == "" {
			status = DefaultStatusFor(h.Reason)
		}
		kept = append(kept, reportLine(h.Entity, h.Ref, status, h.Reason, h.Detail))
	}
	if len(kept) == 0 {
		out.Lines = nil
	} else {
		out.Lines = kept
	}

	replaced := false
	for _, c := range out.GetCounters() {
		if c.GetEntity() != EntityColorway {
			continue
		}
		c.Imported, c.Skipped, c.Degraded = clampCount(tally.Imported), clampCount(tally.Skipped), clampCount(tally.Degraded)
		replaced = true
	}
	if !replaced {
		// A report assembled by BuildReport always carries the row (CountedEntities). One that does
		// not was assembled elsewhere or by an older writer — appending is right, because dropping
		// the tally would leave the card claiming nobody ever looked at its colourways.
		out.Counters = append(out.Counters, counterOf(EntityColorway, tally))
	}
	return MarshalReport(out)
}

// moveLostCounters applies the write's losses to the tally, entity by entity in a fixed order so
// the refusal below names the same entity on every run.
func moveLostCounters(rep *pb_admin.TechCardImportReport, lost Counters) error {
	if len(lost) == 0 {
		return nil
	}
	byEntity := make(map[string]*pb_admin.TechCardImportCounter, len(rep.GetCounters()))
	for _, c := range rep.GetCounters() {
		if c != nil {
			byEntity[c.GetEntity()] = c
		}
	}

	names := make([]string, 0, len(lost))
	for e := range lost {
		names = append(names, e)
	}
	sort.Strings(names)

	for _, e := range names {
		t := lost[e]
		if t.Imported != 0 {
			return fmt.Errorf("the write reported %d IMPORTED rows of %q as a loss: losses move rows "+
				"out of the imported column, they never add any", t.Imported, e)
		}
		if t.Skipped == 0 && t.Degraded == 0 {
			continue
		}
		c := byEntity[e]
		if c == nil {
			// An entity BuildReport does not count (card, material, measurement, archive) has no
			// imported column for a row to leave. The LINE is the whole record of the loss, and
			// inventing a counter row for it here would claim rows nobody ever counted.
			continue
		}
		moveCount(&c.Imported, &c.Skipped, t.Skipped)
		moveCount(&c.Imported, &c.Degraded, t.Degraded)
	}
	return nil
}

// moveCount moves n rows from one column to another, taking only what is there.
func moveCount(from, to *int32, n int) {
	if n <= 0 || *from <= 0 {
		return
	}
	if int64(n) > int64(*from) {
		n = int(*from)
	}
	*from -= int32(n)
	*to += int32(n)
}
