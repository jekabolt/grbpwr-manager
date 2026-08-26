package techcardarchive

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ─────────────────────────────────────────────────────────────────────────────
// TestSanitizeImportedCard        — the behaviour, on a fully approved+released card.
// TestSanitizeImportedCardFromJSON — the same through protojson, i.e. the real import path.
// TestSanitizeFieldGuard          — the guard that keeps ApprovalFieldNames from rotting.
// ─────────────────────────────────────────────────────────────────────────────

func sanitizeStamp() *timestamppb.Timestamp {
	return timestamppb.New(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
}

// signoffSections reads the sections off the ENUM DESCRIPTOR rather than listing them.
// The phase brief says "all 7 sections"; seven is what the contract holds today
// (DESIGN, CONSTRUCTION, MATERIALS, COLOUR, LABELS, PACKAGING, COSTING — number 3 is
// reserved, POM was removed). Reading the descriptor means an eighth section added later
// is covered by this test on the day it appears instead of on the day somebody remembers.
func signoffSections(t *testing.T) []pb_common.TechCardSignoffSection {
	t.Helper()
	vals := pb_common.TechCardSignoffSection(0).Descriptor().Values()
	out := make([]pb_common.TechCardSignoffSection, 0, vals.Len())
	for i := 0; i < vals.Len(); i++ {
		n := vals.Get(i).Number()
		if n == 0 { // UNKNOWN is "unset", not a section
			continue
		}
		out = append(out, pb_common.TechCardSignoffSection(n))
	}
	if len(out) < 7 {
		t.Fatalf("expected at least the 7 known sign-off sections, descriptor gave %d", len(out))
	}
	return out
}

// approvedReleasedCard is the thing the sanitiser exists for: a card that arrives already
// wearing every mark of an approval nobody in this database performed — RELEASED, both
// stamps, and an APPROVED sign-off for every section, each with a signer, a time and a
// content digest.
func approvedReleasedCard(t *testing.T) *pb_common.TechCardInsert {
	t.Helper()
	c := &pb_common.TechCardInsert{
		StyleNumber:    "FW26-0007",
		Name:           "coat",
		Status:         "released to the factory on the 3rd", // freeform prose; must survive
		Stage:          pb_common.TechCardStage_TECH_CARD_STAGE_PROD,
		ApprovalState:  pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED,
		ReleasedAt:     sanitizeStamp(),
		ApprovedAt:     sanitizeStamp(),
		Notes:          "outer shell 2/2",
		CategoryId:     11,
		SizeIds:        []int32{3, 4, 5},
		BomItems:       []*pb_common.TechCardBomItem{{LineKey: "shell", MaterialId: 42}},
		Operations:     []*pb_common.TechCardOperation{{OperationNumber: 10, Note: "join shoulders"}},
		TargetDropDate: sanitizeStamp(),
	}
	for _, sec := range signoffSections(t) {
		c.Signoffs = append(c.Signoffs, &pb_common.TechCardSignoff{
			Section:      sec,
			State:        pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED,
			SignedBy:     "someone at the source company",
			SignedAt:     sanitizeStamp(),
			Note:         "approved",
			SignedDigest: "cafebabe",
		})
	}
	return c
}

// wantClearedNames is the oracle, written out LITERALLY and deliberately not read from
// ApprovalFieldNames. A test that asks "is any field named in ApprovalFieldNames still
// populated" moves with the list it is checking: delete `signoffs` from the production list
// and the walk stops looking for sign-offs, so the sanitiser stops clearing them and the
// test stays green — a watchman assigned by the thing he is watching. This literal is the
// independent statement of what must be gone; TestSanitizeFieldGuard is what keeps it and
// the production list from drifting apart (see the cross-check in assertNoApprovalMarks).
//
// approval_state is not here: it is the one field the sanitiser SETS rather than clears, so
// "populated" is its correct state. It is asserted by value instead.
var wantClearedNames = map[string]bool{
	"released_at": true,
	"approved_at": true,
	"signoffs":    true,
}

// assertNoApprovalMarks walks the RESULT and asserts that no field named in wantClearedNames
// is populated anywhere in the tree, and that the state reads DRAFT.
//
// A walk rather than three struct comparisons on purpose: three comparisons pass whenever
// the three assignments are present, which is the same thing the code says, so they can
// only catch a typo. This asks the question the phase actually cares about — "does the
// output carry any approval mark at all" — of the whole tree, so a nested one added later
// fails here even though no assertion was written for it.
func assertNoApprovalMarks(t *testing.T, card *pb_common.TechCardInsert) {
	t.Helper()
	// The oracle and the production list must describe the same family. They are written
	// separately so that a mutation to one is caught by the other; this is where the pair is
	// held together.
	if len(ApprovalFieldNames) != len(wantClearedNames)+1 || !ApprovalFieldNames["approval_state"] {
		t.Fatalf("ApprovalFieldNames %v and the test oracle %v have drifted apart",
			approvalSortedNames(ApprovalFieldNames), approvalSortedNames(wantClearedNames))
	}
	for n := range wantClearedNames {
		if !ApprovalFieldNames[n] {
			t.Fatalf("%q is in the test oracle but not in ApprovalFieldNames", n)
		}
	}

	var found []string
	scanApprovalMarks(card.ProtoReflect(), string(card.ProtoReflect().Descriptor().Name()), &found)
	if len(found) != 0 {
		t.Fatalf("sanitised card still carries approval marks: %v", found)
	}
	if card.ApprovalState != pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_DRAFT {
		t.Fatalf("approval_state = %v, want DRAFT", card.ApprovalState)
	}
}

func approvalSortedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// scanApprovalMarks collects the paths of populated wantClearedNames fields.
func scanApprovalMarks(m protoreflect.Message, path string, found *[]string) {
	if m == nil || !m.IsValid() {
		return
	}
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		name := string(fd.Name())
		p := path + "." + name
		if wantClearedNames[name] {
			*found = append(*found, p)
			return true
		}
		switch {
		case fd.IsMap():
			if isMessageValueMap(fd) {
				v.Map().Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
					scanApprovalMarks(mv.Message(), fmt.Sprintf("%s[%v]", p, k), found)
					return true
				})
			}
		case fd.IsList() && isMessageKind(fd):
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				scanApprovalMarks(l.Get(i).Message(), fmt.Sprintf("%s[%d]", p, i), found)
			}
		case isMessageKind(fd):
			scanApprovalMarks(v.Message(), p, found)
		}
		return true
	})
}

