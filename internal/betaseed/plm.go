package betaseed

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	decimal "google.golang.org/genproto/googleapis/type/decimal"

	admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
)

// PLMResult is the handle SeedPLM returns: the one style it carried through the
// full PLM flow A-L plus every downstream id a verifier / later phase wants.
type PLMResult struct {
	StyleID         int32
	StyleNumber     string
	SuggestedNumber string

	Colorway1ID       int32
	Colorway2ID       int32
	Colorway1BaseSku  string
	Colorway2BaseSku  string
	Colorway1Variants []VariantResult // m,l with minted SKUs (post-publish)

	FabricBomID   int64
	HardwareBomID int64

	MaterialIDs struct {
		Fabric, FabricAlt, Hardware, Thread, Packaging int64
		DustBag, Insert, Composition                   int64
	}

	Sample1ID, Sample2ID     int32
	Fitting1ID, Fitting2ID   int32
	CR1ID, CR2ID, CR2CarryID int32
	ReleaseID                int32
	ReleaseUnitCost          string

	AuxStyleIDs     []int32
	ProductionRunID int32

	OrderAUUID string // storefront, reserve -> release (cancel)
	OrderBUUID string // admin custom (bank_invoice), reserve -> consume -> return

	StepCount int
	PassCount int
	WarnCount int
}

// plmState threads shared ids through the phase methods.
type plmState struct {
	res *PLMResult

	// dictionary-resolved
	mID, lID            int32
	top, sub, typ, leaf int32
	meas                []int32
	country             string
	color1, color2      string
	carrier             int32
	myAdminID           int32
	otherAdminID        int32

	media []int32 // 3 uploaded jpegs

	// stable line keys
	pieceFrontKey, pieceBackKey, pieceCuffKey                   string
	bomFabricKey, bomHardwareKey, bomThreadKey, bomPackagingKey string

	// durable equipment-profile keys (26-char, the shape the server validates)
	overlockProfileKey, ironProfileKey string

	// cut-list numeric piece ids (front, back)
	pieceFrontID, pieceBackID int32

	// colourway variant handles (colourway #1)
	cw1VarMID, cw1VarLID   int32
	cw1VarMSku, cw1VarLSku string

	// order B item ids by size
	orderBItemMID, orderBItemLID int32

	// dust-bag on_hand baseline captured post packaging-receive (H.23)
	dustbagBaseline float64
}

// SeedPLM mints a fresh sellable style (unique per run via s.Run) and carries it
// through the full PLM flow A-L: draft -> design/pieces -> materials/BOM -> two
// colourways + recipe -> sample/fitting rounds -> spec release -> costing ->
// assembly/packaging -> production run -> publish -> orders/fulfillment ->
// hygiene/negative checks. Independent of SeedCatalog. Returns a PLMResult.
func (s *Seeder) SeedPLM(ctx context.Context) (*PLMResult, error) {
	res := &PLMResult{}
	st := &plmState{res: res}

	phases := []struct {
		name string
		fn   func(context.Context, *plmState) error
	}{
		{"setup", s.plmSetup},
		{"A draft + roles", s.plmDraft},
		{"B design + pieces + construction", s.plmDesign},
		{"C materials + composition", s.plmMaterials},
		{"C.10 BOM + operations", s.plmBOM},
		{"C.10b раскладки (marker composition)", s.plmMarkers},
		{"D colourways + recipe", s.plmColorways},
		{"E samples + fittings", s.plmSamples},
		{"spec release Rev.N", s.plmRelease},
		{"F.17 costing estimate", s.plmCostingEstimate},
		{"G assembly + packaging", s.plmAssembly},
		{"H production run + F.18 actual", s.plmProduction},
		{"I publish + hero", s.plmPublish},
		{"J orders + fulfillment", s.plmOrders},
		{"K hygiene + negative", s.plmHygiene},
	}
	for _, p := range phases {
		if err := p.fn(ctx, st); err != nil {
			return res, fmt.Errorf("PLM phase %q (style_id=%d): %w", p.name, res.StyleID, err)
		}
	}
	s.logf("PLM COMPLETE: %d/%d steps passed, %d warnings; style_id=%d style_number=%s cw1=%d(%s) cw2=%d(%s) run=%d orderA=%s orderB=%s",
		res.PassCount, res.StepCount, res.WarnCount, res.StyleID, res.StyleNumber,
		res.Colorway1ID, res.Colorway1BaseSku, res.Colorway2ID, res.Colorway2BaseSku,
		res.ProductionRunID, res.OrderAUUID, res.OrderBUUID)
	return res, nil
}

// --- bookkeeping helpers ---

func (s *Seeder) step(st *plmState, format string, args ...any) {
	st.res.StepCount++
	s.logf("== [%d] "+format+" ==", append([]any{st.res.StepCount}, args...)...)
}
func (s *Seeder) pass(st *plmState, format string, args ...any) {
	st.res.PassCount++
	s.logf("  PASS: "+format, args...)
}
func (s *Seeder) warn(st *plmState, format string, args ...any) {
	st.res.WarnCount++
	s.logf("  WARN: "+format, args...)
}

// key builds a run-unique identifier suffix (mirrors the bash key()).
func (s *Seeder) key(prefix string) string { return prefix + "-" + s.Run }

// --- optimistic-lock helpers (shared tech_card.lock_version) ---

// withLock reads the shared style lock_version, runs fn(lv), and retries on a
// 409/Aborted (a concurrent bump invalidated the token). fn MUST return the raw
// client error (an *APIError) unwrapped so the 409 can be detected.
func (s *Seeder) withLock(ctx context.Context, styleID int32, fn func(lv uint64) error) error {
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		lv, err := s.lockVersion(ctx, styleID)
		if err != nil {
			return err
		}
		if err = fn(lv); err == nil {
			return nil
		}
		if e, ok := AsAPIError(err); ok && e.Code == 409 {
			last = err
			time.Sleep(150 * time.Millisecond)
			continue
		}
		return err
	}
	return last
}

// tcFetch loads the current TechCardInsert body for a read-modify-write cycle.
func (s *Seeder) tcFetch(ctx context.Context, styleID int32) (*common.TechCardInsert, error) {
	r, err := s.C.GetTechCard(ctx, &admin.GetTechCardRequest{Id: styleID})
	if err != nil {
		return nil, fmt.Errorf("GetTechCard(%d): %w", styleID, err)
	}
	tc := r.GetTechCard().GetTechCard()
	if tc == nil {
		return nil, fmt.Errorf("GetTechCard(%d): empty tech card body", styleID)
	}
	return tc, nil
}

// tcSave full-replaces the tech card with a fresh lock_version (retry on 409).
//
// EVERY save states machine_fields_aware, and it is set HERE rather than on each payload because a
// read-modify-write cycle is what the seeder does: tcFetch returns the stored card — machine types,
// equipment profiles and all — and the flag is a property of the CLIENT, never of the body, so the
// read never carries it back. A save that echoed those facts without the flag would be refused as
// «an old bundle is about to flatten what a new one wrote», which is exactly what the flag is for.
func (s *Seeder) tcSave(ctx context.Context, styleID int32, tc *common.TechCardInsert, label string) error {
	tc.MachineFieldsAware = true
	tc.MediaAware = true
	// Осведомлённость о полях сборки. Сидер — первый настоящий НЕинтерактивный клиент фичи, и он
	// обязан ходить штатным путём через все гейты: без флага щит отвергнет любое сохранение против
	// карточки, которую сидер сам же и разметил.
	tc.AssemblyAware = true
	// Осведомлённость о 32 колонках волны видов операций (0324): та же природа read-modify-write —
	// tcFetch возвращает сохранённые блоки шага, и сохранение без флага было бы отвергнуто как
	// «старый бандл собирается стереть то, что написал новый».
	tc.OperationKindsAware = true
	tc.OperationWorkAware = true
	err := s.withLock(ctx, styleID, func(lv uint64) error {
		_, e := s.C.UpdateTechCard(ctx, &admin.UpdateTechCardRequest{
			Id:                  styleID,
			ExpectedLockVersion: int32(lv),
			TechCard:            tc,
		})
		return e
	})
	if err != nil {
		return fmt.Errorf("UpdateTechCard(%s): %w", label, err)
	}
	return nil
}

// ========================================================================= setup

func (s *Seeder) plmSetup(ctx context.Context, st *plmState) error {
	s.step(st, "setup: dictionary + admin ids + media + fibres")

	var err error
	if st.mID, err = s.SizeIDByName("m"); err != nil {
		return err
	}
	if st.lID, err = s.SizeIDByName("l"); err != nil {
		return err
	}
	if st.top, st.sub, st.typ, st.leaf, err = s.CategoryChain(); err != nil {
		return err
	}
	if st.meas, err = s.MeasurementIDs(2); err != nil {
		return err
	}
	st.country = s.CountryCode()
	if st.color1, _, err = s.ColorByCode("BLK"); err != nil {
		return err
	}
	st.color2 = s.secondColor(st.color1)
	st.carrier = s.carrierID()

	// resolve own admin id (+ a distinct second one when available) for role assignment.
	ar, err := s.C.ListAdmins(ctx, &admin.ListAdminsRequest{})
	if err != nil {
		return fmt.Errorf("ListAdmins: %w", err)
	}
	for _, a := range ar.GetAdmins() {
		if st.myAdminID == 0 {
			st.myAdminID = a.GetId()
		}
		if a.GetId() != st.myAdminID && st.otherAdminID == 0 {
			st.otherAdminID = a.GetId()
		}
	}
	if st.myAdminID == 0 {
		return fmt.Errorf("ListAdmins returned no admins")
	}
	if st.otherAdminID == 0 {
		st.otherAdminID = st.myAdminID // degrade: reuse own id for the 2nd role row
	}

	// 3 real JPEGs (technical x2, moodboard x1).
	st.media = make([]int32, 0, 3)
	for i := 0; i < 3; i++ {
		id, err := s.UploadJPEG(ctx, fmt.Sprintf("PLM-%s-%d", s.Run, i))
		if err != nil {
			return err
		}
		st.media = append(st.media, id)
	}

	// fibre dictionary (FK for material composition_entries; idempotent reuse-or-create).
	if err := s.ensureFibers(ctx, [][2]string{{"COT", "Cotton"}, {"POL", "Polyester"}, {"NYL", "Nylon"}, {"ELA", "Elastane"}}); err != nil {
		return err
	}

	// line keys
	st.pieceFrontKey = s.key("piece-front")
	st.pieceBackKey = s.key("piece-back")
	// Третья деталь нужна ГРАФУ: с двумя деталями второй джойн собрать не из чего, а без второго
	// джойна нет ни поглощения, ни настоящей проверки правила 4.
	st.pieceCuffKey = s.key("piece-cuff")
	st.bomFabricKey = s.key("bom-fabric")
	st.bomHardwareKey = s.key("bom-hardware")
	st.bomThreadKey = s.key("bom-thread")
	st.bomPackagingKey = s.key("bom-packaging")

	// Equipment-profile keys are DURABLE CLIENT-MINTED IDENTITIES, not readable run keys: the server
	// validates a profile_key as exactly 26 alphanumerics (the piece / BOM line-key rule), because a
	// step points at a profile by this key and the whole list is full-replaced on every save.
	// seedIdempotencyKey mints exactly that shape.
	st.overlockProfileKey = seedIdempotencyKey()
	st.ironProfileKey = seedIdempotencyKey()

	s.pass(st, "setup: sizes(m=%d l=%d) cat(top=%d sub=%d type=%d) meas=%v country=%s colors=%s,%s carrier=%d admin=%d/%d media=%v",
		st.mID, st.lID, st.top, st.sub, st.typ, st.meas, st.country, st.color1, st.color2, st.carrier, st.myAdminID, st.otherAdminID, st.media)
	return nil
}

// secondColor returns a non-archived colour code different from first (falls back to first).
func (s *Seeder) secondColor(first string) string {
	for _, c := range s.Dict.GetColors() {
		if c.GetArchived() {
			continue
		}
		if !strings.EqualFold(c.GetCode(), first) {
			return c.GetCode()
		}
	}
	return first
}

// ensureFibers creates any fibre codes missing from a fresh dictionary read.
func (s *Seeder) ensureFibers(ctx context.Context, fibers [][2]string) error {
	dr, err := s.C.GetDictionary(ctx, &admin.GetDictionaryRequest{})
	if err != nil {
		return fmt.Errorf("GetDictionary (fibres): %w", err)
	}
	have := map[string]bool{}
	for _, f := range dr.GetDictionary().GetFibers() {
		have[strings.ToUpper(f.GetCode())] = true
	}
	for _, f := range fibers {
		if have[strings.ToUpper(f[0])] {
			continue
		}
		if _, err := s.C.CreateFiber(ctx, &admin.CreateFiberRequest{Code: f[0], Name: f[1], ExpectedVersion: 0}); err != nil {
			return fmt.Errorf("CreateFiber(%s): %w", f[0], err)
		}
	}
	return nil
}

// ========================================================================= A. draft + roles

