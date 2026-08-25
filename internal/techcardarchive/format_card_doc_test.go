package techcardarchive

import (
	"os"
	"regexp"
	"strings"
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ─────────────────────────────────────────────────────────────────────────────
// §4.1 IS A LEDGER, AND A LEDGER WITH A MISSING ROW IS WORSE THAN NO LEDGER.
//
// card.json is protojson of the OUTER common.TechCard, and that message grows: piece_area_scopes,
// markers and output_variants were all added to it long after the archive was designed, and each
// arrived in card.json without anyone deciding it should. `on_hand` — the exporting warehouse's
// stock balance — travelled out of the building that way.
//
// §4.1 answers the question field by field, which only works while it answers it for EVERY field.
// A section that silently stops covering the message reads exactly like one that covers it, so the
// gap is measured here rather than trusted: the descriptor is the authority on what the message
// holds, the table is the authority on what the format promises, and they must name the same set.
//
// Same shape as the §7 reason-table check in report_test.go, and for the same reason.
// ─────────────────────────────────────────────────────────────────────────────

// formatCardFieldRe pulls the backticked names out of the SECOND column of a §4.1 row. One row may
// name several fields (7/8/28 share a verdict), and the verdict column is never read — it is full
// of backticked names belonging to other messages.
var formatCardFieldRe = regexp.MustCompile("`([a-z0-9_]+)`")

func TestFormatDocumentsEveryOuterCardField(t *testing.T) {
	fields := (&pb_common.TechCard{}).ProtoReflect().Descriptor().Fields()
	inMessage := make(map[string]bool, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		inMessage[string(fields.Get(i).Name())] = true
	}
	if len(inMessage) < 20 {
		t.Fatalf("common.TechCard reflected to %d fields — the descriptor was not read, so every "+
			"comparison below would pass against anything", len(inMessage))
	}

	documented := formatCardLedgerRows(t, formatDocPath)

	for name := range inMessage {
		if !documented[name] {
			t.Errorf("common.TechCard carries %q and FORMAT.md §4.1 says nothing about it. Every "+
				"field of the outer message travels in card.json unless something removes it, so an "+
				"undocumented field is a field nobody decided to ship. Add the row — travels / "+
				"written / remapped / cleared — and cut the field in sanitizeCardForArchive if the "+
				"honest verdict is «cleared»", name)
		}
	}
	for name := range documented {
		if !inMessage[name] {
			t.Errorf("FORMAT.md §4.1 has a row for %q, which is not a field of common.TechCard any "+
				"more — a renamed field costs a MAJOR (§3), and a dropped one costs this row", name)
		}
	}
}

// formatCardLedgerRows reads the field column of the §4.1 table. Every way the read can go wrong is
// a failure and never an empty set: an empty set turns the loops above into "nothing is missing".
func formatCardLedgerRows(t *testing.T, path string) map[string]bool {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — the outer-message ledger lives there and this test compares against it", path, err)
	}
	doc := string(body)

	start := strings.Index(doc, "### 4.1 `card.json`: the outer message")
	if start < 0 {
		t.Fatalf("%s no longer has a '### 4.1 `card.json`: the outer message' heading; find where "+
			"the ledger went and re-point this test (do not delete the check)", path)
	}
	section := doc[start:]
	if end := strings.Index(section, "\n### 4.2"); end >= 0 {
		section = section[:end]
	} else {
		t.Fatalf("%s §4.1 has no '### 4.2' after it; this test would otherwise read the whole rest "+
			"of the document as the ledger and pass on names from every other section", path)
	}

	out := map[string]bool{}
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		cols := strings.Split(line, "|")
		// "", " # ", " field ", " verdict ", "" — anything shorter is not a row of this table.
		if len(cols) < 4 {
			continue
		}
		for _, m := range formatCardFieldRe.FindAllStringSubmatch(cols[2], -1) {
			out[m[1]] = true
		}
	}
	if len(out) < 20 {
		t.Fatalf("%s §4.1 parsed to %d field rows (%s) — the table was not read, so the comparison "+
			"would pass against anything", path, len(out), strings.Join(reportSortedKeys(out), ", "))
	}
	return out
}
