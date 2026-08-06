package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestFabricPurposeBinding covers 0267 — выкройка and cut-piece alias bind to a НАЗНАЧЕНИЕ instead
// of to one BOM line, ADDITIVELY. The three cases below are the acceptance test of the transition,
// not a formality:
//
//	(a) a card where NO line has a purpose behaves EXACTLY as today;
//	(b) a card MID-SORT — some lines sorted, some not — which is the real state for the whole
//	    transition and the one that usually breaks;
//	(c) the alias-collapse case: two lines of one purpose that held same-named blocks.
//
// The migration itself cannot collide (every pre-existing row has fabric_purpose NULL, so its
// scope_key EQUALS its old bom_line_key and the new unique index reproduces the old one row for
// row); TestFabricPurposeScopeIndex below pins that equivalence directly against the schema.
func TestFabricPurposeBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	var szA int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&szA))

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	// present(v) is proto PRESENCE, not emptiness: Valid=true with "" is an explicit clear, which is
	// a different instruction from the zero value (absent → carry the stored value forward).
	present := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	const (
		lineShell  = "01FPLINESHELL00000000000A1" // «основная ткань», article 1
		lineShell2 = "01FPLINESHELL20000000000A2" // «основная ткань», article 2 — SAME purpose
		linePocket = "01FPLINEPOCKET0000000000A3" // карманка, left unsorted for the mid-sort case
		pcFront    = "01FPPIECEFRONT0000000000P1"
		pcPocket   = "01FPPIECEPOCKET000000000P2"
		sheetA     = "01FPSHEETA00000000000000S1"
		sheetB     = "01FPSHEETB00000000000000S2"
		urlBase    = "https://cdn.example/base/tech-card-patterns/2026/august/"
	)
	// The card starts entirely UNSORTED — exactly the state every card on prod is in, because 0265
	// deliberately back-filled nothing.
	unsortedBom := []entity.TechCardBomItem{
		{LineKey: lineShell, Section: entity.BomSectionFabric, Name: "Твил 1"},
		{LineKey: lineShell2, Section: entity.BomSectionFabric, Name: "Твил 2"},
		{LineKey: linePocket, Section: entity.BomSectionFabric, Name: "Карманка"},
	}
	// Mid-sort: the two shells are sorted into ONE назначение, the pocketing line is left alone.
	// This is (b), and it is also the setup (c) needs.
	midSortBom := []entity.TechCardBomItem{
		{LineKey: lineShell, Section: entity.BomSectionFabric, Name: "Твил 1", Purpose: ns(string(entity.BomPurposeMain))},
		{LineKey: lineShell2, Section: entity.BomSectionFabric, Name: "Твил 2", Purpose: ns(string(entity.BomPurposeMain))},
		{LineKey: linePocket, Section: entity.BomSectionFabric, Name: "Карманка"},
	}
	pieces := []entity.TechCardPiece{
		{LineKey: pcFront, Name: "перед", PiecesPerGarment: 1, Grainline: "lengthwise"},
		{LineKey: pcPocket, Name: "мешковина кармана", PiecesPerGarment: 2, Grainline: "lengthwise"},
	}
	card := func(bom []entity.TechCardBomItem, patterns []entity.TechCardSizePattern,
		aliasSet bool, aliases []entity.TechCardPieceDxfAlias) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "Fabric Purpose", StyleNumber: ns("FP-1"),
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit:    entity.TechCardUnitMm,
			SizeIds:            []int{szA},
			BomItems:           bom,
			Pieces:             pieces,
			Patterns:           patterns,
			PieceDxfAliases:    aliases,
			PieceDxfAliasesSet: aliasSet,
		}
	}

	tcID, err := T.AddTechCard(ctx, card(unsortedBom, nil, false, nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})
	lock := 0
	resave := func(bom []entity.TechCardBomItem, patterns []entity.TechCardSizePattern,
		aliasSet bool, aliases []entity.TechCardPieceDxfAlias) error {
		err := T.UpdateTechCard(ctx, tcID, card(bom, patterns, aliasSet, aliases), lock)
		if err == nil {
			lock++
		}
		return err
	}
	read := func() *entity.TechCard {
		c, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		return c
	}
	aliasBy := func(scope, block string) (entity.TechCardPieceDxfAlias, bool) {
		for _, a := range read().PieceDxfAliases {
			if a.ScopeKey() == scope && a.BlockName == block {
				return a, true
			}
		}
		return entity.TechCardPieceDxfAlias{}, false
	}
	patternBy := func(lineKey string) (entity.TechCardSizePattern, bool) {
		for _, p := range read().Patterns {
			if p.LineKey == lineKey {
				return p, true
			}
		}
		return entity.TechCardSizePattern{}, false
	}

	// Patterns are a FULL-REPLACE child: every save states the whole set, so each call below passes
	// both sheets and varies only the two presence-gated binding fields.
	pat := func(lineKey, file string, bind, purpose sql.NullString) entity.TechCardSizePattern {
		return entity.TechCardSizePattern{
			SizeId: szA, URL: urlBase + file, LineKey: lineKey,
			BomLineKey: bind, FabricPurpose: purpose,
		}
	}
	absent := sql.NullString{}
	boundA := pat(sheetA, "fp-shell.dxf", present(lineShell), absent)
	boundB := pat(sheetB, "fp-pocket.dxf", present(linePocket), absent)
	// «bare» = the save a client that predates the field sends: the row is there, the binding fields
	// are not spoken at all.
	bareA := pat(sheetA, "fp-shell.dxf", absent, absent)
	bareB := pat(sheetB, "fp-pocket.dxf", absent, absent)

	// ── (a) an UNSORTED card behaves EXACTLY as today ────────────────────────────────────────
	// Nothing in this block mentions a purpose. Upload (patterns bound to lines), matching (aliases
	// scoped to lines) and saving all have to work byte-for-byte as they did before 0267 — including
	// the property the whole feature rests on today: the SAME generic block name on two DIFFERENT
	// lines maps to two DIFFERENT pieces.
	require.NoError(t, resave(unsortedBom,
		[]entity.TechCardSizePattern{boundA, boundB},
		true, []entity.TechCardPieceDxfAlias{
			{BomLineKey: lineShell, BlockName: "деталь_1", PieceLineKey: pcFront},
			{BomLineKey: linePocket, BlockName: "деталь_1", PieceLineKey: pcPocket},
		}))
	shellSheet, ok := patternBy(sheetA)
	require.True(t, ok)
	require.Equal(t, lineShell, shellSheet.BomLineKey.String, "(a) line binding round-trips")
	require.False(t, shellSheet.FabricPurpose.Valid, "(a) nothing invents a purpose")
	aShell, ok := aliasBy(lineShell, "деталь_1")
	require.True(t, ok, "(a) alias still scopes by line")
	require.Equal(t, pcFront, aShell.PieceLineKey)
	require.Empty(t, aShell.FabricPurpose, "(a) nothing invents a purpose")
	aPocket, ok := aliasBy(linePocket, "деталь_1")
	require.True(t, ok, "(a) the same block name on another line is a SEPARATE alias, as today")
	require.Equal(t, pcPocket, aPocket.PieceLineKey)

	// A pattern bound to a line that is not roll goods is still refused, and an unbound sheet is
	// still legal — the pre-0267 rules, unchanged.
	require.Error(t, resave(unsortedBom,
		[]entity.TechCardSizePattern{pat(sheetA, "fp-shell.dxf", present("01FPNOSUCHLINE0000000000ZZ"), absent), boundB},
		false, nil), "(a) an unknown line is still refused")

	// ── (b) the card MID-SORT ────────────────────────────────────────────────────────────────
	// Both shells are sorted into MAIN; the pocketing line stays unsorted. Nothing was migrated by
	// the sort itself: the two aliases above are still line-scoped and still resolve. That is the
	// whole point of the additive shape — the operator sorted the BOM and lost nothing.
	require.NoError(t, resave(midSortBom, []entity.TechCardSizePattern{boundA, boundB}, false, nil))
	aShell, ok = aliasBy(lineShell, "деталь_1")
	require.True(t, ok, "(b) sorting the BOM does not move or drop a line-scoped alias")
	require.Equal(t, pcFront, aShell.PieceLineKey)
	_, ok = aliasBy(linePocket, "деталь_1")
	require.True(t, ok, "(b) the unsorted line's alias is untouched too")

	// A stale client saves the card without ever speaking either binding field. Both halves are
	// presence-gated, so nothing it never saw may be wiped.
	require.NoError(t, resave(midSortBom, []entity.TechCardSizePattern{bareA, bareB}, false, nil))
	shellSheet, _ = patternBy(sheetA)
	require.Equal(t, lineShell, shellSheet.BomLineKey.String, "(b) absent bom_line_key carries the stored binding")

	// Now a NEW client rebinds the shell sheet to the назначение and records the legacy line
	// alongside it. A purpose-scoped sheet and a line-scoped sheet coexist on ONE card — that IS
	// mid-sort, and there is no migration step between them.
	require.NoError(t, resave(midSortBom, []entity.TechCardSizePattern{
		pat(sheetA, "fp-shell.dxf", present(lineShell), present(string(entity.BomPurposeMain))),
		boundB,
	}, false, nil))
	shellSheet, _ = patternBy(sheetA)
	require.Equal(t, string(entity.BomPurposeMain), shellSheet.FabricPurpose.String)
	require.Equal(t, lineShell, shellSheet.BomLineKey.String, "(b) the legacy half survives as compatibility")
	pocketSheet, _ := patternBy(sheetB)
	require.False(t, pocketSheet.FabricPurpose.Valid, "(b) the unsorted sheet stays line-bound")
	require.Equal(t, linePocket, pocketSheet.BomLineKey.String)

	// Presence again, now that a purpose IS stored: absent must not clear it, present-empty must.
	require.NoError(t, resave(midSortBom, []entity.TechCardSizePattern{bareA, bareB}, false, nil))
	shellSheet, _ = patternBy(sheetA)
	require.Equal(t, string(entity.BomPurposeMain), shellSheet.FabricPurpose.String,
		"(b) an ABSENT fabric_purpose carries the stored value — a stale client cannot wipe it")
	require.NoError(t, resave(midSortBom, []entity.TechCardSizePattern{
		pat(sheetA, "fp-shell.dxf", absent, present("")), bareB,
	}, false, nil))
	shellSheet, _ = patternBy(sheetA)
	require.False(t, shellSheet.FabricPurpose.Valid, "(b) present-empty is an explicit unbind")
	require.Equal(t, lineShell, shellSheet.BomLineKey.String,
		"(b) clearing one half leaves the other exactly where it was")

	// A purpose no cloth line of this card carries is refused on a CHANGE — the same rule the line
	// half has always had, phrased for the other control.
	err = resave(midSortBom, []entity.TechCardSizePattern{
		pat(sheetA, "fp-shell.dxf", absent, present(string(entity.BomPurposeInsulation))), bareB,
	}, false, nil)
	require.Error(t, err, "(b) no line is sorted as утеплитель, so nothing can bind to it")

	// ── (c) the alias collapse ───────────────────────────────────────────────────────────────
	// Both shells share назначение MAIN. Line-scoped, their same-named blocks are two legal aliases
	// (that is (a)). Purpose-scoped, they are ONE key — and the server must refuse readably rather
	// than 500 the whole card save on a driver duplicate.
	require.NoError(t, resave(midSortBom, []entity.TechCardSizePattern{bareA, bareB}, true,
		[]entity.TechCardPieceDxfAlias{
			{BomLineKey: lineShell, BlockName: "полочка", PieceLineKey: pcFront},
			{BomLineKey: lineShell2, BlockName: "полочка", PieceLineKey: pcPocket},
		}))
	require.Len(t, read().PieceDxfAliases, 2, "(c) line-scoped, both survive — this is today's behaviour")

	err = resave(midSortBom, []entity.TechCardSizePattern{bareA, bareB}, true,
		[]entity.TechCardPieceDxfAlias{
			{FabricPurpose: string(entity.BomPurposeMain), BomLineKey: lineShell, BlockName: "полочка", PieceLineKey: pcFront},
			{FabricPurpose: string(entity.BomPurposeMain), BomLineKey: lineShell2, BlockName: "полочка", PieceLineKey: pcPocket},
		})
	require.Error(t, err, "(c) two pieces claiming one block under one назначение must not be written")
	var fv *entity.ValidationError
	require.ErrorAs(t, err, &fv, "(c) it is a readable field violation, not a raw driver error")
	require.NotEmpty(t, fv.HowToFix, "(c) the message must tell the operator what to do")

	// The refusal left the stored set intact — a failed save is a no-op, not a half-applied one.
	require.Len(t, read().PieceDxfAliases, 2, "(c) the rejected save changed nothing")

	// And the RESOLUTION of the collapse — the operator keeps ONE alias for the block — is accepted,
	// migrating the pair onto the purpose scope in a single save. bom_line_key goes empty because
	// the назначение owns two lines and there is no single honest line to record.
	require.NoError(t, resave(midSortBom, []entity.TechCardSizePattern{bareA, bareB}, true,
		[]entity.TechCardPieceDxfAlias{
			{FabricPurpose: string(entity.BomPurposeMain), BlockName: "полочка", PieceLineKey: pcFront},
		}))
	got := read().PieceDxfAliases
	require.Len(t, got, 1)
	require.Equal(t, string(entity.BomPurposeMain), got[0].FabricPurpose)
	require.Equal(t, string(entity.BomPurposeMain), got[0].ScopeKey())
	require.Empty(t, got[0].BomLineKey)
	require.Equal(t, pcFront, got[0].PieceLineKey)
}