func (s *Seeder) plmDraft(ctx context.Context, st *plmState) error {
	// A.2 SuggestStyleNumber (constructor).
	s.step(st, "A.2 SuggestStyleNumber + CreateTechCard draft (manual override, purpose=sellable)")
	sn, err := s.C.SuggestStyleNumber(ctx, &admin.SuggestStyleNumberRequest{
		SkuSeason: &common.SkuSeason{Code: common.SeasonEnum_SEASON_ENUM_SS, Year: 2026},
	})
	if err != nil {
		return fmt.Errorf("SuggestStyleNumber: %w", err)
	}
	st.res.SuggestedNumber = sn.GetStyleNumber()
	if st.res.SuggestedNumber == "" {
		return fmt.Errorf("SuggestStyleNumber returned empty")
	}
	s.pass(st, "SuggestStyleNumber -> %s", st.res.SuggestedNumber)

	// NEGATIVE: a lowercase/underscored manual override must be rejected (400).
	_, negErr := s.C.CreateTechCard(ctx, &admin.CreateTechCardRequest{
		TechCard: &common.TechCardInsert{
			StyleNumber:       "bad_format_" + s.Run,
			StyleNumberSource: common.StyleNumberSource_STYLE_NUMBER_SOURCE_MANUAL,
			Name:              "QA negative probe",
		},
	})
	if e, ok := AsAPIError(negErr); !ok || e.Code != 400 {
		return fmt.Errorf("NEGATIVE bad style_number: expected HTTP 400, got %v", negErr)
	}
	s.pass(st, "NEGATIVE manual style_number 'bad_format_%s' rejected -> HTTP 400", s.Run)

	// Real create: strict manual override that satisfies ^[A-Z0-9]{2,}(-[A-Z0-9]+)*$.
	styleNumber := "PLM-SEED-" + s.Run
	cr, err := s.C.CreateTechCard(ctx, &admin.CreateTechCardRequest{
		TechCard: &common.TechCardInsert{
			StyleNumber:       styleNumber,
			StyleNumberSource: common.StyleNumberSource_STYLE_NUMBER_SOURCE_MANUAL,
			Name:              "PLM Seed Jacket " + s.Run,
			Brand:             "grbpwr",
			Collection:        "beta-seed-collection",
			CategoryId:        st.leaf,
			TargetGender:      common.GenderEnum_GENDER_ENUM_UNISEX,
			Stage:             common.TechCardStage_TECH_CARD_STAGE_PROTO,
			ApprovalState:     common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_DRAFT,
			Purpose:           common.TechCardPurpose_TECH_CARD_PURPOSE_SELLABLE,
			SkuSeason:         &common.SkuSeason{Code: common.SeasonEnum_SEASON_ENUM_SS, Year: 2026},
			Concept:           "PLM acceptance seed: a single jacket style carried end to end.",
			// Ф3.2: the card's own ТРЕБУЕМЫЙ ПРИПУСК, the right-hand side a readiness gate compares a
			// раскладка's recorded allowance against. Set on the CARD rather than through the workshop
			// singleton on purpose: the seeder runs repeatedly and must not clobber a shop-wide setting
			// a human configured.
			RequiredSeamAllowanceMm: decv("1"),
			// The card is created by a client that knows about machines and ВТО profiles — every
			// later save of it (via tcSave) says the same, and B.7 fills its equipment park.
			MachineFieldsAware:  true,
			AssemblyAware:       true,
			MediaAware:          true,
			OperationKindsAware: true,
			OperationWorkAware:  true,
		},
	})
	if err != nil {
		return fmt.Errorf("CreateTechCard: %w", err)
	}
	st.res.StyleID = cr.GetId()
	st.res.StyleNumber = styleNumber
	if st.res.StyleID == 0 {
		return fmt.Errorf("CreateTechCard returned style_id=0")
	}
	s.pass(st, "CreateTechCard -> style_id=%d style_number=%s", st.res.StyleID, styleNumber)

	// A.2b roles x2 (DESIGNER + APPROVER).
	s.step(st, "A.2b AssignTechCardRole x2 (multi-role)")
	if _, err := s.C.AssignTechCardRole(ctx, &admin.AssignTechCardRoleRequest{
		TechCardId: st.res.StyleID, Role: common.TechCardRole_TECH_CARD_ROLE_DESIGNER, AdminId: st.myAdminID,
	}); err != nil {
		return fmt.Errorf("AssignTechCardRole(DESIGNER): %w", err)
	}
	if _, err := s.C.AssignTechCardRole(ctx, &admin.AssignTechCardRoleRequest{
		TechCardId: st.res.StyleID, Role: common.TechCardRole_TECH_CARD_ROLE_APPROVER, AdminId: st.otherAdminID,
	}); err != nil {
		return fmt.Errorf("AssignTechCardRole(APPROVER): %w", err)
	}
	rr, err := s.C.ListTechCardRoleAssignments(ctx, &admin.ListTechCardRoleAssignmentsRequest{TechCardId: st.res.StyleID})
	if err != nil {
		return fmt.Errorf("ListTechCardRoleAssignments: %w", err)
	}
	if len(rr.GetAssignments()) < 2 {
		return fmt.Errorf("expected >=2 role assignments, got %d", len(rr.GetAssignments()))
	}
	s.pass(st, "roles: DESIGNER=%d APPROVER=%d -> %d assignment rows", st.myAdminID, st.otherAdminID, len(rr.GetAssignments()))

	// A.3 auto-journal revisions.
	s.step(st, "A.3 revisions/auto-journal server-stamped")
	tr, err := s.C.GetTechCard(ctx, &admin.GetTechCardRequest{Id: st.res.StyleID})
	if err != nil {
		return fmt.Errorf("GetTechCard(revisions): %w", err)
	}
	if len(tr.GetTechCard().GetRevisions()) < 1 {
		return fmt.Errorf("expected >=1 auto-journal revision after create")
	}
	s.pass(st, "GetTechCard.revisions=%d after create", len(tr.GetTechCard().GetRevisions()))
	return nil
}

// ========================================================================= B. design + pieces + construction

func (s *Seeder) plmDesign(ctx context.Context, st *plmState) error {
	sid := st.res.StyleID

	// B.4/B.5 technical sketches + moodboard + callouts (one pinned onto the moodboard image).
	s.step(st, "B.4/B.5 technical sketches (2) + moodboard (1) + callouts (2)")
	tc, err := s.tcFetch(ctx, sid)
	if err != nil {
		return err
	}
	t1, t2, mood := st.media[0], st.media[1], st.media[2]
	tc.TechnicalMedia = []*common.TechCardMediaItem{
		{MediaId: t1, Kind: common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_FRONT, Caption: "front flat"},
		{MediaId: t2, Kind: common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_BACK, Caption: "back flat"},
	}
	tc.MoodboardMedia = []*common.TechCardMediaItem{
		{MediaId: mood, Kind: common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_MOODBOARD, Caption: "mood ref"},
	}
	tc.Callouts = []*common.TechCardCallout{
		{Number: 1, Part: "front placket", Description: "zip placket detail", Dimensions: "2cm wide", MediaId: t1, PosX: decv("0.5"), PosY: decv("0.3")},
		{Number: 2, Part: "palette", Description: "seasonal colour reference", MediaId: mood, PosX: decv("0.4"), PosY: decv("0.6")},
	}
	if err := s.tcSave(ctx, sid, tc, "B.4/B.5"); err != nil {
		return err
	}
	if tc, err = s.tcFetch(ctx, sid); err != nil {
		return err
	}
	if len(tc.GetTechnicalMedia()) != 2 || len(tc.GetMoodboardMedia()) != 1 || len(tc.GetCallouts()) != 2 {
		return fmt.Errorf("B.4/B.5 readback: technical=%d moodboard=%d callouts=%d", len(tc.GetTechnicalMedia()), len(tc.GetMoodboardMedia()), len(tc.GetCallouts()))
	}
	s.pass(st, "B.4/B.5 readback: technical=2 moodboard=1 callouts=2 (callout#2 on the moodboard image)")

	// B.6 pieces, verified via the cut-list. The mirror flag is retired (the admin no longer sends
	// it and GetStyleCutList no longer doubles by it), so a left/right pair is stated the way a
	// graded DXF actually draws it — as a count, not as a flag on one row.
	//
	// cut_symmetry (0275) says what the note here has always said in prose: "left + right" is a
	// MIRRORED pair, "single cut, on the fold" is FOLD. The readback below asserts the counts did not
	// move — front stays 2, back stays 1 — which is the whole claim of the field: it classifies the
	// number already there and multiplies nothing.
	s.step(st, "B.6 pieces (front 2/garment mirrored, back 1 on the fold) via cut-list")
	tc.Pieces = []*common.TechCardPiece{
		{Name: "front panel", PiecesPerGarment: 2, Grainline: "lengthwise", CalloutNumber: p32(1), Note: "left + right", LineKey: st.pieceFrontKey,
			CutSymmetry: pcs(common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_MIRRORED)},
		{Name: "back panel", PiecesPerGarment: 1, Grainline: "lengthwise", Note: "single cut, on the fold", LineKey: st.pieceBackKey,
			CutSymmetry: pcs(common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_FOLD)},
		{Name: "cuff", PiecesPerGarment: 2, Grainline: "crosswise", Note: "вход второго джойна — без него граф не собрать", LineKey: st.pieceCuffKey,
			CutSymmetry: pcs(common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_IDENTICAL)},
	}
	if err := s.tcSave(ctx, sid, tc, "B.6 pieces"); err != nil {
		return err
	}
	cl, err := s.C.GetStyleCutList(ctx, &admin.GetStyleCutListRequest{TechCardId: sid})
	if err != nil {
		return fmt.Errorf("GetStyleCutList: %w", err)
	}
	var frontTotal, backTotal int32
	var frontSym, backSym common.TechCardPieceCutSymmetry
	for _, p := range cl.GetPieces() {
		// Matched by NAME now: the mirror flag used to be what told the two rows apart, and it is
		// false on both from here on.
		//
		// ЯВНЫЙ switch, а не if/else: раньше «всё, что не front» считалось back, и появление
		// третьей детали молча перезаписало бы id спинки манжетой — вместе с проверками ниже,
		// которые тогда сверяли бы не то, что думают.
		switch p.GetName() {
		case "front panel":
			st.pieceFrontID = p.GetPieceId()
			frontTotal = p.GetTotalPerGarment()
			frontSym = p.GetCutSymmetry()
		case "back panel":
			st.pieceBackID = p.GetPieceId()
			backTotal = p.GetTotalPerGarment()
			backSym = p.GetCutSymmetry()
		}
	}
	if st.pieceFrontID == 0 || st.pieceBackID == 0 {
		return fmt.Errorf("cut-list did not resolve numeric piece ids (front=%d back=%d)", st.pieceFrontID, st.pieceBackID)
	}
	if frontTotal != 2 || backTotal != 1 {
		return fmt.Errorf("cut math: front total_per_garment=%d (want 2) back=%d (want 1)", frontTotal, backTotal)
	}
	// The counts above are the proof that 0275 classifies rather than multiplies; these two are the
	// proof it survived the round trip at all.
	if frontSym != common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_MIRRORED {
		return fmt.Errorf("cut symmetry: front=%s (want MIRRORED)", frontSym)
	}
	if backSym != common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_FOLD {
		return fmt.Errorf("cut symmetry: back=%s (want FOLD)", backSym)
	}
	s.pass(st, "B.6 cut-list: front piece=%d total=2 mirrored, back piece=%d total=1 on the fold",
		st.pieceFrontID, st.pieceBackID)

	// B.7/B.8 construction + size range.
	s.step(st, "B.7/B.8 construction section + size range + size chart + UpdateStyle")
	if tc, err = s.tcFetch(ctx, sid); err != nil {
		return err
	}
	// The card's equipment PARK replaces the two free-text answers this section used to give. The
	// thread count was `overlock_thread_count` on the construction — one number that could only ever
	// describe one overlock — and is a machine profile now; the ВТО prose was `pressing` and is a
	// press profile with an actual temperature and dwell. The prose itself is not thrown away: it
	// stays in the notes, which is exactly where 0306 put it for every existing card.
	tc.Construction = &common.TechCardConstruction{
		DefaultSeamClass:     common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_SS_PLAIN,
		DefaultStitchesPerCm: decv("4"),
		HemFinish:            "coverstitch double-fold",
		Notes:                "QA seeded construction section\n\nВТО: steam press, no top pressure on print",
		EquipmentDefaults: &common.TechCardEquipmentDefaults{
			Machines: []*common.TechCardMachineProfile{{
				ProfileKey:  st.overlockProfileKey,
				Label:       "seed overlock",
				MachineType: common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK,
				ThreadCount: 4,
			}},
			Presses: []*common.TechCardPressProfile{{
				ProfileKey:        st.ironProfileKey,
				Label:             "seed iron",
				PressEquipment:    common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON,
				OperationType:     common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS,
				PressTemperatureC: 140,
				PressDwellSec:     10,
				PressSteam:        pbool(true),
				Note:              "no top pressure on print",
			}},
		},
	}
	tc.SizeIds = []int32{st.mID, st.lID}
	if err := s.tcSave(ctx, sid, tc, "B.7/B.8 construction+size_ids"); err != nil {
		return err
	}
	if tc, err = s.tcFetch(ctx, sid); err != nil {
		return err
	}
	if tc.GetConstruction().GetDefaultSeamClass() == common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_UNKNOWN || len(tc.GetSizeIds()) != 2 {
		return fmt.Errorf("B.7/B.8 readback: construction=%v size_ids=%d", tc.GetConstruction().GetDefaultSeamClass(), len(tc.GetSizeIds()))
	}
	// The park has to come BACK, wrapper and all: a read that dropped it would make the next save
	// (and the seasonal clone, which round-trips the read) silently lose the profiles.
	park := tc.GetConstruction().GetEquipmentDefaults()
	if len(park.GetMachines()) != 1 || len(park.GetPresses()) != 1 {
		return fmt.Errorf("B.7 equipment park readback: machines=%d presses=%d (want 1/1)",
			len(park.GetMachines()), len(park.GetPresses()))
	}
	if park.GetMachines()[0].GetProfileKey() != st.overlockProfileKey || park.GetMachines()[0].GetThreadCount() != 4 {
		return fmt.Errorf("B.7 overlock profile readback: key=%q threads=%d",
			park.GetMachines()[0].GetProfileKey(), park.GetMachines()[0].GetThreadCount())
	}
	if park.GetPresses()[0].GetPressTemperatureC() != 140 || !park.GetPresses()[0].GetPressSteam() {
		return fmt.Errorf("B.7 iron profile readback: temp=%d steam=%v",
			park.GetPresses()[0].GetPressTemperatureC(), park.GetPresses()[0].GetPressSteam())
	}
	s.pass(st, "B.7 construction.defaultSeamClass=%v + equipment park (overlock 4 threads, iron 140C/10s steam); B.8 size_ids=[%d,%d]",
		tc.GetConstruction().GetDefaultSeamClass(), st.mID, st.lID)

	// style size chart (shares the lock_version).
	if err := s.withLock(ctx, sid, func(lv uint64) error {
		_, e := s.C.UpdateStyleSizeChart(ctx, &admin.UpdateStyleSizeChartRequest{
			StyleId:             sid,
			ExpectedLockVersion: lv,
			Cells: []*common.StyleSizeChartCell{
				{SizeId: st.mID, MeasurementNameId: st.meas[0], Value: decv("50")},
				{SizeId: st.lID, MeasurementNameId: st.meas[0], Value: decv("54")},
				{SizeId: st.mID, MeasurementNameId: st.meas[1], Value: decv("48")},
				{SizeId: st.lID, MeasurementNameId: st.meas[1], Value: decv("52")},
			},
		})
		return e
	}); err != nil {
		return fmt.Errorf("UpdateStyleSizeChart: %w", err)
	}

	// UpdateStyle: categories + merch facts (top_category_id must be non-null).
	if err := s.withLock(ctx, sid, func(lv uint64) error {
		_, e := s.C.UpdateStyle(ctx, &admin.UpdateStyleRequest{
			StyleId:             int64(sid),
			ExpectedLockVersion: lv,
			Patch: &admin.StylePatch{
				Brand:              "grbpwr",
				Season:             common.SeasonEnum_SEASON_ENUM_SS,
				Collection:         "beta-seed-collection",
				TargetGender:       common.GenderEnum_GENDER_ENUM_UNISEX,
				Fit:                "regular",
				Composition:        "70% cotton, 30% polyester",
				CareInstructions:   "Machine wash cold at 30, do not tumble dry",
				ModelWearsHeightCm: 181,
				ModelWearsSizeId:   st.mID,
				TopCategoryId:      st.top,
				SubCategoryId:      st.sub,
				TypeId:             st.typ,
			},
		})
		return e
	}); err != nil {
		return fmt.Errorf("UpdateStyle: %w", err)
	}
	s.pass(st, "B.8 size chart (4 cells) + UpdateStyle (categories/composition) applied")
	return nil
}

// ========================================================================= C. materials + composition