func TestSanitizeImportedCard(t *testing.T) {
	t.Run("released and signed card is forced to draft", func(t *testing.T) {
		card := approvedReleasedCard(t)
		before := proto.Clone(card).(*pb_common.TechCardInsert)

		SanitizeImportedCard(card)

		assertNoApprovalMarks(t, card)
		if card.ReleasedAt != nil {
			t.Errorf("released_at = %v, want nil", card.ReleasedAt)
		}
		if card.ApprovedAt != nil {
			t.Errorf("approved_at = %v, want nil", card.ApprovedAt)
		}
		if len(card.Signoffs) != 0 {
			t.Errorf("signoffs = %d entries, want none", len(card.Signoffs))
		}

		// Everything outside the approval family is untouched — including `status`, whose
		// prose says "released" and is none of the sanitiser's business.
		if card.Status != before.Status {
			t.Errorf("status = %q, want %q (freeform prose, not an approval)", card.Status, before.Status)
		}
		if card.StyleNumber != before.StyleNumber {
			t.Errorf("style_number = %q, want %q (collision strategy is Ф3.3's)", card.StyleNumber, before.StyleNumber)
		}
		if card.Stage != before.Stage {
			t.Errorf("stage = %v, want %v", card.Stage, before.Stage)
		}
		if card.Name != before.Name || card.Notes != before.Notes || card.CategoryId != before.CategoryId {
			t.Errorf("identity fields changed: %+v", card)
		}
		if !proto.Equal(card.TargetDropDate, before.TargetDropDate) {
			t.Errorf("target_drop_date changed: it is owner-set intent, not a workflow stamp")
		}
		if len(card.SizeIds) != 3 || len(card.BomItems) != 1 || len(card.Operations) != 1 {
			t.Errorf("children changed: sizes=%d bom=%d ops=%d", len(card.SizeIds), len(card.BomItems), len(card.Operations))
		}

		// The only difference between input and output must BE the approval family: put the
		// marks back on the clone's counterpart and the two messages must be equal again.
		// This is the "nothing else moved" assertion in the form that cannot be outrun by a
		// field nobody wrote a getter comparison for.
		before.ApprovalState = pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_DRAFT
		before.ReleasedAt, before.ApprovedAt, before.Signoffs = nil, nil, nil
		if !proto.Equal(card, before) {
			t.Errorf("sanitiser changed something outside the approval family:\n got %v\nwant %v", card, before)
		}
	})

	t.Run("empty card is stamped DRAFT, not left UNKNOWN", func(t *testing.T) {
		card := &pb_common.TechCardInsert{}
		SanitizeImportedCard(card)
		if card.ApprovalState != pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_DRAFT {
			t.Fatalf("approval_state = %v, want DRAFT written explicitly (UNKNOWN relies on a "+
				"default owned by the write layer)", card.ApprovalState)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		card := approvedReleasedCard(t)
		SanitizeImportedCard(card)
		once := proto.Clone(card).(*pb_common.TechCardInsert)
		SanitizeImportedCard(card)
		if !proto.Equal(card, once) {
			t.Fatalf("second pass changed the card")
		}
	})

	t.Run("nil is safe", func(t *testing.T) {
		SanitizeImportedCard(nil) // card.json absent, or TechCard.TechCard unset
		var typed *pb_common.TechCardInsert
		SanitizeImportedCard(typed)
	})

	t.Run("every approval state above draft lands on draft", func(t *testing.T) {
		vals := pb_common.TechCardApprovalState(0).Descriptor().Values()
		for i := 0; i < vals.Len(); i++ {
			st := pb_common.TechCardApprovalState(vals.Get(i).Number())
			card := &pb_common.TechCardInsert{ApprovalState: st}
			SanitizeImportedCard(card)
			if card.ApprovalState != pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_DRAFT {
				t.Errorf("%v sanitised to %v, want DRAFT", st, card.ApprovalState)
			}
		}
	})
}

// TestSanitizeImportedCardFromJSON runs the sanitiser over a card decoded the way the
// import actually decodes one — protojson into TechCardInsert — from a card.json that our
// exporter would never write. This is the archive the phase is defended against: hand-made
// or built by another tool, carrying an APPROVED sign-off set and a release stamp.
//
// It matters that this is a separate case from the struct-built one. Building the struct in
// Go proves the function clears fields; decoding proves the fields SURVIVE the wire into the
// shape the function is given, i.e. that there is really something here to clear.
func TestSanitizeImportedCardFromJSON(t *testing.T) {
	const hostile = `{
	  "styleNumber": "FW26-0007",
	  "name": "coat",
	  "approvalState": "TECH_CARD_APPROVAL_STATE_RELEASED",
	  "releasedAt": "2026-03-04T05:06:07Z",
	  "approvedAt": "2026-03-04T05:06:07Z",
	  "status": "signed off by the factory",
	  "signoffs": [
	    {"section": "TECH_CARD_SIGNOFF_SECTION_DESIGN",
	     "state": "TECH_CARD_SIGNOFF_STATE_APPROVED",
	     "signedBy": "not a user of this database",
	     "signedAt": "2026-03-04T05:06:07Z",
	     "signedDigest": "cafebabe"},
	    {"section": "TECH_CARD_SIGNOFF_SECTION_COSTING",
	     "state": "TECH_CARD_SIGNOFF_STATE_APPROVED",
	     "signedBy": "nor this one",
	     "signedAt": "2026-03-04T05:06:07Z",
	     "signedDigest": "deadbeef"}
	  ]
	}`

	var card pb_common.TechCardInsert
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(hostile), &card); err != nil {
		t.Fatalf("decode hostile card.json: %v", err)
	}
	// Positive control: the decode really did produce the marks, so a green result below is
	// the sanitiser working rather than the payload having been empty all along.
	if card.ApprovalState != pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED ||
		card.ReleasedAt == nil || card.ApprovedAt == nil || len(card.Signoffs) != 2 {
		t.Fatalf("precondition: hostile card.json did not decode into approval marks: %v", &card)
	}

	SanitizeImportedCard(&card)

	assertNoApprovalMarks(t, &card)
	if card.ReleasedAt != nil || card.ApprovedAt != nil || len(card.Signoffs) != 0 {
		t.Errorf("hand-made archive kept its marks: released_at=%v approved_at=%v signoffs=%d",
			card.ReleasedAt, card.ApprovedAt, len(card.Signoffs))
	}
	if card.StyleNumber != "FW26-0007" || card.Status != "signed off by the factory" {
		t.Errorf("sanitiser damaged non-approval content: %v", &card)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// The field-list guard.
//
// ApprovalFieldNames is four names typed by hand against a contract of several hundred
// fields that grows every phase. Reading it with one's eyes was how it was assembled and
// is not how it stays true. The guard walks the DESCRIPTORS of everything reachable from
// TechCardInsert, raises every field whose name looks like an approval mark, and requires
// each raised name to be either in the list or in an exclusion carrying a written reason.
// Both directions: a dead list entry and a stale exclusion fail too.
//
// Descriptors, not values, for the reason the money guard gives: protoreflect.Range does
// not enter unset fields, so a walk over a value can only see what a test happened to fill.
// ─────────────────────────────────────────────────────────────────────────────

// sanitizeGuardSubstrings is what "looks like an approval mark" means. Deliberately wider
// than the four names: the point is to catch a name nobody has thought of yet.
var sanitizeGuardSubstrings = []string{
	"approv",  // approval_state, approved_at, rounds_to_approval
	"sign",    // signoffs, signed_by, signed_at, signed_digest — and design/assigned, excluded below
	"releas",  // released_at, released_by, release_number
	"ratif",   // ratification (R7 vocabulary; nothing on the card yet)
	"digest",  // signed_digest, section digests
	"attest",  // none today
	"certif",  // none today
	"endors",  // none today
	"authori", // authorised_by-shaped names
	"frozen",  // a released card is frozen for edits; a "frozen" flag would be the same claim
}

// sanitizeGuardExclusions are names the guard raises that were DECIDED not to belong to the
// approval family. The written reason is the entry's whole purpose — an exclusion without
// one is the same unchecked list under a different name. A stale exclusion (a name no longer
// raised) fails this test, so the file cannot accumulate them.
//
// EMPTY, and measured rather than assumed. The walk reaches 269 distinct field names from
// TechCardInsert and the substring list above raises exactly seven of them: the four in
// ApprovalFieldNames, plus signed_by / signed_at / signed_digest, which are reachable ONLY
// under `signoffs` and are therefore cleared with it. Nothing else in the write contract is
// approval-shaped — `designer`, `assigned_by` and the revision log (`released_by`,
// `release_number`) that the `sign`/`releas` substrings would catch all live on the read-side
// TechCard message or were removed from the contract. So the four names are not a sample of
// the approval family; they are the whole of it. If this map ever stops being empty, the entry
// is a decision somebody made, in writing, and the test holds them to it.
var sanitizeGuardExclusions = map[string]string{}

// sanitizeGuardRoot is TechCardInsert and only TechCardInsert. The sanitiser's argument is
// the WRITE contract, and that is the correct and sufficient root: the read-side approval
// projections (TechCard.revisions with its released_by/release_number, TechCard.section_digests,
// TechCard.lock_version) live on the outer TechCard message, are output-only, and never reach
// an insert — a fact this file asserts structurally in TestSanitizeFieldGuard rather than
// assuming, because "unreachable" is what makes them somebody else's problem.
func sanitizeGuardRoot() protoreflect.MessageDescriptor {
	return (&pb_common.TechCardInsert{}).ProtoReflect().Descriptor()
}

// walkSanitizeDescriptors returns every field name reachable from TechCardInsert, and the
// subset reachable WITHOUT passing through a field that ApprovalFieldNames clears whole.
//
// The two-state walk models RedactFieldsDeep's actual semantics: a matched field is cleared
// and not descended into, so signed_by / signed_at / signed_digest under `signoffs` are
// structurally out of reach and need no exclusion. Move one of them out from under signoffs
// and it becomes exposed — and this test goes red, which is the whole point.
func walkSanitizeDescriptors() (all map[string]bool, exposed map[string][]string) {
	all = map[string]bool{}
	exposed = map[string][]string{}

	type state struct {
		md      protoreflect.MessageDescriptor
		cleared bool
		path    string
	}
	seen := map[string]bool{}
	root := sanitizeGuardRoot()
	queue := []state{{md: root, path: string(root.Name())}}
	for len(queue) > 0 {
		st := queue[0]
		queue = queue[1:]
		key := fmt.Sprintf("%s|%t", st.md.FullName(), st.cleared)
		if seen[key] {
			continue
		}
		seen[key] = true

		fields := st.md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			name := string(fd.Name())
			path := st.path + "." + name
			all[name] = true
			if !st.cleared {
				exposed[name] = append(exposed[name], path)
			}
			var child protoreflect.MessageDescriptor
			switch {
			case fd.IsMap():
				if isMessageValueMap(fd) {
					child = fd.MapValue().Message()
				}
			case isMessageKind(fd):
				child = fd.Message()
			}
			if child != nil {
				queue = append(queue, state{md: child, cleared: st.cleared || ApprovalFieldNames[name], path: path})
			}
		}
	}
	return all, exposed
}

func looksLikeApproval(name string) bool {
	for _, s := range sanitizeGuardSubstrings {
		if strings.Contains(name, s) {
			return true
		}
	}
	return false
}

func TestSanitizeFieldGuard(t *testing.T) {
	all, exposed := walkSanitizeDescriptors()
	if len(all) < 200 {
		t.Fatalf("descriptor walk reached only %d field names — the walk is broken, not the contract", len(all))
	}

	// Direction 1: every approval-shaped name that a card can actually carry is either
	// cleared or explicitly excluded with a reason.
	var uncovered []string
	for name, paths := range exposed {
		if !looksLikeApproval(name) || ApprovalFieldNames[name] {
			continue
		}
		if _, ok := sanitizeGuardExclusions[name]; ok {
			continue
		}
		uncovered = append(uncovered, fmt.Sprintf("%s (at %s)", name, strings.Join(paths, ", ")))
	}
	if len(uncovered) != 0 {
		sort.Strings(uncovered)
		t.Errorf("approval-shaped fields covered by neither ApprovalFieldNames nor an exclusion:\n  %s\n"+
			"Decide each: clear it in sanitize.go, or exclude it there with the reason it is not an approval.",
			strings.Join(uncovered, "\n  "))
	}

	// Direction 2: no dead entry. A name in ApprovalFieldNames that the contract no longer
	// has is a clearing that stopped happening — silently, and forever.
	for name := range ApprovalFieldNames {
		if !all[name] {
			t.Errorf("ApprovalFieldNames has %q, which no longer exists under TechCardInsert", name)
		}
	}

	// Direction 3: no stale exclusion. An exclusion the walk no longer raises is a decision
	// about a field that is gone, and the next reader will trust it.
	for name, why := range sanitizeGuardExclusions {
		if why == "" {
			t.Errorf("exclusion %q carries no reason", name)
		}
		if len(exposed[name]) == 0 {
			t.Errorf("exclusion %q is stale: the guard no longer raises that name", name)
		}
	}

	// Direction 4: the sign-off leaves really are unreachable except through `signoffs`.
	// This is what lets the exclusions list stay empty of them, so if the contract ever grows
	// a second door to a signature the guard must not keep quiet about it.
	for _, leaf := range []string{"signed_by", "signed_at", "signed_digest"} {
		if !all[leaf] {
			t.Errorf("%q is gone from the contract — TechCardSignoff changed shape; re-read this guard", leaf)
			continue
		}
		if paths := exposed[leaf]; len(paths) != 0 {
			t.Errorf("%q is reachable outside `signoffs` (at %s): clearing signoffs no longer removes it",
				leaf, strings.Join(paths, ", "))
		}
	}

	// Direction 5: the read-side approval projections are NOT reachable from the write
	// contract. The sanitiser's signature takes TechCardInsert; that is only sufficient while
	// this holds.
	for _, readOnly := range []string{"revisions", "section_digests", "released_by", "release_number", "lock_version"} {
		if paths := exposed[readOnly]; len(paths) != 0 {
			t.Errorf("read-only approval projection %q is reachable from TechCardInsert (at %s): "+
				"the sanitiser's root is no longer sufficient", readOnly, strings.Join(paths, ", "))
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestApprovalMarksStayInsideTheInsertHalf — the OTHER half of the guard above.
//
// TestSanitizeFieldGuard roots at TechCardInsert, because that is SanitizeImportedCard's
// argument. That leaves one thing assumed rather than asserted, and sanitizeGuardRoot's own
// comment names it: the read-side approval projections "live on the outer TechCard message
// … and never reach an insert". TRUE TODAY, MEASURED, AND HELD UP BY NOTHING — the lists
// guard themselves, but no test guards WHERE the names are reachable from.
//
// It stopped being an academic gap when the import resolver started reading the outer
// message. Ф2.3 lifts the style's catalogue facts and the measured piece areas off
// TechCard directly (techcard_archive_resolve.go, section 13), and SanitizeImportedCard
// cannot cover that half: its parameter is *TechCardInsert, so the outer message is not
// something it declines to clean — it is something it cannot be handed. The money denylist
// had exactly this shape, and there it was not zero: seven live paths under `colorways`,
// which is why the resolver now redacts money over the whole card message. Approval is zero
// only by luck of where the contract put things.
//
// So: the day somebody hangs an `approval_state` on AdminColorwayRef or on
// TechCardOutputVariant, this test goes red and NAMES THE PATH. Whoever breaks it will read
// it a year from now with no memory of this conversation, so the failure has to say where.
// ─────────────────────────────────────────────────────────────────────────────

// walkOuterCardReach returns, for every name in names, the paths by which it is reachable
// from the OUTER TechCard message WITHOUT going through its `tech_card` field.
//
// Its own walk rather than walkSanitizeDescriptors with a parameter: that one answers "what
// can an insert carry", keyed on a cleared/not-cleared state that has no meaning out here,
// and bending it into answering two questions would make one test's red ambiguous.
//
// A MATCHED FIELD IS RECORDED AND NOT DESCENDED INTO — the same stop rule RedactFieldsDeep
// applies, because a guard that walks differently from the production code measures a
// contract nobody enforces. `seen` is keyed by message type: a type's field names are the
// same however it was reached, so skipping a second visit cannot lose a NAME. It can lose a
// second PATH to a name already found, which is why the message says "at" and not "only at".
func walkOuterCardReach(names map[string]bool) (hits map[string][]string, reached int) {
	hits = map[string][]string{}

	type state struct {
		md   protoreflect.MessageDescriptor
		path string
	}
	root := (&pb_common.TechCard{}).ProtoReflect().Descriptor()
	seen := map[string]bool{string(root.FullName()): true}
	fieldNames := map[string]bool{}
	queue := []state{{md: root, path: string(root.Name())}}

	for len(queue) > 0 {
		st := queue[0]
		queue = queue[1:]

		fields := st.md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			name := string(fd.Name())
			path := st.path + "." + name

			// THE ONE FIELD THIS WALK DOES NOT ENTER. TechCard.tech_card is the writable half,
			// it is what SanitizeImportedCard is handed, and TestSanitizeFieldGuard covers it
			// in full. Everything this walk reaches is by definition the half nothing cleans.
			if st.md.FullName() == root.FullName() && name == "tech_card" {
				continue
			}

			fieldNames[name] = true
			if names[name] {
				hits[name] = append(hits[name], path)
				continue // cleared whole and not descended into — RedactFieldsDeep's rule
			}

			var child protoreflect.MessageDescriptor
			switch {
			case fd.IsMap():
				if isMessageValueMap(fd) {
					child = fd.MapValue().Message()
				}
			case isMessageKind(fd):
				child = fd.Message()
			}
			if child == nil || seen[string(child.FullName())] {
				continue
			}
			seen[string(child.FullName())] = true
			queue = append(queue, state{md: child, path: path})
		}
	}
	return hits, len(fieldNames)
}

func TestApprovalMarksStayInsideTheInsertHalf(t *testing.T) {
	approval, reached := walkOuterCardReach(ApprovalFieldNames)

	// POSITIVE CONTROL 1 — the walk went somewhere. An emptiness assertion is exactly the
	// shape that reads green when the instrument is dead, and "no approval marks out here"
	// is what a walk that visited nothing also says.
	if reached < 100 {
		t.Fatalf("the walk reached only %d field names outside TechCard.tech_card — the walk is broken, not the contract", reached)
	}

	// POSITIVE CONTROL 2 — and it went somewhere THAT MATTERS. The region this test exists to
	// watch is the outer message's own subtrees, so the instrument has to be shown reaching
	// into one. Money is the proof by counterexample: run the SAME walk over the money
	// denylist and it finds the very paths that made widening the resolver's redaction
	// necessary. A walk that cannot see cost_price under `colorways` could not see an
	// approval_state hung there either.
	money, _ := walkOuterCardReach(MoneyFieldNamesArchive)
	if len(money["cost_price"]) == 0 {
		t.Fatalf("the walk does not reach cost_price under colorways — it cannot be trusted to report "+
			"an approval mark hung in the same place (money names it did reach out here: %v)",
			approvalSortedNames(sanitizeNameSet(money)))
	}

	// THE ASSERTION.
	if len(approval) == 0 {
		return
	}
	var lines []string
	for name, paths := range approval {
		sort.Strings(paths)
		lines = append(lines, fmt.Sprintf("%s at %s", name, strings.Join(paths, ", ")))
	}
	sort.Strings(lines)
	t.Errorf("approval marks are now reachable from the OUTER TechCard message, outside its tech_card half:\n  %s\n\n"+
		"SanitizeImportedCard cannot clear these: its parameter is *TechCardInsert, so it is never handed the "+
		"message these live on. Ф2.3 (internal/apisrv/admin/techcard_archive_resolve.go) reads that outer half for "+
		"the style's catalogue facts and the measured piece areas, so an archive can now carry a forged approval "+
		"mark into a branch nothing sanitises — the same shape the money denylist had, where it was seven live "+
		"paths under `colorways`.\n"+
		"Fix it the way money was fixed: clear the approval family over the WHOLE card message at the point Ф2.3 "+
		"sanitises it, next to the RedactFieldsDeep(card.ProtoReflect(), MoneyFieldNamesArchive) call.",
		strings.Join(lines, "\n  "))
}

// sanitizeNameSet reduces a reach map to the set of names it found, for the control's message.
func sanitizeNameSet(hits map[string][]string) map[string]bool {
	out := make(map[string]bool, len(hits))
	for name := range hits {
		out[name] = true
	}
	return out
}