// TestFabricPurposeScopeIndex pins the schema half of 0267 against the DB itself: the generated
// scope column, the unique index that replaced (card, line, block), and — the claim the whole
// migration rests on — that with fabric_purpose NULL the new index accepts and rejects EXACTLY what
// the old one did. Every row that existed before 0267 is in that state, which is why the migration
// could not collide on its own data.
func TestFabricPurposeScopeIndex(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	var szA int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&szA))

	count := func(q string, args ...any) int {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx, q, args...).Scan(&n))
		return n
	}
	require.Equal(t, 0, count(`SELECT COUNT(*) FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece_dxf_block'
		  AND INDEX_NAME = 'uniq_tcpdb_card_slot_block'`), "the line-scoped unique is gone")
	require.Greater(t, count(`SELECT COUNT(*) FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece_dxf_block'
		  AND INDEX_NAME = 'uniq_tcpdb_card_scope_block'`), 0, "the scope-scoped unique is there")
	// Dropping the old index must not have taken the FK's supporting index with it (errno 1553 is
	// what a wrong step order looks like, and it halts a prod boot).
	require.Greater(t, count(`SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece_dxf_block'
		  AND CONSTRAINT_NAME = 'fk_tcpdb_card'`), 0, "the card FK survived the index swap")

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	const (
		lineA   = "01FPIDXLINEA000000000000B1"
		lineB   = "01FPIDXLINEB000000000000B2"
		pcOne   = "01FPIDXPIECE000000000000Q1"
		blockNm = "деталь_1"
	)
	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Scope Index", StyleNumber: ns("FPX-1"),
		Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		SizeIds:         []int{szA},
		BomItems: []entity.TechCardBomItem{
			{LineKey: lineA, Section: entity.BomSectionFabric, Name: "A"},
			{LineKey: lineB, Section: entity.BomSectionFabric, Name: "B"},
		},
		Pieces: []entity.TechCardPiece{{LineKey: pcOne, Name: "перед", PiecesPerGarment: 1, Grainline: "lengthwise"}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})
	var pieceID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM tech_card_piece WHERE tech_card_id = ? LIMIT 1", tcID).Scan(&pieceID))

	ins := func(line, purpose string) error {
		_, err := testDB.ExecContext(ctx, `INSERT INTO tech_card_piece_dxf_block
			(tech_card_id, bom_line_key, fabric_purpose, block_name, piece_id)
			VALUES (?, ?, ?, ?, ?)`, tcID, line, sql.NullString{String: purpose, Valid: purpose != ""}, blockNm, pieceID)
		return err
	}
	// Purpose NULL — the state of every pre-0267 row. scope_key IS the line, so the index behaves
	// exactly like uniq_tcpdb_card_slot_block did: same line + same block collides, different lines
	// do not. This is the "no collision can occur during the migration itself" claim, tested.
	require.NoError(t, ins(lineA, ""))
	require.NoError(t, ins(lineB, ""), "two lines, one block name — accepted before 0267 and still accepted")
	require.Error(t, ins(lineA, ""), "same line, same block — rejected before 0267 and still rejected")

	var scope string
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT scope_key FROM tech_card_piece_dxf_block WHERE tech_card_id = ? AND bom_line_key = ?",
		tcID, lineA).Scan(&scope))
	require.Equal(t, lineA, scope, "with no purpose the scope IS the legacy line")
	require.Equal(t, entity.FabricScopeKey("", lineA), scope, "…and Go computes the same value the column does")

	// Sorting both lines into one назначение is what makes them collide — later, as a live edit, not
	// during the migration.
	require.Error(t, func() error {
		_, err := testDB.ExecContext(ctx, `UPDATE tech_card_piece_dxf_block SET fabric_purpose = 'main'
			WHERE tech_card_id = ?`, tcID)
		return err
	}(), "collapsing two line scopes onto one purpose is what the unique index now catches")

	// A row naming neither a purpose nor a line would scope to '' and pool with every other such
	// row of the card; chk_tcpdb_scope_present is what stops that at the schema.
	require.Error(t, ins("", ""), "a scopeless alias is refused by the CHECK")
	// The closed vocabulary is enforced byte-wise, 0265's rule: 'MAIN' would group with nothing.
	require.Error(t, ins(lineA, "MAIN"), "a non-lowercase назначение is refused")
	require.Error(t, ins(lineA, "нечто"), "a назначение outside the closed list is refused")
}