func (s *Seeder) plmMaterials(ctx context.Context, st *plmState) error {
	s.step(st, "C.9 materials: >=4 typed classes (fabric/hardware/thread/packaging) + prices + composition")
	m := &st.res.MaterialIDs
	var err error

	if m.Fabric, err = s.createMaterial(ctx, &common.Material{
		Name: "Shell Fabric - Main", Section: common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
		MaterialClass: common.MaterialClass_MATERIAL_CLASS_FABRIC, Code: s.key("FAB"), Unit: "m",
		Supplier: "Milano Tessuti", SupplierRef: "MT-2201", MinStock: decv("20"),
		Attributes: &common.Material_FabricAttrs{FabricAttrs: &common.MaterialFabricAttrs{
			WidthCm: decv("150"), WeightGsm: decv("220"), FabricDirection: "lengthwise", ShrinkagePct: decv("3"), RollLengthM: decv("50"),
		}},
		CompositionEntries: []*common.CompositionEntry{{FiberCode: "COT", Percent: decv("70")}, {FiberCode: "POL", Percent: decv("30")}},
	}); err != nil {
		return err
	}
	if m.FabricAlt, err = s.createMaterial(ctx, &common.Material{
		Name: "Shell Fabric - Alt (substitute)", Section: common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
		MaterialClass: common.MaterialClass_MATERIAL_CLASS_FABRIC, Code: s.key("FABALT"), Unit: "m",
		Supplier: "Milano Tessuti", SupplierRef: "MT-2202", MinStock: decv("20"),
		Attributes: &common.Material_FabricAttrs{FabricAttrs: &common.MaterialFabricAttrs{
			WidthCm: decv("150"), WeightGsm: decv("230"), FabricDirection: "lengthwise", ShrinkagePct: decv("3"), RollLengthM: decv("50"),
		}},
	}); err != nil {
		return err
	}
	if m.Hardware, err = s.createMaterial(ctx, &common.Material{
		Name: "YKK Zipper Pull 5mm", Section: common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE,
		MaterialClass: common.MaterialClass_MATERIAL_CLASS_HARDWARE, Code: s.key("HRD"), Unit: "pcs",
		Supplier: "YKK", SupplierRef: "YKK-5C", MinStock: decv("100"),
		Attributes: &common.Material_HardwareAttrs{HardwareAttrs: &common.MaterialHardwareAttrs{
			DiameterMm: decv("5"), Dimensions: "55cm zip", Finish: "matte nickel", BaseMaterial: "zinc alloy", WeightG: decv("6"),
		}},
	}); err != nil {
		return err
	}
	if m.Thread, err = s.createMaterial(ctx, &common.Material{
		Name: "Polyester Sewing Thread Tex30", Section: common.TechCardBomSection_TECH_CARD_BOM_SECTION_THREAD,
		MaterialClass: common.MaterialClass_MATERIAL_CLASS_THREAD, Code: s.key("THR"), Unit: "cone",
		Supplier: "Amann", SupplierRef: "AM-T30", MinStock: decv("30"),
		Attributes: &common.Material_ThreadAttrs{ThreadAttrs: &common.MaterialThreadAttrs{
			TicketTex: "Tex 30", LengthPerConeM: decv("5000"), NeedleReco: "90/14",
		}},
	}); err != nil {
		return err
	}
	if m.Packaging, err = s.createMaterial(ctx, &common.Material{
		Name: "Shipping Box - Standard", Section: common.TechCardBomSection_TECH_CARD_BOM_SECTION_PACKAGING,
		MaterialClass: common.MaterialClass_MATERIAL_CLASS_PACKAGING, Code: s.key("PKG"), Unit: "pcs",
		Supplier: "KartonWerk", SupplierRef: "KW-30x20x10", MinStock: decv("50"),
		Attributes: &common.Material_PackagingAttrs{PackagingAttrs: &common.MaterialPackagingAttrs{
			Substrate: "kraft cardboard", Dimensions: "30x20x10cm", Gsm: decv("350"), PrintMethod: "offset 1-colour",
		}},
	}); err != nil {
		return err
	}
	for _, mid := range []int64{m.Fabric, m.FabricAlt, m.Hardware, m.Thread, m.Packaging} {
		if err := s.addMaterialPrice(ctx, mid, "12.50", "EUR"); err != nil {
			return err
		}
	}
	s.pass(st, "C.9 materials: fabric=%d alt=%d hardware=%d thread=%d packaging=%d (4 typed classes + EUR price each; shell composition COT70/POL30)",
		m.Fabric, m.FabricAlt, m.Hardware, m.Thread, m.Packaging)

	// C.11 composition round-trip: create a composition-bearing material, read it back.
	s.step(st, "C.11 material composition: CreateMaterial(NYL60/ELA40) -> GetMaterial round-trip + sum!=100 negative")
	if m.Composition, err = s.createMaterial(ctx, &common.Material{
		Name: "Stretch Lining - PLM Seed", Section: common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
		MaterialClass: common.MaterialClass_MATERIAL_CLASS_FABRIC, Code: s.key("COMPFAB"), Unit: "m",
		Supplier: "Sattler Textil", MinStock: decv("10"),
		Attributes: &common.Material_FabricAttrs{FabricAttrs: &common.MaterialFabricAttrs{
			WidthCm: decv("140"), WeightGsm: decv("180"), FabricDirection: "lengthwise",
		}},
		CompositionEntries: []*common.CompositionEntry{{FiberCode: "NYL", Percent: decv("60")}, {FiberCode: "ELA", Percent: decv("40")}},
	}); err != nil {
		return err
	}
	// NEGATIVE: composition summing to 80 must be rejected.
	_, negErr := s.C.CreateMaterial(ctx, &admin.CreateMaterialRequest{Material: &common.Material{
		Name: "QA negative composition probe", Section: common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
		MaterialClass: common.MaterialClass_MATERIAL_CLASS_FABRIC, Code: s.key("COMPBAD"), Unit: "m",
		CompositionEntries: []*common.CompositionEntry{{FiberCode: "COT", Percent: decv("50")}, {FiberCode: "POL", Percent: decv("30")}},
	}})
	if e, ok := AsAPIError(negErr); !ok || e.Code != 400 {
		return fmt.Errorf("NEGATIVE composition sum=80: expected HTTP 400, got %v", negErr)
	}
	gm, err := s.C.GetMaterial(ctx, &admin.GetMaterialRequest{Id: m.Composition})
	if err != nil {
		return fmt.Errorf("GetMaterial(composition): %w", err)
	}
	ce := gm.GetMaterial().GetCompositionEntries()
	if len(ce) != 2 || decFloat(pctByFiber(ce, "NYL")) != 60 || decFloat(pctByFiber(ce, "ELA")) != 40 {
		return fmt.Errorf("composition round-trip mismatch: %v", ce)
	}
	s.pass(st, "C.11 material=%d composition NYL60/ELA40 round-trips; sum!=100 rejected -> 400", m.Composition)
	return nil
}

// createMaterial creates a material and returns its id.
func (s *Seeder) createMaterial(ctx context.Context, mat *common.Material) (int64, error) {
	r, err := s.C.CreateMaterial(ctx, &admin.CreateMaterialRequest{Material: mat})
	if err != nil {
		return 0, fmt.Errorf("CreateMaterial(%s): %w", mat.GetName(), err)
	}
	if r.GetId() == 0 {
		return 0, fmt.Errorf("CreateMaterial(%s) returned id=0", mat.GetName())
	}
	return r.GetId(), nil
}

func (s *Seeder) addMaterialPrice(ctx context.Context, materialID int64, price, cur string) error {
	_, err := s.C.AddMaterialPrice(ctx, &admin.AddMaterialPriceRequest{Price: &common.MaterialPrice{
		MaterialId: materialID, Price: decv(price), Currency: cur,
		ValidFrom: timestamppb.Now(), Source: "manual", Note: "PLM seed manual price",
	}})
	if err != nil {
		return fmt.Errorf("AddMaterialPrice(material=%d): %w", materialID, err)
	}
	return nil
}

// ========================================================================= C.10 BOM

func (s *Seeder) plmBOM(ctx context.Context, st *plmState) error {
	sid := st.res.StyleID
	m := &st.res.MaterialIDs
	s.step(st, "C.10 BOM (4 lines, line_key) + operations; materialSnapshot auto-transfer; negative bad bomLineKey")
	tc, err := s.tcFetch(ctx, sid)
	if err != nil {
		return err
	}
	tc.BomItems = []*common.TechCardBomItem{
		{Section: common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, Name: "Shell Fabric - Main", MaterialId: m.Fabric, LineKey: st.bomFabricKey,
			Unit: "m", UnitPrice: decv("12.50"), Currency: "EUR", FabricWidth: decv("150"), FabricWeightGsm: decv("220"),
			FabricDirection: common.TechCardFabricDirection_TECH_CARD_FABRIC_DIRECTION_ANY.Enum(), WastagePercent: decv("8")},
		{Section: common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE, Name: "YKK Zipper Pull 5mm", MaterialId: m.Hardware, LineKey: st.bomHardwareKey,
			Unit: "pcs", UnitPrice: decv("0.90"), Currency: "EUR"},
		{Section: common.TechCardBomSection_TECH_CARD_BOM_SECTION_THREAD, Name: "Polyester Sewing Thread Tex30", MaterialId: m.Thread, LineKey: st.bomThreadKey,
			Unit: "cone", UnitPrice: decv("6.00"), Currency: "EUR"},
		{Section: common.TechCardBomSection_TECH_CARD_BOM_SECTION_PACKAGING, Name: "Shipping Box - Standard", MaterialId: m.Packaging, LineKey: st.bomPackagingKey,
			Unit: "pcs", UnitPrice: decv("0.60"), Currency: "EUR"},
	}
	// MACHINE + machine_type, not the legacy LOCKSTITCH: «что делаем» and «на чём» are two fields
	// now, and the legacy token would be canonicalised into exactly this pair anyway. The seeder is
	// the only automatic end-to-end run of a card, so it speaks the form the client will send.
	// СБОРОЧНЫЙ ГРАФ (0307), а не одна операция. Раньше здесь сеялся единственный шаг вовсе без
	// привязок к деталям, и «дозаполнить его узлами» было невозможно: джойну нужно не меньше двух
	// входов, а входов не было ни одного.
	//
	// Граф намеренно проходит ВСЕ правила, включая четвёртое: один терминал, и каждая деталь в
	// него попадает. Бета — единственное место, где фичу можно увидеть работающей до того, как её
	// разметит живой технолог, поэтому демо обязано быть валидным, а не просто непустым.
	//
	//   10  полочка + спинка              → SHELL      джойн: узел рождается
	//   20  SHELL                         → (ничего)   обработка: вход остаётся на столе
	//   30  SHELL + манжета               → SHELL      ПОГЛОЩЕНИЕ: узел сохраняет идентичность
	//
	// Терминал ровно один (SHELL), в него входят все три детали.
	tc.Operations = []*common.TechCardOperation{
		{OperationNumber: 10,
			OperationType: common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			MachineType:   common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
			// CLOSURE, not OUTER: the zone now says WHERE on the garment, and «front placket» — the
			// free-text placement this row used to carry — is exactly what it replaces.
			Zone:           common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_CLOSURE,
			BomLineKeys:    []string{st.bomHardwareKey},
			CalloutNumber:  1,
			InputKeys:      []string{st.pieceFrontKey, st.pieceBackKey},
			OutputUnitKey:  "SHELL",
			OutputUnitName: "корпус",
			Note:           "attach main zipper to front placket"},
		{OperationNumber: 20,
			OperationType: common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			MachineType:   common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
			Zone:          common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_OTHER,
			// Обработка: выходного ключа НЕТ. Её вход не съедается и остаётся доступным шагу 30 —
			// в этом вся разница между обработкой и джойном.
			InputKeys: []string{"SHELL"},
			Note:      "отстрочка по корпусу"},
		{OperationNumber: 30,
			OperationType: common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			MachineType:   common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
			Zone:          common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_SLEEVE,
			InputKeys:     []string{"SHELL", st.pieceCuffKey},
			OutputUnitKey: "SHELL",
			Note:          "притачать манжету — узел вбирает деталь, идентичность сохраняется"},
	}

	// NEGATIVE: an operation referencing an unknown bom_line_key must be rejected (400).
	// Fire on a clone so it never touches the real save.
	neg := proto.Clone(tc).(*common.TechCardInsert)
	// The probe is the unknown key and nothing else: with `node` gone, the row carries only the two
	// required fields plus the bad reference, so a rejection can mean one thing.
	neg.Operations = append(neg.Operations, &common.TechCardOperation{
		OperationType: common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
		MachineType:   common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
		Zone:          common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_OTHER,
		BomLineKeys:   []string{"does-not-exist-xyz"}})
	negLV, err := s.lockVersion(ctx, sid)
	if err != nil {
		return err
	}
	// The probe bypasses tcSave, so it states the capability itself — a payload carrying machine
	// facts without the flag is refused for a DIFFERENT reason, and the probe would then pass while
	// proving nothing about bom_line_key.
	neg.MachineFieldsAware = true
	neg.AssemblyAware = true
	neg.MediaAware = true
	neg.OperationKindsAware = true
	neg.OperationWorkAware = true
	_, negErr := s.C.UpdateTechCard(ctx, &admin.UpdateTechCardRequest{Id: sid, ExpectedLockVersion: int32(negLV), TechCard: neg})
	if e, ok := AsAPIError(negErr); !ok || e.Code != 400 {
		return fmt.Errorf("NEGATIVE bad bomLineKey: expected HTTP 400, got %v", negErr)
	}
	s.pass(st, "NEGATIVE operation.bomLineKey='does-not-exist-xyz' rejected -> HTTP 400")

	if err := s.tcSave(ctx, sid, tc, "C.10 BOM + operations"); err != nil {
		return err
	}
	if tc, err = s.tcFetch(ctx, sid); err != nil {
		return err
	}
	if len(tc.GetBomItems()) != 4 {
		return fmt.Errorf("expected 4 BOM lines, got %d", len(tc.GetBomItems()))
	}
	for _, b := range tc.GetBomItems() {
		switch b.GetLineKey() {
		case st.bomFabricKey:
			st.res.FabricBomID = b.GetId()
		case st.bomHardwareKey:
			st.res.HardwareBomID = b.GetId()
		}
	}
	if st.res.FabricBomID == 0 || st.res.HardwareBomID == 0 {
		return fmt.Errorf("BOM lines did not resolve stable ids via line_key")
	}
	s.pass(st, "C.10 BOM saved: 4 lines, fabric_bom_id=%d hardware_bom_id=%d, materialSnapshot auto-populated", st.res.FabricBomID, st.res.HardwareBomID)
	return nil
}

// =========================================================================== C.10b раскладки (Ф2)

// plmMarkers saves TWO saved раскладки, and the pair is the point: beta has never carried a single
// marker (the seeder predates 0257 entirely), so the СОСТАВ format was observable only by driving the
// nesting modal by hand. One marker per branch of the instance formula:
//
//		instances(piece) = quantity × (piece.size_id > 0 ? composition[size_id] : total_units)
//
//	  - «однородная» goes down the LEGACY write path — size_id + sets, a v2 blob with no composition
//	    and no size on any piece, exactly what a stale admin bundle sends. The server must store it in
//	    the old shape and still read it back as a состав из одного размера. That is the branch every
//	    row on prod is in today, and the one a deploy overlap keeps producing.
//	  - «смешанная» goes down the Ф2 path — a v4 blob carrying composition and sizes on pieces, with
//	    one deliberately size-AGNOSTIC piece so the `: total_units` half of the formula is covered too.
//	    It must come back with size_id/sets 0, a three-line состав, and NO scalar consumption figure
//	    (Р2: the mean across a состав is withheld, not labelled).
func (s *Seeder) plmMarkers(ctx context.Context, st *plmState) error {
	sid := st.res.StyleID
	s.step(st, "C.10b раскладки: одна легаси (size+sets) + одна со СОСТАВОМ (v4), обе на fabric BOM line")

	// A rectangle is enough geometry: the server stores the blob opaquely and validates only its
	// cardinality and its rotations. Two shapes so the pieces are visibly distinct in the editor.
	poly := func(w, h float64) []*common.TechCardMarkerPoint {
		return []*common.TechCardMarkerPoint{{XCm: 0, YCm: 0}, {XCm: w, YCm: 0}, {XCm: w, YCm: h}, {XCm: 0, YCm: h}}
	}
	place := func(pieceID, instance int32, x, y float64) *common.TechCardMarkerPlacement {
		return &common.TechCardMarkerPlacement{PieceId: pieceID, Instance: instance, RotDeg: 0, XCm: x, YCm: y}
	}

	// --- 1. ЛЕГАСИ: one size, four комплектов, v2 blob. Sent the way the shipped bundle sends it.
	legacy := &common.TechCardMarkerInsert{
		SizeId: st.mID, Sets: 4, Name: "M · основная (легаси)", Source: "auto",
		BomLineKey:    st.bomFabricKey,
		FabricWidthCm: decv("150"), GapCm: decv("0.5"), EdgeMarginCm: decv("1"), SelvedgeCm: decv("1"),
		UsedLengthCm: decv("480"), EfficiencyPct: decv("71.5"),
		PlacedCount: 8, TotalCount: 8,
		Layout: &common.TechCardMarkerLayout{
			SchemaVersion: 2,
			Params:        &common.TechCardMarkerNestParams{Unit: "cm", TolCm: 0.1, RdpEpsCm: 0.05},
			Pieces: []*common.TechCardMarkerPiece{
				{PieceId: 1, Name: "FRONT_M", Source: "seed.dxf", Quantity: 1, Poly: poly(60, 70), BboxWCm: 60, BboxHCm: 70, AreaCm2: 4200, PieceLineKey: st.pieceFrontKey},
				{PieceId: 2, Name: "BACK_M", Source: "seed.dxf", Quantity: 1, Poly: poly(60, 75), BboxWCm: 60, BboxHCm: 75, AreaCm2: 4500, PieceLineKey: st.pieceBackKey},
			},
			// quantity 1 per garment × 4 комплектов = 4 instances each.
			Placements: []*common.TechCardMarkerPlacement{
				place(1, 0, 0, 0), place(2, 0, 0, 75), place(1, 1, 62, 0), place(2, 1, 62, 75),
				place(1, 2, 124, 0), place(2, 2, 124, 75), place(1, 3, 186, 0), place(2, 3, 186, 75),
			},
		},
	}
	legacyResp, err := s.C.SaveTechCardMarker(ctx, &admin.SaveTechCardMarkerRequest{TechCardId: sid, Marker: legacy})
	if err != nil {
		return fmt.Errorf("SaveTechCardMarker(легаси): %w", err)
	}

	// --- 2. СОСТАВ: M×2 + L×1, a v4 blob whose pieces carry sizes, plus one size-agnostic piece.
	//         Instances: front_M 1×2 + front_L 1×1 + pocket 1×3 (total_units) = 6.
	mixed := &common.TechCardMarkerInsert{
		Name: "M×2 + L×1 (состав)", Source: "auto", BomLineKey: st.bomFabricKey,
		FabricWidthCm: decv("150"), GapCm: decv("0.5"), EdgeMarginCm: decv("1"), SelvedgeCm: decv("1"),
		UsedLengthCm: decv("330"), EfficiencyPct: decv("78.25"),
		PlacedCount: 6, TotalCount: 6,
		// УСЛОВИЯ СЪЁМКИ (Ф3). Only THIS marker carries them; «однородная» above deliberately does not,
		// so beta holds one раскладка of each category — a labelled measurement and a «старая норма» —
		// and the difference is observable without driving the modal by hand.
		//
		// contour_allowance_mm = 0 is a MEASUREMENT («the laid contour IS the seam line»), not an
		// absence, and grain_layer = "" is a DECISION («не разворачивать»), not «не записано». Both are
		// here precisely because they are the two values a wire that only carried presence-by-value
		// would silently destroy.
		SeamAllowanceMm:    decv("1"),
		ContourAllowanceMm: decv("0"),
		ContourLayer:       strp("14"),
		GrainLayer:         strp(""),
		AllowFlip:          boolp(false),
		Layout: &common.TechCardMarkerLayout{
			SchemaVersion: 4,
			Params:        &common.TechCardMarkerNestParams{Unit: "cm", TolCm: 0.1, RdpEpsCm: 0.05},
			Composition: []*common.TechCardMarkerCompositionEntry{
				{SizeId: st.mID, Quantity: 2}, {SizeId: st.lID, Quantity: 1},
			},
			Pieces: []*common.TechCardMarkerPiece{
				{PieceId: 1, Name: "FRONT_M", Source: "seed.dxf", Quantity: 1, SizeId: st.mID, Poly: poly(60, 70), BboxWCm: 60, BboxHCm: 70, AreaCm2: 4200, PieceLineKey: st.pieceFrontKey},
				{PieceId: 2, Name: "FRONT_L", Source: "seed.dxf", Quantity: 1, SizeId: st.lID, Poly: poly(64, 74), BboxWCm: 64, BboxHCm: 74, AreaCm2: 4736, PieceLineKey: st.pieceFrontKey},
				// No размерный хвост in the block name: this piece does not grade, so one is cut per
				// garment of the WHOLE состав — the `: total_units` half of the formula.
				{PieceId: 3, Name: "POCKET", Source: "seed.dxf", Quantity: 1, Poly: poly(16, 18), BboxWCm: 16, BboxHCm: 18, AreaCm2: 288},
			},
			Placements: []*common.TechCardMarkerPlacement{
				place(1, 0, 0, 0), place(1, 1, 0, 72), place(2, 0, 62, 0),
				place(3, 0, 128, 0), place(3, 1, 146, 0), place(3, 2, 164, 0),
			},
		},
	}
	mixedResp, err := s.C.SaveTechCardMarker(ctx, &admin.SaveTechCardMarkerRequest{TechCardId: sid, Marker: mixed})
	if err != nil {
		return fmt.Errorf("SaveTechCardMarker(состав): %w", err)
	}

	// NEGATIVE: a v4 field under a v3 declaration is a client writing a format it does not speak.
	// Fired on a clone so it never touches the two real markers.
	neg := proto.Clone(mixed).(*common.TechCardMarkerInsert)
	neg.Name = "QA negative · состав под v3"
	neg.Layout.SchemaVersion = 3
	_, negErr := s.C.SaveTechCardMarker(ctx, &admin.SaveTechCardMarkerRequest{TechCardId: sid, Marker: neg})
	if e, ok := AsAPIError(negErr); !ok || e.Code != 400 {
		return fmt.Errorf("NEGATIVE composition under schema_version 3: expected HTTP 400, got %v", negErr)
	}
	s.pass(st, "NEGATIVE layout.composition под schema_version=3 отвергнут -> HTTP 400")

	// Read both back off the card and assert the two branches of the formula landed.
	r, err := s.C.GetTechCard(ctx, &admin.GetTechCardRequest{Id: sid})
	if err != nil {
		return fmt.Errorf("GetTechCard(markers): %w", err)
	}
	byID := map[int32]*common.TechCardMarkerSummary{}
	for _, m := range r.GetTechCard().GetMarkers() {
		byID[m.GetId()] = m
	}
	got := byID[legacyResp.GetId()]
	if got == nil {
		return fmt.Errorf("легаси marker %d missing from the card read", legacyResp.GetId())
	}
	if got.GetSizeId() != st.mID || got.GetSets() != 4 || got.GetTotalUnits() != 4 ||
		len(got.GetComposition()) != 1 || got.GetComposition()[0].GetQuantity() != 4 {
		return fmt.Errorf("легаси marker read back wrong: size_id=%d sets=%d total_units=%d composition=%v",
			got.GetSizeId(), got.GetSets(), got.GetTotalUnits(), got.GetComposition())
	}
	if got.GetScalarApplyRefusal() != "" || got.GetConsumptionPerUnitCm().GetValue() != "120" {
		return fmt.Errorf("легаси marker must still hand out its scalar norm: refusal=%q consumption=%q",
			got.GetScalarApplyRefusal(), got.GetConsumptionPerUnitCm().GetValue())
	}
	s.pass(st, "легаси раскладка id=%d читается как состав из одного размера (%d×%d), расход %s см/изд",
		got.GetId(), got.GetComposition()[0].GetSizeId(), got.GetComposition()[0].GetQuantity(),
		got.GetConsumptionPerUnitCm().GetValue())

	got = byID[mixedResp.GetId()]
	if got == nil {
		return fmt.Errorf("marker with a состав %d missing from the card read", mixedResp.GetId())
	}
	if got.GetSizeId() != 0 || got.GetSets() != 0 || got.GetTotalUnits() != 3 || len(got.GetComposition()) != 2 {
		return fmt.Errorf("состав marker read back wrong: size_id=%d sets=%d total_units=%d composition=%v",
			got.GetSizeId(), got.GetSets(), got.GetTotalUnits(), got.GetComposition())
	}
	if got.GetConsumptionPerUnitCm() != nil || got.GetScalarApplyRefusal() == "" {
		return fmt.Errorf("mixed раскладка must WITHHOLD its scalar norm: consumption=%v refusal=%q",
			got.GetConsumptionPerUnitCm(), got.GetScalarApplyRefusal())
	}
	s.pass(st, "раскладка со СОСТАВОМ id=%d: %d размеров, %d изделий, скалярный расход не выдан (%s)",
		got.GetId(), len(got.GetComposition()), got.GetTotalUnits(), got.GetScalarApplyRefusal())

	return s.plmMarkerConditions(ctx, st, legacyResp.GetId(), mixedResp.GetId(), mixed)
}

// plmMarkerConditions asserts the Ф3 half on the two markers just written: the conditions survive the
// trip WITH THEIR PRESENCE, the piece-set fingerprint is server-written, the double allowance is
// refused with words, and the norm is exclusive per cloth.
//
// It exists as its own function only because plmMarkers was already long; it is one step, not two.
func (s *Seeder) plmMarkerConditions(ctx context.Context, st *plmState, legacyID, mixedID int32,
	mixed *common.TechCardMarkerInsert) error {
	sid := st.res.StyleID
	s.step(st, "C.10c условия съёмки (Ф3): припуск/слои/переворот, отпечаток набора, норма")

	read := func(id int32) (*common.TechCardMarkerSummary, error) {
		r, err := s.C.GetTechCard(ctx, &admin.GetTechCardRequest{Id: sid})
		if err != nil {
			return nil, fmt.Errorf("GetTechCard(conditions): %w", err)
		}
		for _, m := range r.GetTechCard().GetMarkers() {
			if m.GetId() == id {
				return m, nil
			}
		}
		return nil, fmt.Errorf("marker %d missing from the card read", id)
	}

	m, err := read(mixedID)
	if err != nil {
		return err
	}
	if m.GetSeamAllowanceMm().GetValue() != "1" || m.GetContourLayer() != "14" {
		return fmt.Errorf("conditions lost in transit: seam=%q contour_layer=%q",
			m.GetSeamAllowanceMm().GetValue(), m.GetContourLayer())
	}
	// The two distinctions a careless wire destroys, checked by PRESENCE and not by value.
	if m.ContourAllowanceMm == nil || m.GetContourAllowanceMm().GetValue() != "0" {
		return fmt.Errorf("a MEASURED zero contour allowance must survive as a value, got %v", m.ContourAllowanceMm)
	}
	if m.GrainLayer == nil || m.GetGrainLayer() != "" {
		return fmt.Errorf("an EMPTY grain layer means «не разворачивать» and must survive, got %v", m.GrainLayer)
	}
	if m.AllowFlip == nil || m.GetAllowFlip() {
		return fmt.Errorf("a recorded flip policy of false must survive as false, got %v", m.AllowFlip)
	}
	if m.GetPieceSetStatus() != common.TechCardMarkerPieceSetStatus_TECH_CARD_MARKER_PIECE_SET_STATUS_MATCHES {
		return fmt.Errorf("the save must fingerprint the card's piece set, got %v", m.GetPieceSetStatus())
	}
	s.pass(st, "условия съёмки доехали: припуск %s, контур %s (замерено 0 = линия шва), долевая \"\" (не разворачивать), переворот запрещён, набор деталей СОВПАДАЕТ",
		m.GetSeamAllowanceMm().GetValue(), m.GetContourLayer())

	// The «старая норма» half: the legacy marker records no allowance and must NOT be badged as a
	// changed piece set either — the fingerprint is written by the SAVE regardless of what the client
	// knows about Ф3, so the two categories stay independent.
	l, err := read(legacyID)
	if err != nil {
		return err
	}
	if l.SeamAllowanceMm != nil {
		return fmt.Errorf("the legacy marker must record NO allowance, got %q", l.GetSeamAllowanceMm().GetValue())
	}
	if l.GetPieceSetStatus() != common.TechCardMarkerPieceSetStatus_TECH_CARD_MARKER_PIECE_SET_STATUS_MATCHES {
		return fmt.Errorf("the fingerprint is server-written and does not depend on the client knowing Ф3, got %v",
			l.GetPieceSetStatus())
	}
	s.pass(st, "легаси раскладка id=%d осталась «старой нормой» (припуск не записан), но отпечаток набора у неё есть", legacyID)

	// NEGATIVE: the laid contour already carries the allowance AND an offset was added on top. The
	// allowance would be counted twice and the spread length overstated around every perimeter.
	neg := proto.Clone(mixed).(*common.TechCardMarkerInsert)
	neg.Name = "QA negative · двойной припуск"
	neg.ContourAllowanceMm = decv("1")
	_, negErr := s.C.SaveTechCardMarker(ctx, &admin.SaveTechCardMarkerRequest{TechCardId: sid, Marker: neg})
	if e, ok := AsAPIError(negErr); !ok || e.Code != 400 {
		return fmt.Errorf("NEGATIVE double seam allowance: expected HTTP 400, got %v", negErr)
	}
	s.pass(st, "NEGATIVE двойной припуск (контур 1 мм + офсет 1 мм) отвергнут -> HTTP 400")

	// НОРМА. Designation is a dedicated RPC: a boolean on the save would arrive as false from a stale
	// bundle and clear the norm while knowing nothing about it.
	first, err := s.C.SetTechCardMarkerNorm(ctx, &admin.SetTechCardMarkerNormRequest{Id: mixedID, IsNorm: true})
	if err != nil {
		return fmt.Errorf("SetTechCardMarkerNorm(mixed): %w", err)
	}
	if first.GetPreviousNormMarkerId() != 0 || !first.GetRecipesNotRecalculated() {
		return fmt.Errorf("first designation: previous=%d recipes_not_recalculated=%v",
			first.GetPreviousNormMarkerId(), first.GetRecipesNotRecalculated())
	}
	second, err := s.C.SetTechCardMarkerNorm(ctx, &admin.SetTechCardMarkerNormRequest{Id: legacyID, IsNorm: true})
	if err != nil {
		return fmt.Errorf("SetTechCardMarkerNorm(legacy): %w", err)
	}
	if second.GetPreviousNormMarkerId() != mixedID {
		return fmt.Errorf("designating a sibling of the same cloth must name the norm it replaced, got %d",
			second.GetPreviousNormMarkerId())
	}
	m, err = read(mixedID)
	if err != nil {
		return err
	}
	l, err = read(legacyID)
	if err != nil {
		return err
	}
	if m.GetIsNorm() || !l.GetIsNorm() {
		return fmt.Errorf("exclusivity broken: mixed.is_norm=%v legacy.is_norm=%v", m.GetIsNorm(), l.GetIsNorm())
	}
	if l.GetNormConflict() != "" || m.GetNormConflict() != "" {
		return fmt.Errorf("a healthy card must report no norm conflict, got %q / %q",
			l.GetNormConflict(), m.GetNormConflict())
	}
	// Put the norm back on the labelled раскладка: leaving beta with «старая норма» designated would
	// seed the exact state the readiness gate is supposed to reject.
	if _, err := s.C.SetTechCardMarkerNorm(ctx, &admin.SetTechCardMarkerNormRequest{Id: mixedID, IsNorm: true}); err != nil {
		return fmt.Errorf("SetTechCardMarkerNorm(restore): %w", err)
	}
	s.pass(st, "норма назначается отдельным RPC и эксклюзивна по ткани: переназначение вернуло прежнюю (id=%d), рецепты не пересчитаны", mixedID)
	return nil
}

// ========================================================================= D. colourways + recipe