// TestFabricPurposeMigrationReplay runs 0267 Down → Up → Up against the live schema.
//
// CLAUDE.md makes idempotency a hard requirement for a reason: MySQL 8 auto-commits DDL, so a
// mid-file failure leaves the schema half-applied with NO gorp_migrations row, and the next boot
// re-runs the file from the top with MYSQL_AUTOMIGRATE=true on both beta and prod. A step that is
// not guarded then fails on «duplicate column» and the process never starts — a halted deploy where
// the old container keeps serving and the cause is three layers down. Nothing else in the suite
// actually executes a migration twice, so this is the only place that claim is tested rather than
// asserted in a comment.
//
// Down is replayed for the same reason it is written at all: it only ever runs in an emergency,
// which is precisely when a typo in it is most expensive.
func TestFabricPurposeMigrationReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	body, err := os.ReadFile("sql/0267_pattern_purpose_binding.sql")
	require.NoError(t, err)
	up, down := splitMigration(t, string(body))

	indexCount := func(name string) int {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece_dxf_block'
			  AND INDEX_NAME = ?`, name).Scan(&n))
		return n
	}
	run := func(label string, stmts []string) {
		for i, q := range stmts {
			if _, err := testDB.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s statement %d failed: %v\n%s", label, i, err, q)
			}
		}
	}

	require.Greater(t, indexCount("uniq_tcpdb_card_scope_block"), 0, "starts applied")
	run("down", down)
	require.Equal(t, 0, indexCount("uniq_tcpdb_card_scope_block"), "down removed the scope unique")
	require.Greater(t, indexCount("uniq_tcpdb_card_slot_block"), 0, "down restored the line unique")
	run("up", up)
	// The re-run is the whole point: every step is guarded on information_schema, so a second pass
	// over an already-applied file must be a no-op rather than a halt.
	run("up (replayed)", up)
	require.Greater(t, indexCount("uniq_tcpdb_card_scope_block"), 0, "up is back")
	require.Equal(t, 0, indexCount("uniq_tcpdb_card_slot_block"), "and the line unique is gone again")
}

// splitMigration returns the Up and Down statement lists of a sql-migrate file. Comments are
// stripped first: a ';' inside a '--' comment would otherwise split a statement in half, and the
// statements are executed ONE AT A TIME because the driver has no multiStatements — the same
// constraint the migration runner itself works under.
func splitMigration(t *testing.T, content string) (up, down []string) {
	t.Helper()
	upPart, rest, ok := strings.Cut(content, "-- +migrate Down")
	require.True(t, ok, "migration has no Down section")
	_, upPart, ok = strings.Cut(upPart, "-- +migrate Up")
	require.True(t, ok, "migration has no Up section")
	stmts := func(body string) []string {
		var kept []string
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			kept = append(kept, line)
		}
		// Quote-aware, because a ';' inside a quoted literal is not a statement boundary — a COMMENT
		// string is the obvious way to hit one, and a splitter that got it wrong would cut a
		// statement in half and report a syntax error that the real runner never sees.
		var out []string
		var cur strings.Builder
		inQuote := false
		src := strings.Join(kept, "\n")
		for i := 0; i < len(src); i++ {
			c := src[i]
			if c == '\'' {
				// '' inside a quoted literal is an escaped quote, not a close-then-open.
				if inQuote && i+1 < len(src) && src[i+1] == '\'' {
					cur.WriteByte(c)
					cur.WriteByte(src[i+1])
					i++
					continue
				}
				inQuote = !inQuote
			}
			if c == ';' && !inQuote {
				if q := strings.TrimSpace(cur.String()); q != "" {
					out = append(out, q)
				}
				cur.Reset()
				continue
			}
			cur.WriteByte(c)
		}
		if q := strings.TrimSpace(cur.String()); q != "" {
			out = append(out, q)
		}
		return out
	}
	return stmts(upPart), stmts(rest)
}