func (s *Seeder) plmColorways(ctx context.Context, st *plmState) error {
	sid := st.res.StyleID
	s.step(st, "D.12 two colourways on one style + variants (m,l) each + recipe (usages keyed by line_key)")

	cw1, err := s.createColorway(ctx, st, st.color1, "Black", st.media[0], st.media[1])
	if err != nil {
		return err
	}
	cw2, err := s.createColorway(ctx, st, st.color2, "Alt", st.media[1], st.media[2])
	if err != nil {
		return err
	}
	st.res.Colorway1ID, st.res.Colorway2ID = cw1, cw2
	s.pass(st, "D.12 colourways: cw1=%d(%s) cw2=%d(%s)", cw1, st.color1, cw2, st.color2)

	// variants (m,l) on each colourway. SKUs stay NULL until publish.
	for _, cw := range []int32{cw1, cw2} {
		for _, sz := range []int32{st.mID, st.lID} {
			if _, err := s.C.CreateVariant(ctx, &admin.CreateVariantRequest{ColorwayId: int64(cw), SizeId: sz}); err != nil {
				return fmt.Errorf("CreateVariant(cw=%d size=%d): %w", cw, sz, err)
			}
		}
	}
	s.pass(st, "D.12 variants (m,l) created on both colourways")

	// recipe (usages) per colourway, keyed by bom/piece line_key. Slots: cw1 inherits the slot
	// defaults; cw2 PINS the alt shell fabric (usage.material_id) — the divergence the whole
	// slots model exists for, so beta always carries one pinned colourway to exercise the
	// pin-first resolution in costing and the material plan.
	for _, cw := range []int32{cw1, cw2} {
		cwCopy := cw
		var shellPin *int64
		if cw == cw2 {
			shellPin = p64(st.res.MaterialIDs.FabricAlt)
		}
		if err := s.withLock(ctx, sid, func(lv uint64) error {
			_, e := s.C.UpdateColorwayRecipe(ctx, &admin.UpdateColorwayRecipeRequest{
				ColorwayId:              cwCopy,
				ExpectedColorwayVersion: int32(lv),
				Usages: []*common.TechCardColorwayUsage{
					{BomLineKey: st.bomFabricKey, Placement: "outer shell", Color: "as dictionary", Consumption: decv("1.4"), PieceLineKey: st.pieceFrontKey, MaterialId: shellPin},
					{BomLineKey: st.bomFabricKey, Placement: "outer shell", Color: "as dictionary", Consumption: decv("1.1"), PieceLineKey: st.pieceBackKey, MaterialId: shellPin},
					{BomLineKey: st.bomHardwareKey, Placement: "front placket", Color: "gunmetal", Quantity: decv("1")},
				},
			})
			return e
		}); err != nil {
			return fmt.Errorf("UpdateColorwayRecipe(cw=%d): %w", cw, err)
		}
	}
	s.pass(st, "D.12 recipe written for both colourways (cw2 pins alt shell fabric %d)", st.res.MaterialIDs.FabricAlt)

	// D.12b map cut-piece -> fabric per colourway.
	s.step(st, "D.12b map cut-piece fabric per colourway -> cut-list fabrics[] resolves both")
	tc, err := s.tcFetch(ctx, sid)
	if err != nil {
		return err
	}
	for _, p := range tc.GetPieces() {
		if p.GetLineKey() == st.pieceFrontKey || p.GetLineKey() == st.pieceBackKey {
			p.Materials = []*common.TechCardPieceColorwayMaterial{
				{ColorwayId: int64(cw1), BomLineKey: st.bomFabricKey, Note: "cw1 shell"},
				{ColorwayId: int64(cw2), BomLineKey: st.bomFabricKey, Note: "cw2 shell"},
			}
		}
	}
	if err := s.tcSave(ctx, sid, tc, "D.12b piece<->colourway map"); err != nil {
		return err
	}
	cl, err := s.C.GetStyleCutList(ctx, &admin.GetStyleCutListRequest{TechCardId: sid})
	if err != nil {
		return fmt.Errorf("GetStyleCutList(post-map): %w", err)
	}
	fabricsOnFront := 0
	for _, p := range cl.GetPieces() {
		if p.GetName() == "front panel" {
			fabricsOnFront = len(p.GetFabrics())
		}
	}
	if fabricsOnFront < 2 {
		s.warn(st, "D.12b front panel resolved %d per-colourway fabrics (want >=2)", fabricsOnFront)
	} else {
		s.pass(st, "D.12b front panel resolves %d per-colourway fabric mappings", fabricsOnFront)
	}

	// D.13 both DRAFT + usages readback + derived composition.
	s.step(st, "D.13 both colourways DRAFT + usages read-path + derived style composition")
	c1, err := s.C.GetColorwayByID(ctx, &admin.GetColorwayByIDRequest{ColorwayId: cw1})
	if err != nil {
		return fmt.Errorf("GetColorwayByID(cw1): %w", err)
	}
	c2, err := s.C.GetColorwayByID(ctx, &admin.GetColorwayByIDRequest{ColorwayId: cw2})
	if err != nil {
		return fmt.Errorf("GetColorwayByID(cw2): %w", err)
	}
	s1 := c1.GetColorway().GetColorway().GetStatus()
	s2 := c2.GetColorway().GetColorway().GetStatus()
	if s1 != common.ColorwayLifecycleStatus_COLORWAY_LIFECYCLE_STATUS_DRAFT || s2 != common.ColorwayLifecycleStatus_COLORWAY_LIFECYCLE_STATUS_DRAFT {
		return fmt.Errorf("expected both colourways DRAFT, got cw1=%s cw2=%s", s1, s2)
	}
	if len(c1.GetUsages()) < 1 {
		return fmt.Errorf("D.13/H1: GetColorwayByID(cw1).usages empty; recipe not readable back")
	}
	s.pass(st, "D.13 both DRAFT; cw1 usages read-path=%d line(s)", len(c1.GetUsages()))

	// derived style composition (best-effort; feature may or may not be live).
	if tr, err := s.C.GetTechCard(ctx, &admin.GetTechCardRequest{Id: sid}); err == nil {
		ce := tr.GetTechCard().GetCompositionEntries()
		if len(ce) == 2 && decFloat(pctByFiber(ce, "COT")) == 70 && decFloat(pctByFiber(ce, "POL")) == 30 {
			s.pass(st, "C.11/M1 derived style composition COT70/POL30 (single fabric -> 100%%)")
		} else {
			s.warn(st, "derived composition_entries not as expected (len=%d) - feature may be off", len(ce))
		}
	}
	return nil
}

func (s *Seeder) createColorway(ctx context.Context, st *plmState, colorCode, suffix string, thumb, secondary int32) (int32, error) {
	r, err := s.C.CreateColorway(ctx, &admin.CreateColorwayRequest{
		StyleId:                   st.res.StyleID,
		Merchandising:             &common.ColorwayMerchandisingInsert{ColorCode: colorCode, CountryCode: st.country, MinTier: 0},
		ThumbnailMediaId:          thumb,
		SecondaryThumbnailMediaId: secondary,
		MediaIds:                  []int32{thumb, secondary},
		Tags:                      []*common.ColorwayTagInsert{{Tag: seedTag}},
		Prices:                    s.Prices(),
		Translations: []*common.ColorwayInsertTranslation{{
			LanguageId: s.LangID, Name: "PLM Seed Jacket " + suffix, Description: "Seed colourway " + suffix + " for the PLM acceptance run.",
		}},
		CountryCode: st.country,
	})
	if err != nil {
		return 0, fmt.Errorf("CreateColorway(%s): %w", suffix, err)
	}
	if r.GetColorwayId() == 0 {
		return 0, fmt.Errorf("CreateColorway(%s) returned colorway_id=0", suffix)
	}
	return r.GetColorwayId(), nil
}

// ========================================================================= E. samples + fittings

func (s *Seeder) plmSamples(ctx context.Context, st *plmState) error {
	sid := st.res.StyleID
	cw1 := st.res.Colorway1ID
	today := time.Now().Format("2006-01-02")

	// E.14 sample #1 + substitution + fitting -> REJECTED + 2 structured CRs.
	s.step(st, "E.14 sample#1 + substitution + fitting REJECTED + 2 structured change-requests")
	s1, err := s.C.AddSample(ctx, &admin.AddSampleRequest{Sample: &common.SampleInsert{
		TechCardId: sid, Purpose: "fit", SizeId: st.mID, ColorwayId: cw1, Status: "in_sewing",
		FabricSource: "sample", Notes: "round 1 fit sample", StartedAt: today,
	}})
	if err != nil {
		return fmt.Errorf("AddSample(round1): %w", err)
	}
	st.res.Sample1ID = s1.GetId()

	if _, err := s.C.AddSampleSubstitution(ctx, &admin.AddSampleSubstitutionRequest{Substitution: &common.SampleSubstitutionInsert{
		SampleId: st.res.Sample1ID, BomItemId: int32(st.res.FabricBomID),
		OriginalMaterialId: int32(st.res.MaterialIDs.Fabric), SubstitutedMaterialId: int32(st.res.MaterialIDs.FabricAlt),
		Reason: "main shell fabric out of stock, trial run with alt shell fabric", PlannedQty: decv("1.5"), ActualQty: decv("1.6"),
	}}); err != nil {
		return fmt.Errorf("AddSampleSubstitution: %w", err)
	}

	f1, err := s.C.AddFitting(ctx, &admin.AddFittingRequest{Fitting: &common.FittingInsert{
		TechCardId: sid, SampleId: st.res.Sample1ID, FittingDate: timestamppb.Now(), Comment: "round 1 fit review",
		Status: common.FittingStatus_FITTING_STATUS_DONE, Verdict: common.FittingVerdict_FITTING_VERDICT_REJECTED,
		Sizes: []*common.FittingSizeInsert{{SizeId: st.mID, FitNote: "shoulder tight"}}, MediaIds: []int32{st.media[0]},
	}})
	if err != nil {
		return fmt.Errorf("AddFitting(round1): %w", err)
	}
	st.res.Fitting1ID = f1.GetId()
	gf1, err := s.C.GetFitting(ctx, &admin.GetFittingRequest{Id: st.res.Fitting1ID})
	if err != nil {
		return fmt.Errorf("GetFitting(round1): %w", err)
	}
	round1 := gf1.GetFitting().GetFitting().GetRoundNumber()

	cr1, err := s.C.AddFittingChangeRequest(ctx, &admin.AddFittingChangeRequestRequest{ChangeRequest: &common.FittingChangeRequestInsert{
		FittingId: st.res.Fitting1ID, Target: "pattern", Note: "shoulder seam too tight across front panel", Zone: "outer", PieceId: st.pieceFrontID, Status: "open",
	}})
	if err != nil {
		return fmt.Errorf("AddFittingChangeRequest(CR1): %w", err)
	}
	st.res.CR1ID = cr1.GetId()
	cr2, err := s.C.AddFittingChangeRequest(ctx, &admin.AddFittingChangeRequestRequest{ChangeRequest: &common.FittingChangeRequestInsert{
		FittingId: st.res.Fitting1ID, Target: "construction", Note: "back panel hem finish puckering", Zone: "lining", PieceId: st.pieceBackID, Status: "open",
	}})
	if err != nil {
		return fmt.Errorf("AddFittingChangeRequest(CR2): %w", err)
	}
	st.res.CR2ID = cr2.GetId()
	s.pass(st, "E.14 sample=%d (round %d) fitting=%d REJECTED; CR1=%d(outer/piece=%d) CR2=%d(lining/piece=%d)",
		st.res.Sample1ID, round1, st.res.Fitting1ID, st.res.CR1ID, st.pieceFrontID, st.res.CR2ID, st.pieceBackID)

	// E.15 sample #2 -> APPROVED; resolve CR1; carry over CR2.
	s.step(st, "E.15 sample#2 APPROVED (previous_sample_id chain); resolve CR1; carry over CR2")
	s2, err := s.C.AddSample(ctx, &admin.AddSampleRequest{Sample: &common.SampleInsert{
		TechCardId: sid, Purpose: "fit", SizeId: st.mID, ColorwayId: cw1, Status: "done", FabricSource: "sample",
		Notes: "round 2 fit sample, corrected shoulder seam", StartedAt: today, FinishedAt: today, PreviousSampleId: st.res.Sample1ID,
	}})
	if err != nil {
		return fmt.Errorf("AddSample(round2): %w", err)
	}
	st.res.Sample2ID = s2.GetId()
	f2, err := s.C.AddFitting(ctx, &admin.AddFittingRequest{Fitting: &common.FittingInsert{
		TechCardId: sid, SampleId: st.res.Sample2ID, FittingDate: timestamppb.Now(), Comment: "round 2 fit review",
		Status: common.FittingStatus_FITTING_STATUS_DONE, Verdict: common.FittingVerdict_FITTING_VERDICT_APPROVED,
		Sizes: []*common.FittingSizeInsert{{SizeId: st.mID, FitNote: "fit approved"}},
	}})
	if err != nil {
		return fmt.Errorf("AddFitting(round2): %w", err)
	}
	st.res.Fitting2ID = f2.GetId()
	gf2, err := s.C.GetFitting(ctx, &admin.GetFittingRequest{Id: st.res.Fitting2ID})
	if err != nil {
		return fmt.Errorf("GetFitting(round2): %w", err)
	}
	round2 := gf2.GetFitting().GetFitting().GetRoundNumber()
	if round2 <= round1 {
		s.warn(st, "expected round2(%d) > round1(%d) auto-numbering", round2, round1)
	}

	// resolve CR1 (full-replace of that stable item).
	if _, err := s.C.UpdateFittingChangeRequest(ctx, &admin.UpdateFittingChangeRequestRequest{
		Id: st.res.CR1ID, ChangeRequest: &common.FittingChangeRequestInsert{
			FittingId: st.res.Fitting1ID, Target: "pattern", Note: "shoulder seam too tight across front panel -- fixed in round 2 pattern",
			Zone: "outer", PieceId: st.pieceFrontID, Status: "resolved",
		},
	}); err != nil {
		return fmt.Errorf("UpdateFittingChangeRequest(resolve CR1): %w", err)
	}
	// carry CR2 forward into round 2 (still open).
	cr2b, err := s.C.AddFittingChangeRequest(ctx, &admin.AddFittingChangeRequestRequest{ChangeRequest: &common.FittingChangeRequestInsert{
		FittingId: st.res.Fitting2ID, Target: "construction", Note: "back panel hem finish still puckering, needs second pass",
		Zone: "lining", PieceId: st.pieceBackID, Status: "open", CarriedFromId: st.res.CR2ID,
	}})
	if err != nil {
		return fmt.Errorf("AddFittingChangeRequest(carry-over CR2): %w", err)
	}
	st.res.CR2CarryID = cr2b.GetId()

	open, err := s.C.ListOpenFittingChangeRequests(ctx, &admin.ListOpenFittingChangeRequestsRequest{TechCardId: sid})
	if err != nil {
		return fmt.Errorf("ListOpenFittingChangeRequests: %w", err)
	}
	hasCarry, hasCR1 := false, false
	for _, c := range open.GetChangeRequests() {
		if c.GetId() == st.res.CR2CarryID && c.GetCarriedFromId() == st.res.CR2ID {
			hasCarry = true
		}
		if c.GetId() == st.res.CR1ID {
			hasCR1 = true
		}
	}
	if !hasCarry {
		return fmt.Errorf("round-2 carry-over item not visible in open list")
	}
	if hasCR1 {
		return fmt.Errorf("resolved CR1 should not appear as open")
	}
	s.pass(st, "E.15 sample=%d (round %d) APPROVED; CR1 resolved (dropped from open); CR2 carried to CR2B=%d (open)", st.res.Sample2ID, round2, st.res.CR2CarryID)

	// E.16 iteration chain.
	s.step(st, "E.16 iteration chain (sample2.previous_sample_id -> sample1)")
	gs2, err := s.C.GetSample(ctx, &admin.GetSampleRequest{Id: st.res.Sample2ID})
	if err != nil {
		return fmt.Errorf("GetSample(2): %w", err)
	}
	if gs2.GetSample().GetSample().GetPreviousSampleId() != st.res.Sample1ID {
		return fmt.Errorf("expected sample2.previousSampleId=%d, got %d", st.res.Sample1ID, gs2.GetSample().GetSample().GetPreviousSampleId())
	}
	s.pass(st, "E.16 sample2.previousSampleId=%d (chain confirmed)", st.res.Sample1ID)
	return nil
}

// ========================================================================= spec release Rev.N

func (s *Seeder) plmRelease(ctx context.Context, st *plmState) error {
	sid := st.res.StyleID
	s.step(st, "spec release Rev.N: costing plan + approvalState=RELEASED, then freeze + reopen")
	tc, err := s.tcFetch(ctx, sid)
	if err != nil {
		return err
	}
	// hardware/packaging money is BOM-only since Phase 2 (the scalars were removed from the
	// contract): this card prices its zipper as a HARDWARE BOM line (C.10) and its polybag would be
	// a PACKAGING BOM line; the costing block carries only the genuinely manual articles.
	tc.Costing = &common.TechCardCosting{
		CmtCost:       decv("18.00"),
		LogisticsCost: decv("3.00"), OverheadCost: decv("4.00"), DefectPercent: decv("2"),
		Currency: "EUR", Notes: "PLM seed estimated costing plan",
	}
	tc.SizeQuantities = []*common.TechCardSizeQuantity{{SizeId: st.mID, OrderQty: 5}, {SizeId: st.lID, OrderQty: 5}}

	tc.ApprovalState = common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED
	if err := s.tcSave(ctx, sid, tc, "release: costing + RELEASED"); err != nil {
		return err
	}

	rl, err := s.C.ListTechCardReleases(ctx, &admin.ListTechCardReleasesRequest{TechCardId: sid})
	if err != nil {
		return fmt.Errorf("ListTechCardReleases: %w", err)
	}
	for _, r := range rl.GetReleases() {
		if r.GetReleaseNumber() == 1 {
			st.res.ReleaseID = r.GetId()
		}
	}
	if st.res.ReleaseID == 0 {
		return fmt.Errorf("no Rev.1 release snapshot: %d releases", len(rl.GetReleases()))
	}
	gr, err := s.C.GetTechCardRelease(ctx, &admin.GetTechCardReleaseRequest{Id: st.res.ReleaseID})
	if err != nil {
		return fmt.Errorf("GetTechCardRelease: %w", err)
	}
	st.res.ReleaseUnitCost = gr.GetRelease().GetUnitCost().GetValue()
	s.pass(st, "Rev.1 release id=%d unit_cost=%s EUR", st.res.ReleaseID, valOrNA(st.res.ReleaseUnitCost))

	// NEGATIVE: a stray edit while RELEASED must be rejected (FailedPrecondition -> 400).
	tc2, err := s.tcFetch(ctx, sid)
	if err != nil {
		return err
	}
	tc2.Notes = "stray edit while released"
	// Same reason as the bom_line_key probe: this one bypasses tcSave, and the fetched body carries
	// the card's machine facts, so it must state the capability or it is refused for the wrong reason.
	tc2.MachineFieldsAware = true
	tc2.AssemblyAware = true
	tc2.MediaAware = true
	tc2.OperationKindsAware = true
	tc2.OperationWorkAware = true
	strayLV, err := s.lockVersion(ctx, sid)
	if err != nil {
		return err
	}
	_, negErr := s.C.UpdateTechCard(ctx, &admin.UpdateTechCardRequest{Id: sid, ExpectedLockVersion: int32(strayLV), TechCard: tc2})
	if e, ok := AsAPIError(negErr); !ok || e.Code != 400 {
		return fmt.Errorf("NEGATIVE edit-while-released: expected HTTP 400, got %v", negErr)
	}
	s.pass(st, "NEGATIVE stray edit on RELEASED card rejected -> HTTP 400 (frozen-spec guard)")

	// reopen to DRAFT (K.31 hygiene writes need it later).
	tc3, err := s.tcFetch(ctx, sid)
	if err != nil {
		return err
	}
	tc3.ApprovalState = common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_DRAFT
	if err := s.tcSave(ctx, sid, tc3, "reopen to draft"); err != nil {
		return err
	}
	s.pass(st, "tech card reopened to DRAFT (Rev.1 snapshot immutable)")
	return nil
}

// ========================================================================= F.17 costing estimate

func (s *Seeder) plmCostingEstimate(ctx context.Context, st *plmState) error {
	s.step(st, "F.17 costing ESTIMATED (GetStyleCostEstimate before any run)")
	e, err := s.C.GetStyleCostEstimate(ctx, &admin.GetStyleCostEstimateRequest{TechCardId: st.res.StyleID, ColorwayId: int64(st.res.Colorway1ID)})
	if err != nil {
		return fmt.Errorf("GetStyleCostEstimate(pre-run): %w", err)
	}
	est := e.GetEstimate()
	if est.GetUnitCostBase().GetValue() == "" {
		return fmt.Errorf("estimate returned no unit_cost_base")
	}
	if est.GetComparison().GetHasActual() {
		s.warn(st, "expected has_actual=false before any run")
	}
	s.pass(st, "F.17 estimate.unit_cost_base=%s EUR; has_actual=false", est.GetUnitCostBase().GetValue())
	return nil
}

// ========================================================================= G. assembly + packaging

func (s *Seeder) plmAssembly(ctx context.Context, st *plmState) error {
	sid := st.res.StyleID
	s.step(st, "G.19 assembly: 4 auxiliary tech cards (brand/care/size label + hangtag) + UpsertStyleAssembly")

	subtypes := []common.TechCardAuxSubtype{
		common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_BRAND_LABEL,
		common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_CARE_LABEL,
		common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_SIZE_LABEL,
		common.TechCardAuxSubtype_TECH_CARD_AUX_SUBTYPE_HANGTAG,
	}
	names := []string{"Brand Label", "Care Label", "Size Label", "Hangtag"}
	var items []*admin.StyleAssemblyItem
	for i, sub := range subtypes {
		code := s.key(fmt.Sprintf("AUX%d", i))
		matID, err := s.createMaterial(ctx, &common.Material{
			Name: names[i] + " material", Section: common.TechCardBomSection_TECH_CARD_BOM_SECTION_LABEL,
			MaterialClass: common.MaterialClass_MATERIAL_CLASS_OTHER, Code: code, Unit: "pcs", MinStock: decv("200"), OtherAttrs: "{}",
		})
		if err != nil {
			return err
		}
		auxResp, err := s.C.CreateTechCard(ctx, &admin.CreateTechCardRequest{TechCard: &common.TechCardInsert{
			StyleNumber: code, StyleNumberSource: common.StyleNumberSource_STYLE_NUMBER_SOURCE_MANUAL,
			Name: names[i] + " (PLM seed)", Purpose: common.TechCardPurpose_TECH_CARD_PURPOSE_AUXILIARY,
			AuxSubtype: sub, OutputMaterialId: int32(matID),
			Stage: common.TechCardStage_TECH_CARD_STAGE_PROD, ApprovalState: common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_DRAFT,
			MachineFieldsAware:  true,
			AssemblyAware:       true,
			MediaAware:          true,
			OperationKindsAware: true,
			OperationWorkAware:  true,
		}})
		if err != nil {
			return fmt.Errorf("CreateTechCard(aux %s): %w", names[i], err)
		}
		st.res.AuxStyleIDs = append(st.res.AuxStyleIDs, auxResp.GetId())
		items = append(items, &admin.StyleAssemblyItem{
			ComponentTechCardId: auxResp.GetId(), SizeId: 0, Qty: decv("1"),
			PrintNote: names[i] + " print", PositionNote: "sewn-in, " + names[i] + " position", Active: true,
		})
	}
	if _, err := s.C.UpsertStyleAssembly(ctx, &admin.UpsertStyleAssemblyRequest{StyleId: sid, Items: items}); err != nil {
		return fmt.Errorf("UpsertStyleAssembly: %w", err)
	}
	la, err := s.C.ListStyleAssembly(ctx, &admin.ListStyleAssemblyRequest{StyleId: sid})
	if err != nil {
		return fmt.Errorf("ListStyleAssembly: %w", err)
	}
	if len(la.GetItems()) != 4 {
		return fmt.Errorf("expected 4 style-assembly lines, got %d", len(la.GetItems()))
	}
	s.pass(st, "G.19 4 auxiliary cards %v + assembly -> %d resolved lines", st.res.AuxStyleIDs, len(la.GetItems()))

	// G.20 packaging recipe scope=product: dust bag + box + insert.
	s.step(st, "G.20 packaging recipe scope=product (dust bag + box + insert) on colourway #1")
	m := &st.res.MaterialIDs
	if m.DustBag, err = s.createMaterial(ctx, &common.Material{
		Name: "Dust Bag - Cotton", Section: common.TechCardBomSection_TECH_CARD_BOM_SECTION_PACKAGING,
		MaterialClass: common.MaterialClass_MATERIAL_CLASS_PACKAGING, Code: s.key("DUSTBAG"), Unit: "pcs", MinStock: decv("100"),
		Attributes: &common.Material_PackagingAttrs{PackagingAttrs: &common.MaterialPackagingAttrs{Substrate: "cotton twill", Dimensions: "40x30cm", Gsm: decv("120"), PrintMethod: "screen 1-colour"}},
	}); err != nil {
		return err
	}
	if m.Insert, err = s.createMaterial(ctx, &common.Material{
		Name: "Care Insert Card", Section: common.TechCardBomSection_TECH_CARD_BOM_SECTION_PACKAGING,
		MaterialClass: common.MaterialClass_MATERIAL_CLASS_PACKAGING, Code: s.key("INSERT"), Unit: "pcs", MinStock: decv("100"),
		Attributes: &common.Material_PackagingAttrs{PackagingAttrs: &common.MaterialPackagingAttrs{Substrate: "recycled card", Dimensions: "10x15cm", Gsm: decv("300"), PrintMethod: "digital"}},
	}); err != nil {
		return err
	}
	if _, err := s.C.UpsertPackagingRecipe(ctx, &admin.UpsertPackagingRecipeRequest{
		Scope: "product", ProductId: st.res.Colorway1ID,
		Items: []*admin.PackagingRecipeItem{
			{MaterialId: int32(m.DustBag), QtyPerOrder: decv("0"), QtyPerItem: decv("1"), Active: true},
			{MaterialId: int32(m.Packaging), QtyPerOrder: decv("1"), QtyPerItem: decv("0"), Active: true},
			{MaterialId: int32(m.Insert), QtyPerOrder: decv("0"), QtyPerItem: decv("1"), Active: true},
		},
	}); err != nil {
		return fmt.Errorf("UpsertPackagingRecipe: %w", err)
	}
	s.pass(st, "G.20 packaging recipe: dust_bag=%d(1/item) box=%d(1/order) insert=%d(1/item)", m.DustBag, m.Packaging, m.Insert)

	// G.21 kit readable.
	s.step(st, "G.21 ListPackagingRecipe shows product-scoped rows")
	pr, err := s.C.ListPackagingRecipe(ctx, &admin.ListPackagingRecipeRequest{})
	if err != nil {
		return fmt.Errorf("ListPackagingRecipe: %w", err)
	}
	rows := 0
	for _, it := range pr.GetItems() {
		if it.GetScope() == "product" && it.GetProductId() == st.res.Colorway1ID {
			rows++
		}
	}
	if rows < 3 {
		return fmt.Errorf("expected >=3 product-scoped packaging rows, got %d", rows)
	}
	s.pass(st, "G.21 ListPackagingRecipe -> %d product-scoped rows", rows)
	return nil
}

// ========================================================================= H. production run + F.18

func (s *Seeder) plmProduction(ctx context.Context, st *plmState) error {
	sid := st.res.StyleID
	cw1 := st.res.Colorway1ID
	m := &st.res.MaterialIDs

	// H.22a receive garment material stock.
	s.step(st, "H.22a receive raw material stock (fabric/hardware/thread)")
	for _, r := range []struct {
		id  int64
		qty string
	}{{m.Fabric, "100"}, {m.Hardware, "100"}, {m.Thread, "20"}} {
		if err := s.receiveStock(ctx, r.id, r.qty); err != nil {
			return err
		}
	}
	s.pass(st, "H.22a received fabric=100m hardware=100pcs thread=20cones")

	// H.22b production run: plan -> create -> issue -> update -> receive.
	s.step(st, "H.22b production run: create -> material-plan -> issue -> update -> receive (cost_price)")
	cr, err := s.C.CreateProductionRun(ctx, &admin.CreateProductionRunRequest{Run: &common.ProductionRunInsert{
		TechCardId: sid, Status: common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED, Notes: "PLM seed production run",
		Lines: []*common.ProductionRunLine{{ProductId: cw1, SizeId: st.mID, PlannedQty: 5}, {ProductId: cw1, SizeId: st.lID, PlannedQty: 5}},
	}})
	if err != nil {
		return fmt.Errorf("CreateProductionRun: %w", err)
	}
	st.res.ProductionRunID = cr.GetId()
	mp, err := s.C.GetProductionRunMaterialPlan(ctx, &admin.GetProductionRunMaterialPlanRequest{RunId: st.res.ProductionRunID})
	if err != nil {
		return fmt.Errorf("GetProductionRunMaterialPlan: %w", err)
	}

	// Slots acceptance: a run of the PINNED colourway (cw2 pins the alt shell fabric in D.12)
	// must resolve that pin — the plan's rollup carries the alt article, and its contribution is
	// marked pinned. Before slots this run would silently request the DEFAULT fabric for cw2 —
	// the exact mixed-run bug the model exists to close. The run stays PLANNED (seed showcase).
	crPin, err := s.C.CreateProductionRun(ctx, &admin.CreateProductionRunRequest{Run: &common.ProductionRunInsert{
		TechCardId: sid, Status: common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED, Notes: "PLM seed slots pin run (cw2 → alt shell)",
		Lines: []*common.ProductionRunLine{{ProductId: st.res.Colorway2ID, SizeId: st.mID, PlannedQty: 4}},
	}})
	if err != nil {
		return fmt.Errorf("CreateProductionRun(pin run): %w", err)
	}
	mpPin, err := s.C.GetProductionRunMaterialPlan(ctx, &admin.GetProductionRunMaterialPlanRequest{RunId: crPin.GetId()})
	if err != nil {
		return fmt.Errorf("GetProductionRunMaterialPlan(pin run): %w", err)
	}
	pinInRows := false
	for _, r := range mpPin.GetRows() {
		if int64(r.GetMaterialId()) == st.res.MaterialIDs.FabricAlt {
			pinInRows = true
		}
	}
	pinInContribs := false
	for _, c := range mpPin.GetContributions() {
		if int64(c.GetMaterialId()) == st.res.MaterialIDs.FabricAlt && c.GetPinned() {
			pinInContribs = true
		}
	}
	if !pinInRows || !pinInContribs {
		return fmt.Errorf("slots: cw2 pin run must resolve alt fabric %d (rows=%v contribs=%v)",
			st.res.MaterialIDs.FabricAlt, pinInRows, pinInContribs)
	}
	s.pass(st, "H.22b-slots pin run=%d resolves alt fabric %d (rows+contribution pinned=true, blockers=%d)",
		crPin.GetId(), st.res.MaterialIDs.FabricAlt, len(mpPin.GetBlockers()))
	if _, err := s.C.IssueMaterialStock(ctx, &admin.IssueMaterialStockRequest{
		MaterialId: int32(m.Fabric), Quantity: decv("14"), ProductionRunId: st.res.ProductionRunID,
		OccurredAt: time.Now().Format("2006-01-02"), Comment: "PLM seed cutting issue",
	}); err != nil {
		return fmt.Errorf("IssueMaterialStock(fabric->run): %w", err)
	}
	gr, err := s.C.GetProductionRun(ctx, &admin.GetProductionRunRequest{Id: st.res.ProductionRunID})
	if err != nil {
		return fmt.Errorf("GetProductionRun(pre-update): %w", err)
	}
	runLV := gr.GetRun().GetLockVersion()
	if _, err := s.C.UpdateProductionRun(ctx, &admin.UpdateProductionRunRequest{
		Id: st.res.ProductionRunID, ExpectedLockVersion: &runLV,
		Run: &common.ProductionRunInsert{
			TechCardId: sid, Status: common.ProductionRunStatus_PRODUCTION_RUN_STATUS_IN_PROGRESS, Notes: "PLM seed production run",
			Lines: []*common.ProductionRunLine{
				{ProductId: cw1, SizeId: st.mID, PlannedQty: 5},
				{ProductId: cw1, SizeId: st.lID, PlannedQty: 5},
			},
			Costs: []*common.ProductionRunCost{
				{Kind: common.ProductionRunCostKind_PRODUCTION_RUN_COST_KIND_CMT, Description: "contract sewing, 10 units", Amount: decv("180.00"), Currency: "EUR"},
				{Kind: common.ProductionRunCostKind_PRODUCTION_RUN_COST_KIND_HARDWARE, Description: "zippers, 10 units", Amount: decv("9.00"), Currency: "EUR"},
			},
		},
	}); err != nil {
		return fmt.Errorf("UpdateProductionRun: %w", err)
	}
	// Receive through the REAL receipt command (the overhaul contract), not the deprecated
	// ReceiveProductionRun shim: counts ride on the receipt lines addressed by line_key, the
	// plan lines carry only the plan. This keeps the seeder exercising the same path the admin
	// client uses (and the same one prod operators run).
	grLines, err := s.C.GetProductionRun(ctx, &admin.GetProductionRunRequest{Id: st.res.ProductionRunID})
	if err != nil {
		return fmt.Errorf("GetProductionRun(pre-receipt): %w", err)
	}
	receiptLines := make([]*admin.PostProductionRunReceiptLineInput, 0, len(grLines.GetRun().GetRun().GetLines()))
	for _, ln := range grLines.GetRun().GetRun().GetLines() {
		receiptLines = append(receiptLines, &admin.PostProductionRunReceiptLineInput{
			LineKey: ln.GetLineKey(), GoodQty: 5, DefectQty: 0,
		})
	}
	receiptLV := grLines.GetRun().GetLockVersion()
	rc, err := s.C.PostProductionRunReceipt(ctx, &admin.PostProductionRunReceiptRequest{
		RunId:               st.res.ProductionRunID,
		Lines:               receiptLines,
		IdempotencyKey:      seedIdempotencyKey(),
		ExpectedLockVersion: &receiptLV,
		Note:                "PLM seed receipt",
		UpdateCostPrice:     true,
	})
	if err != nil {
		return fmt.Errorf("PostProductionRunReceipt: %w", err)
	}
	if !rc.GetCostPriceUpdated() {
		return fmt.Errorf("expected cost_price_updated=true after PostProductionRunReceipt")
	}
	gr2, err := s.C.GetProductionRun(ctx, &admin.GetProductionRunRequest{Id: st.res.ProductionRunID})
	if err != nil {
		return fmt.Errorf("GetProductionRun(post-receive): %w", err)
	}
	if gr2.GetRun().GetRun().GetStatus() != common.ProductionRunStatus_PRODUCTION_RUN_STATUS_RECEIVED {
		return fmt.Errorf("expected run status RECEIVED, got %s", gr2.GetRun().GetRun().GetStatus())
	}
	s.pass(st, "H.22b run=%d plan_rows=%d cost_price_updated=true status=RECEIVED", st.res.ProductionRunID, len(mp.GetRows()))

	// H.23 packaging inventory received + dust-bag baseline.
	s.step(st, "H.23 receive packaging stock (dust bag/box/insert) + capture dust-bag baseline")
	for _, r := range []struct {
		id  int64
		qty string
	}{{m.DustBag, "50"}, {m.Packaging, "50"}, {m.Insert, "50"}} {
		if err := s.receiveStock(ctx, r.id, r.qty); err != nil {
			return err
		}
	}
	db, err := s.C.GetMaterialStock(ctx, &admin.GetMaterialStockRequest{MaterialId: int32(m.DustBag)})
	if err != nil {
		return fmt.Errorf("GetMaterialStock(dust bag baseline): %w", err)
	}
	st.dustbagBaseline = decFloat(db.GetStock().GetOnHand())
	s.pass(st, "H.23 packaging received; dust_bag on_hand baseline=%.0f", st.dustbagBaseline)

	// F.18 costing actual after the run.
	s.step(st, "F.18 costing ACTUAL after run: has_actual=true")
	e, err := s.C.GetStyleCostEstimate(ctx, &admin.GetStyleCostEstimateRequest{TechCardId: sid, ColorwayId: int64(cw1)})
	if err != nil {
		return fmt.Errorf("GetStyleCostEstimate(post-run): %w", err)
	}
	cmp := e.GetEstimate().GetComparison()
	if !cmp.GetHasActual() {
		s.warn(st, "expected has_actual=true after ReceiveProductionRun")
	} else {
		s.pass(st, "F.18 has_actual=true; estimate=%s actual=%s variance=%s EUR",
			valOrNA(cmp.GetEstimateUnitCostBase().GetValue()), valOrNA(cmp.GetActualUnitCostBase().GetValue()), valOrNA(cmp.GetEstimateVsActual().GetValue()))
	}
	return nil
}

func (s *Seeder) receiveStock(ctx context.Context, materialID int64, qty string) error {
	_, err := s.C.ReceiveMaterialStock(ctx, &admin.ReceiveMaterialStockRequest{
		MaterialId: int32(materialID), Quantity: decv(qty), UnitCost: decv("5.00"), Currency: "EUR",
		Lot: "PLM-LOT-" + strconv.FormatInt(materialID, 10), SupplierDoc: "PLM-PO-" + s.Run,
		OccurredAt: time.Now().Format("2006-01-02"), Comment: "PLM seed receipt",
	})
	if err != nil {
		return fmt.Errorf("ReceiveMaterialStock(material=%d): %w", materialID, err)
	}
	return nil
}

// ========================================================================= I. publish + hero

func (s *Seeder) plmPublish(ctx context.Context, st *plmState) error {
	cw1, cw2 := st.res.Colorway1ID, st.res.Colorway2ID
	s.step(st, "I.24 publish both colourways -> ACTIVE + mint stock + storefront presence")

	sku1, err := s.publishColorway(ctx, cw1)
	if err != nil {
		return err
	}
	sku2, err := s.publishColorway(ctx, cw2)
	if err != nil {
		return err
	}
	st.res.Colorway1BaseSku, st.res.Colorway2BaseSku = sku1, sku2

	// mint variant stock (post-publish only) so orders in J have something to buy.
	for _, cw := range []int32{cw1, cw2} {
		full, err := s.C.GetColorwayByID(ctx, &admin.GetColorwayByIDRequest{ColorwayId: cw})
		if err != nil {
			return fmt.Errorf("GetColorwayByID(%d, post-publish): %w", cw, err)
		}
		for _, v := range full.GetColorway().GetVariants() {
			if _, err := s.C.UpdateVariantStock(ctx, &admin.UpdateVariantStockRequest{
				Mode: common.StockAdjustmentMode_STOCK_ADJUSTMENT_MODE_SET, Quantity: 10,
				Reason: common.StockChangeReason_STOCK_CHANGE_REASON_STOCK_COUNT, VariantId: int64(v.GetVariantId()),
			}); err != nil {
				return fmt.Errorf("UpdateVariantStock(variant=%d): %w", v.GetVariantId(), err)
			}
		}
	}

	// resolve cw1 variant ids + SKUs by size, and build the PLMResult variant list.
	full, err := s.C.GetColorwayByID(ctx, &admin.GetColorwayByIDRequest{ColorwayId: cw1})
	if err != nil {
		return fmt.Errorf("GetColorwayByID(cw1, resolve skus): %w", err)
	}
	for _, v := range full.GetColorway().GetVariants() {
		switch v.GetSizeId() {
		case st.mID:
			st.cw1VarMID, st.cw1VarMSku = v.GetVariantId(), v.GetVariantSku()
		case st.lID:
			st.cw1VarLID, st.cw1VarLSku = v.GetVariantId(), v.GetVariantSku()
		}
	}
	if st.cw1VarMSku == "" || st.cw1VarLSku == "" {
		return fmt.Errorf("variant SKUs still NULL after publish (m=%q l=%q)", st.cw1VarMSku, st.cw1VarLSku)
	}
	st.res.Colorway1Variants = []VariantResult{
		{VariantID: st.cw1VarMID, Sku: st.cw1VarMSku, SizeID: st.mID, SizeName: "m"},
		{VariantID: st.cw1VarLID, Sku: st.cw1VarLSku, SizeID: st.lID, SizeName: "l"},
	}

	// storefront presence (typed frontend paged read).
	found1, found2 := false, false
	if paged, err := s.C.SFGetColorwaysPaged(ctx, &frontend.GetColorwaysPagedRequest{Limit: 50, Offset: 0, OrderFactor: common.OrderFactor_ORDER_FACTOR_DESC}); err == nil {
		for _, c := range paged.GetColorways() {
			if c.GetBaseSku() == sku1 {
				found1 = true
			}
			if c.GetBaseSku() == sku2 {
				found2 = true
			}
		}
	}
	if !found1 || !found2 {
		s.warn(st, "storefront paged: cw1_present=%v cw2_present=%v (may lag)", found1, found2)
	}
	s.pass(st, "I.24 both colourways ACTIVE: cw1_sku=%s cw2_sku=%s; storefront cw1=%v cw2=%v", sku1, sku2, found1, found2)

	// I.25 hero.
	s.step(st, "I.25 hero features colourway #1 (GA4 skipped: analytics off on beta)")
	if _, err := s.C.AddHero(ctx, &admin.AddHeroRequest{Hero: &common.HeroFullInsert{
		Entities: []*common.HeroEntityInsert{
			{Type: common.HeroType_HERO_TYPE_MAIN, Main: &common.HeroMainInsert{
				Media: &common.HeroMedia{PortraitId: st.media[0], LandscapeId: st.media[1]}, ExploreLink: "/catalog",
				Translations: []*common.HeroCopyTranslation{{LanguageId: s.LangID, Tag: "plm-seed", Headline: "PLM Seed Hero", Body: "PLM acceptance run hero.", ExploreText: "Explore"}},
			}},
			{Type: common.HeroType_HERO_TYPE_FEATURED_PRODUCTS, FeaturedProducts: &common.HeroFeaturedProductsInsert{
				ProductIds: []int32{cw1}, ExploreLink: "/catalog",
				Translations: []*common.HeroCopyTranslation{{LanguageId: s.LangID, Headline: "Featured", ExploreText: "Shop the drop"}},
			}},
		},
		NavFeatured: &common.NavFeaturedInsert{
			Men:   &common.NavFeaturedEntityInsert{MediaId: st.media[2], FeaturedTag: seedTag, Translations: []*common.NavFeaturedEntityInsertTranslation{{LanguageId: s.LangID, ExploreText: "Shop men"}}},
			Women: &common.NavFeaturedEntityInsert{MediaId: st.media[2], FeaturedTag: seedTag, Translations: []*common.NavFeaturedEntityInsertTranslation{{LanguageId: s.LangID, ExploreText: "Shop women"}}},
		},
	}}); err != nil {
		return fmt.Errorf("AddHero: %w", err)
	}
	s.pass(st, "I.25 hero set (FEATURED_PRODUCTS -> colourway #1)")
	return nil
}

func (s *Seeder) publishColorway(ctx context.Context, cwID int32) (string, error) {
	var baseSku string
	err := s.withLock(ctx, s.currentStyleForColorway(ctx, cwID), func(lv uint64) error {
		r, e := s.C.PublishColorway(ctx, &admin.PublishColorwayRequest{ColorwayId: cwID, ExpectedVersion: lv})
		if e != nil {
			return e
		}
		pub := r.GetColorway()
		if pub.GetStatus() != common.ColorwayLifecycleStatus_COLORWAY_LIFECYCLE_STATUS_ACTIVE {
			return fmt.Errorf("colourway %d not ACTIVE after publish (status=%s)", cwID, pub.GetStatus())
		}
		baseSku = pub.GetBaseSku()
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("PublishColorway(%d): %w", cwID, err)
	}
	if baseSku == "" {
		return "", fmt.Errorf("PublishColorway(%d) minted empty base_sku", cwID)
	}
	return baseSku, nil
}

// currentStyleForColorway resolves the owning style id so withLock can read the
// shared lock_version. Both PLM colourways belong to the same PLM style.
func (s *Seeder) currentStyleForColorway(ctx context.Context, cwID int32) int32 {
	if r, err := s.C.GetColorwayByID(ctx, &admin.GetColorwayByIDRequest{ColorwayId: cwID}); err == nil {
		if sid := r.GetColorway().GetColorway().GetStyleId(); sid != 0 {
			return sid
		}
	}
	return 0
}

// ========================================================================= J. orders + fulfillment

func (s *Seeder) plmOrders(ctx context.Context, st *plmState) error {
	cw1 := st.res.Colorway1ID

	// J.26 order A (storefront, CARD_TEST): 2 positions -> reserve (stock drops).
	s.step(st, "J.26 order A (storefront, 2 items) -> reserve; product stock drops, packaging reserve soft")
	qMBefore, qLBefore, err := s.variantQtys(ctx, cw1, st.cw1VarMID, st.cw1VarLID)
	if err != nil {
		return err
	}
	orderA, statusA, err := s.submitStorefrontOrder(ctx, st)
	if err != nil {
		return err
	}
	st.res.OrderAUUID = orderA
	qMAfter, qLAfter, err := s.variantQtys(ctx, cw1, st.cw1VarMID, st.cw1VarLID)
	if err != nil {
		return err
	}
	if qMBefore-qMAfter != 1 || qLBefore-qLAfter != 1 {
		s.warn(st, "order A reserve delta: M %.0f->%.0f L %.0f->%.0f (want -1 each)", qMBefore, qMAfter, qLBefore, qLAfter)
	}
	dbReserve, err := s.dustbagOnHand(ctx, st)
	if err != nil {
		return err
	}
	if dbReserve != st.dustbagBaseline {
		s.warn(st, "dust-bag on_hand moved on soft reserve: %.0f->%.0f", st.dustbagBaseline, dbReserve)
	}
	s.pass(st, "J.26 order A=%s (%s): stock M %.0f->%.0f L %.0f->%.0f; dust_bag unchanged @%.0f", orderA, statusA, qMBefore, qMAfter, qLBefore, qLAfter, dbReserve)

	// J.26b order B (admin custom, bank_invoice, born Confirmed).
	s.step(st, "J.26b order B (admin custom, bank_invoice) born Confirmed; resolve item ids by size")
	cb, err := s.C.CreateCustomOrder(ctx, &admin.CreateCustomOrderRequest{
		Items: []*common.CustomOrderItemInsert{
			{Quantity: 1, VariantId: int64(st.cw1VarMID), CustomPrice: decv("120.00")},
			{Quantity: 1, VariantId: int64(st.cw1VarLID), CustomPrice: decv("120.00")},
		},
		ShippingAddress: seedAddress(), BillingAddress: seedAddress(),
		Buyer:             &common.BuyerInsert{FirstName: "PLM", LastName: "SeedB", Email: fmt.Sprintf("plm-seed-b-%s@grbpwr.com", s.Run), Phone: "+49301234568"},
		PaymentMethod:     common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_BANK_INVOICE,
		ShipmentCarrierId: st.carrier, Currency: "eur",
	})
	if err != nil {
		return fmt.Errorf("CreateCustomOrder(orderB): %w", err)
	}
	st.res.OrderBUUID = cb.GetOrder().GetUuid()
	if st.res.OrderBUUID == "" {
		return fmt.Errorf("CreateCustomOrder(orderB) returned empty uuid")
	}
	ob, err := s.C.GetOrderByUUID(ctx, &admin.GetOrderByUUIDRequest{OrderUuid: st.res.OrderBUUID})
	if err != nil {
		return fmt.Errorf("GetOrderByUUID(orderB): %w", err)
	}
	for _, it := range ob.GetOrder().GetOrderItems() {
		switch it.GetSizeNameSnapshot() {
		case "m":
			st.orderBItemMID = it.GetId()
		case "l":
			st.orderBItemLID = it.GetId()
		}
	}
	if st.orderBItemMID == 0 || st.orderBItemLID == 0 {
		return fmt.Errorf("could not resolve order B item ids by size (m=%d l=%d)", st.orderBItemMID, st.orderBItemLID)
	}
	s.pass(st, "J.26b order B=%s status_id=%d item_m=%d item_l=%d", st.res.OrderBUUID, ob.GetOrder().GetOrder().GetOrderStatusId(), st.orderBItemMID, st.orderBItemLID)

	// J.27/28 ship order B -> packaging consume.
	s.step(st, "J.27/28 ship order B (SetTrackingNumber) -> packaging consume + packing spec")
	if _, err := s.C.SetTrackingNumber(ctx, &admin.SetTrackingNumberRequest{OrderUuid: st.res.OrderBUUID, TrackingCode: "PLM-TRACK-" + s.Run}); err != nil {
		return fmt.Errorf("SetTrackingNumber(orderB): %w", err)
	}
	dbShip, err := s.dustbagOnHand(ctx, st)
	if err != nil {
		return err
	}
	if dbReserve-dbShip != 2 {
		s.warn(st, "dust-bag consume delta: %.0f->%.0f (want -2)", dbReserve, dbShip)
	}
	if mv, err := s.C.ListMaterialMovements(ctx, &admin.ListMaterialMovementsRequest{MaterialId: int32(st.res.MaterialIDs.DustBag), MovementType: common.MaterialMovementType_MATERIAL_MOVEMENT_TYPE_WRITEOFF}); err == nil {
		found := false
		for _, mm := range mv.GetMovements() {
			if mm.GetReason() == "packaging" {
				found = true
			}
		}
		if !found {
			s.warn(st, "no reason=packaging WRITEOFF movement found for dust bag")
		}
	}
	if spec, err := s.C.GetOrderPackingSpec(ctx, &admin.GetOrderPackingSpecRequest{OrderUuid: st.res.OrderBUUID}); err == nil {
		s.pass(st, "J.27/28 dust_bag %.0f->%.0f (-2); packing-spec items=%d packaging=%d", dbReserve, dbShip, len(spec.GetItems()), len(spec.GetPackaging()))
	} else {
		s.warn(st, "GetOrderPackingSpec: %v", err)
	}

	// J.29 deliver + partial return (one item).
	s.step(st, "J.29 deliver order B + refund one item (partial return, L stock +1)")
	if _, err := s.C.DeliveredOrder(ctx, &admin.DeliveredOrderRequest{OrderUuid: st.res.OrderBUUID}); err != nil {
		return fmt.Errorf("DeliveredOrder(orderB): %w", err)
	}
	_, qLpre, err := s.variantQtys(ctx, cw1, st.cw1VarMID, st.cw1VarLID)
	if err != nil {
		return err
	}
	if _, err := s.C.RefundOrder(ctx, &admin.RefundOrderRequest{
		OrderUuid: st.res.OrderBUUID, OrderItemIds: []int32{st.orderBItemLID},
		Reason: "customer requested return of one item, PLM seed", RefundShipping: false,
		ReasonCode: admin.RefundReason_REFUND_REASON_CHANGED_MIND,
	}); err != nil {
		return fmt.Errorf("RefundOrder(orderB, one item): %w", err)
	}
	_, qLpost, err := s.variantQtys(ctx, cw1, st.cw1VarMID, st.cw1VarLID)
	if err != nil {
		return err
	}
	if qLpost-qLpre != 1 {
		s.warn(st, "refund restore delta: L %.0f->%.0f (want +1)", qLpre, qLpost)
	}
	s.pass(st, "J.29 order B delivered + partially refunded (item_l=%d); L stock %.0f->%.0f", st.orderBItemLID, qLpre, qLpost)

	// J.30 manual stock adjustment (DAMAGE) with audit trail.
	s.step(st, "J.30 manual stock adjustment (ADJUST/DECREASE, DAMAGE) on variant M + audit trail")
	if _, err := s.C.UpdateVariantStock(ctx, &admin.UpdateVariantStockRequest{
		Mode: common.StockAdjustmentMode_STOCK_ADJUSTMENT_MODE_ADJUST, Quantity: 1,
		Direction: common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_DECREASE,
		Reason:    common.StockChangeReason_STOCK_CHANGE_REASON_DAMAGE,
		Comment:   pstr("PLM seed: one unit found damaged on shelf"), VariantId: int64(st.cw1VarMID),
	}); err != nil {
		return fmt.Errorf("UpdateVariantStock(manual damage): %w", err)
	}
	hist, err := s.C.ListStockChangeHistory(ctx, &admin.ListStockChangeHistoryRequest{ColorwayId: cw1, SizeId: p32(st.mID), Limit: 5, OrderFactor: ofPtr(common.OrderFactor_ORDER_FACTOR_DESC)})
	if err != nil {
		return fmt.Errorf("ListStockChangeHistory: %w", err)
	}
	if len(hist.GetChanges()) == 0 {
		return fmt.Errorf("stock change history empty after manual adjustment")
	}
	latest := hist.GetChanges()[0]
	if latest.GetReason() != common.StockChangeReason_STOCK_CHANGE_REASON_DAMAGE || latest.GetAdminUsername() == "" {
		s.warn(st, "audit trail: reason=%s admin=%q", latest.GetReason(), latest.GetAdminUsername())
	}
	s.pass(st, "J.30 manual DAMAGE adjustment; audit reason=%s admin=%s", latest.GetReason(), latest.GetAdminUsername())

	// J release: cancel order A (never shipped) -> reserve release.
	s.step(st, "J release: cancel order A -> both positions restored; dust_bag unchanged")
	qMbc, qLbc, err := s.variantQtys(ctx, cw1, st.cw1VarMID, st.cw1VarLID)
	if err != nil {
		return err
	}
	if _, err := s.C.CancelOrder(ctx, &admin.CancelOrderRequest{OrderUuid: st.res.OrderAUUID}); err != nil {
		return fmt.Errorf("CancelOrder(orderA): %w", err)
	}
	qMac, qLac, err := s.variantQtys(ctx, cw1, st.cw1VarMID, st.cw1VarLID)
	if err != nil {
		return err
	}
	if qMac-qMbc != 1 || qLac-qLbc != 1 {
		s.warn(st, "cancel release delta: M %.0f->%.0f L %.0f->%.0f (want +1 each)", qMbc, qMac, qLbc, qLac)
	}
	dbCancel, err := s.dustbagOnHand(ctx, st)
	if err != nil {
		return err
	}
	if dbCancel != dbShip {
		s.warn(st, "dust-bag on_hand moved on release: %.0f->%.0f", dbShip, dbCancel)
	}
	s.pass(st, "J release: order A cancelled; stock M %.0f->%.0f L %.0f->%.0f; dust_bag unchanged @%.0f (reserve->consume->return->release closed)", qMbc, qMac, qLbc, qLac, dbCancel)
	return nil
}

// submitStorefrontOrder places a 2-item storefront order (CARD_TEST, then CARD fallback).
func (s *Seeder) submitStorefrontOrder(ctx context.Context, st *plmState) (uuid, status string, err error) {
	email := fmt.Sprintf("plm-seed-a-%s@grbpwr.com", s.Run)
	items := []*common.OrderItemInsert{{Quantity: 1, VariantSku: st.cw1VarMSku}, {Quantity: 1, VariantSku: st.cw1VarLSku}}
	for _, pm := range []common.PaymentMethodNameEnum{
		common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST,
		common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD,
	} {
		vr, verr := s.C.SFValidateOrderItemsInsert(ctx, &frontend.ValidateOrderItemsInsertRequest{
			Items: items, ShipmentCarrierId: st.carrier, Country: "DE", PaymentMethod: pm, Currency: "eur",
		})
		if verr != nil || vr.GetPaymentIntentId() == "" {
			continue
		}
		sr, serr := s.C.SFSubmitOrder(ctx, &frontend.SubmitOrderRequest{
			Order: &common.OrderNew{
				Items: items, ShippingAddress: seedAddress(), BillingAddress: seedAddress(),
				Buyer:         &common.BuyerInsert{FirstName: "PLM", LastName: "SeedA", Email: email, Phone: "+49301234567"},
				PaymentMethod: pm, ShipmentCarrierId: st.carrier, Currency: "eur",
			},
			PaymentIntentId: vr.GetPaymentIntentId(),
		})
		if serr != nil {
			continue
		}
		return sr.GetOrderUuid(), sr.GetOrderStatus().String(), nil
	}
	return "", "", fmt.Errorf("storefront order failed for both CARD_TEST and CARD")
}

// variantQtys reads the on-hand quantities for two variants of a colourway.
func (s *Seeder) variantQtys(ctx context.Context, cwID, varM, varL int32) (qM, qL float64, err error) {
	full, err := s.C.GetColorwayByID(ctx, &admin.GetColorwayByIDRequest{ColorwayId: cwID})
	if err != nil {
		return 0, 0, fmt.Errorf("GetColorwayByID(%d): %w", cwID, err)
	}
	for _, v := range full.GetColorway().GetVariants() {
		switch v.GetVariantId() {
		case varM:
			qM = decFloat(v.GetQuantity())
		case varL:
			qL = decFloat(v.GetQuantity())
		}
	}
	return qM, qL, nil
}

func (s *Seeder) dustbagOnHand(ctx context.Context, st *plmState) (float64, error) {
	r, err := s.C.GetMaterialStock(ctx, &admin.GetMaterialStockRequest{MaterialId: int32(st.res.MaterialIDs.DustBag)})
	if err != nil {
		return 0, fmt.Errorf("GetMaterialStock(dust bag): %w", err)
	}
	return decFloat(r.GetStock().GetOnHand()), nil
}

// ========================================================================= K. hygiene + negative

func (s *Seeder) plmHygiene(ctx context.Context, st *plmState) error {
	sid := st.res.StyleID

	// K.31 orphan control: remove a callout/sketch, the piece survives detached.
	s.step(st, "K.31 orphan control: clear technicalMedia+callouts; front piece survives detached=true")
	tc, err := s.tcFetch(ctx, sid)
	if err != nil {
		return err
	}
	tc.TechnicalMedia = nil
	tc.Callouts = nil
	if err := s.tcSave(ctx, sid, tc, "K.31 remove callouts"); err != nil {
		return err
	}
	if tc, err = s.tcFetch(ctx, sid); err != nil {
		return err
	}
	var front *common.TechCardPiece
	for _, p := range tc.GetPieces() {
		if p.GetLineKey() == st.pieceFrontKey {
			front = p
		}
	}
	if front == nil {
		return fmt.Errorf("front panel piece silently dropped after callout removal")
	}
	if !front.GetDetached() {
		s.warn(st, "expected front piece.detached=true after callout removal, got %v", front.GetDetached())
	}
	s.pass(st, "K.31 front-panel piece survives (detached=%v) after callout/sketch removal", front.GetDetached())

	// K.31 BOM save-race: two concurrent UpdateTechCard on the same stale lock -> one 200, one 409.
	s.step(st, "K.31 concurrent save-race: one 200 + one 409 (Aborted), never two 200s")
	raceLV, err := s.lockVersion(ctx, sid)
	if err != nil {
		return err
	}
	base, err := s.tcFetch(ctx, sid)
	if err != nil {
		return err
	}
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			body := proto.Clone(base).(*common.TechCardInsert)
			body.Notes = fmt.Sprintf("race-%d-%s", idx, s.Run)
			// Both racers are new-generation clients; without the flag the loser would be refused by
			// the capability gate instead of by the lock, and the race would prove nothing.
			// ВСЕ ТРИ ФЛАГА, А НЕ ОДИН. `body` — клон прочитанной карточки, а чтение отдаёт узлы
			// сборки безусловно: с одним лишь machine-флагом щит сборки отвергал ОБА гонщика
			// (FailedPrecondition вместо 200 и 409), и финальная фаза сидера падала, ни разу не
			// проверив то, ради чего написана, — оптимистическую блокировку.
			body.MachineFieldsAware = true
			body.AssemblyAware = true
			body.MediaAware = true
			body.OperationKindsAware = true
			body.OperationWorkAware = true
			_, e := s.C.UpdateTechCard(ctx, &admin.UpdateTechCardRequest{Id: sid, ExpectedLockVersion: int32(raceLV), TechCard: body})
			if e == nil {
				codes[idx] = 200
			} else if ae, ok := AsAPIError(e); ok {
				codes[idx] = ae.Code
			} else {
				codes[idx] = -1
			}
		}(i)
	}
	wg.Wait()
	ok200, ok409 := 0, 0
	for _, c := range codes {
		switch c {
		case 200:
			ok200++
		case 409:
			ok409++
		}
	}
	if ok200 != 1 || ok409 != 1 {
		return fmt.Errorf("expected exactly one 200 and one 409 from concurrent save, got %v", codes)
	}
	s.pass(st, "K.31 concurrent save-race: one writer won (200), the other got ABORTED (409)")
	return nil
}

// --- small typed helpers ---

func decv(v string) *decimal.Decimal { return &decimal.Decimal{Value: v} }

// strp / boolp carry PRESENCE onto a proto3 `optional` field. The Ф3 conditions need them because ""
// and false are meaningful ANSWERS there — «не разворачивать» and «переворот запрещён» — and are not
// the same statement as «не записано».
func strp(v string) *string { return &v }

func boolp(v bool) *bool { return &v }

func p32(v int32) *int32 { return &v }

func p64(v int64) *int64 { return &v }

func pbool(v bool) *bool { return &v }

// pcs makes the 0275 marking explicitly PRESENT on the wire. The field is `optional` so a stale tab
// cannot erase it, which means an absent field says "carry the stored value" — the seeder has to be
// the new client, not the stale one, or its readback would assert nothing.
func pcs(v common.TechCardPieceCutSymmetry) *common.TechCardPieceCutSymmetry { return &v }

func pstr(v string) *string { return &v }

func ofPtr(v common.OrderFactor) *common.OrderFactor { return &v }

func decFloat(d *decimal.Decimal) float64 {
	if d == nil {
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(d.GetValue()), 64)
	if err != nil {
		return 0
	}
	return f
}

func pctByFiber(entries []*common.CompositionEntry, code string) *decimal.Decimal {
	for _, e := range entries {
		if strings.EqualFold(e.GetFiberCode(), code) {
			return e.GetPercent()
		}
	}
	return nil
}

func valOrNA(v string) string {
	if v == "" {
		return "n/a"
	}
	return v
}

// seedIdempotencyKey mints a receipt idempotency key in the server's required shape: exactly 26
// characters of [0-9A-Z] (an uppercase-Crockford-ULID-sized token). Crypto-random; the fallback
// (rand unavailable) still satisfies the charset by construction.
func seedIdempotencyKey() string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, 26)
	if _, err := rand.Read(b); err != nil {
		ts := strings.ToUpper(strconv.FormatInt(time.Now().UnixNano(), 36))
		return (ts + "00000000000000000000000000")[:26]
	}
	out := make([]byte, 26)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out)
}
