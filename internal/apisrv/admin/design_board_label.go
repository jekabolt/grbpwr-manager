package admin

// MOODBOARD LABELS (tmp/plans/flat-consistency/101-MOODBOARD-ROLES.md, owner 06.10, wave 11).
//
// The board is the one place a card's pictures live; a design_reference row is the SERVER'S LABEL on a
// board picture — the view of the garment it shows (front / back / side_l / side_r / side), or which
// detail slot it belongs to. Nobody «moves a picture into the input» any more: a person puts it on the
// board and, if needed, corrects one word on the tile.
//
// THE LADDER, per picture (designBoardLabelLadder):
//  1. chat.board_label (cheap, flash-lite): {purpose, view, faces, confidence}; a picture whose
//     purpose the form already states gets it as a given and answers the view only;
//  2. a target whose view the cheap model is not sure of → chat.board_read (sonnet-5.5): {view,
//     confidence, why}; sure → the view; not sure → `unsure` (empty role, never travels) and the
//     question card under the board asks the person;
//  3. AI off / a broken picture → `failed` (empty role; the tile's «view ▾» asks the person).
//
// A model error leaves the row PENDING: the next band read re-queues it once it is
// DesignBoardLabelStaleAfter old, at most DesignBoardLabelMaxAttempts times, then it is `failed`.
// Nothing spins, and every state is a word on the tile.
//
// WHAT NEVER HAPPENS: a model writing over a person's label (every store write carries the guard), a
// model's words reaching a prompt (model_caption is shown greyed as «model read · not sent» only), and
// the server writing the form's purpose (tech_card_media.role has no row key — the client applies
// proposed_purpose to an EMPTY purpose once).

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

const (
	// designBoardLabelSure — the cheap model's confidence at which its view is taken (101 §2.3).
	designBoardLabelSure = 0.7
	// designBoardReadSure — the strong model's: below it the person is asked.
	designBoardReadSure = 0.6
	// designBoardDetailSure — the detail read's: lower, because the owner wants the model to make the
	// detail itself rather than wait (R17 «раз фото есть — деталь нужна»); a wrong name is one tap
	// (measured 07.10 on card 38's strap photo: «asymmetric shoulder strap» at 0.55).
	designBoardDetailSure = 0.5

	designBoardLabelMaxTokens = 1200
	designBoardLabelEffort    = "low"
	designBoardReadMaxTokens  = 2500
	designBoardReadEffort     = "low"
	designBoardFlightMargin   = 10 * time.Second
	// designBoardLabelMaxPerSync — pictures one sync labels at most (the client caps a board at 24).
	designBoardLabelMaxPerSync = 24
	// designBoardLabelPerToken — pictures per token of the shared hourly window.
	designBoardLabelPerToken = 6
	// designBoardMaxSlotsShown — existing details shown to the detail read (101 §2.5: ≤ 6).
	designBoardMaxSlotsShown = 6
	// designBoardResyncEvery — a band read re-checks the board for unlabelled pictures (a legacy
	// card, a save that lost its task) at most this often per card.
	designBoardResyncEvery = 10 * time.Minute
)

// designBoardViewRules — the view vocabulary, ONE text for both models. LEFT / RIGHT IS NOT ASKED: the
// model says where the garment's front points in the picture (`faces`) and the flank is computed
// (entity.DesignBoardLabelAnswer.ResolvedView) — measured 07.10, both models named two identical left
// flanks once left and once right when asked for the flank itself.
const designBoardViewRules = `view — which side of the garment faces the camera:
- "front": the front of the garment (its face side: the opening, the buttons, the neckline, the fly);
- "back": the back of the garment;
- "side": a side (profile) view;
- "unclear": a three-quarter angle, several views in one picture, a folded or crumpled garment you cannot orient, or you are not sure.
faces — for a "side" view only: the edge of the PICTURE the front of the garment (the wearer's face, chest, toes) points to: "left" or "right". Look at the picture, not at the wearer's body.`

const designBoardLabelSystemPrompt = `You label ONE picture from a fashion designer's moodboard for a garment being designed.
Reply with ONE JSON object and nothing else:
{"purpose": "target" | "detail" | "material" | "mood", "view": "front" | "back" | "side" | "unclear", "faces": "left" | "right" | "", "confidence": 0.0-1.0}

purpose — what the picture is for:
- "target": the whole garment (or most of it) is the subject — a product photo, a photo of it worn where the garment is clearly the subject, a sketch or a technical flat of it;
- "detail": a close-up of ONE part of a garment — a collar, a cuff, a pocket, a hem, a seam, a closure, a strap;
- "material": fabric, colour, texture, trim or hardware on its own;
- "mood": atmosphere, a scene, styling, art — the garment is not the subject.
` + designBoardViewRules + `
For a picture that is not "target" answer "view": "unclear".
confidence — 0..1, how sure you are of the view (of the purpose, for a picture that is not "target").`

const designBoardReadViewSystemPrompt = `You decide which view of a garment ONE picture shows. The designer marked the picture as the garment being designed.
Reply with ONE JSON object and nothing else:
{"view": "front" | "back" | "side" | "unclear", "faces": "left" | "right" | "", "confidence": 0.0-1.0, "why": "at most 12 words"}

` + designBoardViewRules + `
Answer "unclear" only when the picture really does not show one view of the garment; then say why in "why".
confidence — 0..1, how sure you are of the view.`

// designBoardReadDetailSystemPrompt — the detail read (101 §2.5): the part a detail photo shows, and
// whether it is one of the card's details already. The name is a LABEL from the parts vocabulary — it
// is the only word of this answer that ever reaches a prompt (as the detail slot's name); the caption
// never does (101 §2.7).
const designBoardReadDetailSystemPrompt = `You read ONE close-up picture from a fashion designer's moodboard. It shows a detail of the garment being designed. Name the detail and decide whether it is one of the details the designer already has.
Reply with ONE JSON object and nothing else:
{"slot": <the number of an existing detail> | "new", "name": "1 to 3 lowercase words", "caption": "1 or 2 short sentences: what the picture shows", "confidence": 0.0-1.0}

name — the garment part, the way a pattern maker says it: "left cuff", "back yoke", "patch pocket", "collar", "front placket", "back vent", "shoulder strap". No colours, no materials, no style adjectives.
slot — the number of an existing detail when this picture shows the SAME part (another photo or angle of it); otherwise "new".
confidence — 0..1, how sure you are of the part (and of the match).`

// designBoardReadDetailUserPrompt — the existing details, each with its number and, when it has a
// photo, which picture of the call shows it (the first picture is always the one being read).
func designBoardReadDetailUserPrompt(slots []designBoardDetailSlot) string {
	var b strings.Builder
	b.WriteString("Picture 1 is the one to read.\n")
	if len(slots) == 0 {
		b.WriteString("Existing details: none.")
		return b.String()
	}
	b.WriteString("Existing details:\n")
	pic := 2
	for _, sl := range slots {
		fmt.Fprintf(&b, "- %d: %s", sl.ID, sl.Name)
		if sl.URL != "" {
			fmt.Fprintf(&b, " (picture %d)", pic)
			pic++
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// designBoardDetailSlot — an existing detail slot of the card as the detail read is shown it.
type designBoardDetailSlot struct {
	ID   int
	Name string
	URL  string // the newest photo labelled to it; "" when none
}

// designBoardLabelUserPrompt — the cheap call's user turn: the purpose as a given when the form states
// one, else the model proposes it.
func designBoardLabelUserPrompt(purpose entity.TechCardMediaRole) string {
	if purpose == entity.TechCardMediaRoleTarget {
		return `The designer marked this picture as the garment being designed ("purpose": "target"). Name its view.`
	}
	return `The designer has not said what this picture is for. Name its purpose and, for a target picture, its view.`
}

// designBoardChatter — the slice of the AI router the ladder uses (a fake in tests).
type designBoardChatter interface {
	Chat(ctx context.Context, purpose string, req aiprov.ChatRequest) (*aiprov.ChatResult, error)
	Enabled(purpose string) bool
}

// designBoardPicture — one picture on the board with the purpose the form states.
type designBoardPicture struct {
	MediaID int
	Purpose entity.TechCardMediaRole
}

// designBoardPictures — the card's board pictures (moodboard category, any kind but REFERENCE — the
// client's isBoardRow), one per media id, the first row's purpose.
func designBoardPictures(card *entity.TechCard) []designBoardPicture {
	if card == nil {
		return nil
	}
	seen := map[int]bool{}
	var out []designBoardPicture
	for _, m := range card.Media {
		if m.Category != entity.TechCardMediaCategoryMoodboard || m.Kind == entity.TechCardMediaReference ||
			m.MediaId <= 0 || seen[m.MediaId] {
			continue
		}
		seen[m.MediaId] = true
		out = append(out, designBoardPicture{MediaID: m.MediaId, Purpose: m.Role})
	}
	return out
}

// designBoardLabelTask — one picture the sync labels; Relabel re-arms a model row whose purpose changed.
type designBoardLabelTask struct {
	designBoardPicture
	Relabel bool
	// Existing — the picture already has a model row (a relabel or a stale pending one).
	Existing bool
	// CallsModel — the route this task starts at is on (decided once per sync, before the token).
	CallsModel bool
}

// designBoardLabelled — whether a purpose takes a model label. A purpose of `detail` waits for the
// detail read (Ф2): until then a detail picture is labelled only by a person.
func designBoardLabelled(p entity.TechCardMediaRole, details bool) bool {
	switch p {
	case entity.TechCardMediaRoleNone, entity.TechCardMediaRoleTarget:
		return true
	case entity.TechCardMediaRoleDetail:
		return details
	}
	return false
}

// designBoardLabelPlan — THE DIFF after a save (pure): which board pictures to label, which model
// rows to drop. A person's row is never in either list.
//
//	board picture, no row, purpose takes a label          → label
//	model row, picture gone from the board                → drop
//	model row, purpose mood / material                    → drop, unless it is the settled «no view»
//	                                                         of the model's own proposal (it carries
//	                                                         proposed_purpose and costs nothing)
//	model row, purpose target, row labelled a detail / or
//	  settled with no view (a non-target proposal)        → relabel
//	model row, purpose detail, row labelled a view / or
//	  settled with no view                                 → relabel (when details are on)
//	model row, pending and stale                          → label (BeginBoardLabel re-arms or fails it)
func designBoardLabelPlan(board []designBoardPicture, refs []entity.DesignReference, staleBefore time.Time, details bool) (tasks []designBoardLabelTask, drop []int) {
	rowOf := make(map[int]entity.DesignReference, len(refs))
	for _, r := range refs {
		rowOf[r.MediaId] = r
	}
	onBoard := make(map[int]bool, len(board))
	for _, p := range board {
		onBoard[p.MediaID] = true
		r, has := rowOf[p.MediaID]
		if !has {
			if designBoardLabelled(p.Purpose, details) {
				tasks = append(tasks, designBoardLabelTask{designBoardPicture: p})
			}
			continue
		}
		if !entity.IsDesignLabelByModel(r.LabelSource) {
			continue
		}
		state := entity.DesignLabelStateOrOk(r.LabelState)
		isView := r.Role != "" && r.Role != entity.DesignViewDetail
		isDetail := r.Role == entity.DesignViewDetail
		settledEmpty := r.Role == "" && state == entity.DesignLabelStateOk
		switch {
		case p.Purpose == entity.TechCardMediaRoleMood || p.Purpose == entity.TechCardMediaRoleMaterial:
			if !settledEmpty {
				drop = append(drop, p.MediaID)
			}
		case p.Purpose == entity.TechCardMediaRoleTarget && (isDetail || settledEmpty):
			tasks = append(tasks, designBoardLabelTask{designBoardPicture: p, Relabel: true, Existing: true})
		case p.Purpose == entity.TechCardMediaRoleDetail && details && (isView || settledEmpty):
			tasks = append(tasks, designBoardLabelTask{designBoardPicture: p, Relabel: true, Existing: true})
		case p.Purpose == entity.TechCardMediaRoleDetail && !details && isView:
			drop = append(drop, p.MediaID)
		case state == entity.DesignLabelStatePending && (!r.LabelledAt.Valid || r.LabelledAt.Time.Before(staleBefore)):
			tasks = append(tasks, designBoardLabelTask{designBoardPicture: p, Existing: true})
		}
	}
	for _, r := range refs {
		if !onBoard[r.MediaId] && entity.IsDesignLabelByModel(r.LabelSource) {
			drop = append(drop, r.MediaId)
		}
	}
	sort.Ints(drop)
	return tasks, drop
}

// designBoardLabelDetails — whether a `detail` picture is read by the model (Ф2): it names the part and
// joins an existing detail slot or mints one (101 §2.5). A var so the plan's test covers both.
var designBoardLabelDetails = true

// ─── the ladder ───

// designBoardLabelLadder runs the models for ONE picture and returns the label to write. err != nil =
// a model call failed or answered garbage: the row stays pending and is retried lazily.
func designBoardLabelLadder(ctx context.Context, ai designBoardChatter, pic designBoardPicture, url string, slots []designBoardDetailSlot) (entity.DesignBoardLabel, error) {
	out := entity.DesignBoardLabel{MediaId: pic.MediaID, Source: entity.DesignLabelSourceModelCheap}
	if pic.Purpose == entity.TechCardMediaRoleDetail {
		// A detail goes straight to the strong read: the cheap model has nothing to add.
		return designBoardDetailRead(ctx, ai, out, url, slots)
	}
	if !ai.Enabled(entity.AIPurposeBoardLabel) {
		out.State = entity.DesignLabelStateFailed
		return out, nil
	}
	res, err := ai.Chat(ctx, entity.AIPurposeBoardLabel, aiprov.ChatRequest{
		System: designBoardLabelSystemPrompt, User: designBoardLabelUserPrompt(pic.Purpose),
		ImageURLs: []string{url}, UserAsParts: true, JSONMode: true,
		MaxTokens: designBoardLabelMaxTokens, Effort: designBoardLabelEffort,
	})
	if err != nil {
		return out, fmt.Errorf("board label: %w", err)
	}
	ans, ok := entity.ParseDesignBoardLabelAnswer(res.Text)
	if !ok {
		return out, fmt.Errorf("board label: the answer is not the promised JSON")
	}
	out.LabelModel = res.Model
	purpose := pic.Purpose
	if purpose == entity.TechCardMediaRoleNone {
		out.ProposedPurpose = ans.Purpose
		purpose = entity.TechCardMediaRole(ans.Purpose)
	}
	if purpose != entity.TechCardMediaRoleTarget {
		// mood / material / a PROPOSED detail (read once the person's purpose says detail) / nothing
		// proposed: settled, no view.
		out.State = entity.DesignLabelStateOk
		return out, nil
	}
	if role, sure := designBoardSureView(ans, designBoardLabelSure); sure {
		out.Role, out.State = role, entity.DesignLabelStateOk
		return out, nil
	}
	// The cheap model is not sure of the view: the strong one looks.
	out.Source = entity.DesignLabelSourceModelStrong
	if !ai.Enabled(entity.AIPurposeBoardRead) {
		out.State = entity.DesignLabelStateUnsure
		return out, nil
	}
	res, err = ai.Chat(ctx, entity.AIPurposeBoardRead, aiprov.ChatRequest{
		System: designBoardReadViewSystemPrompt, User: `Which view of the garment is this?`,
		ImageURLs: []string{url}, UserAsParts: true, JSONMode: true,
		MaxTokens: designBoardReadMaxTokens, Effort: designBoardReadEffort,
	})
	if err != nil {
		return out, fmt.Errorf("board read: %w", err)
	}
	strong, ok := entity.ParseDesignBoardLabelAnswer(res.Text)
	if !ok {
		return out, fmt.Errorf("board read: the answer is not the promised JSON")
	}
	out.LabelModel = res.Model
	out.ModelCaption = strong.Why
	if role, sure := designBoardSureView(strong, designBoardReadSure); sure {
		out.Role, out.State = role, entity.DesignLabelStateOk
		return out, nil
	}
	out.State = entity.DesignLabelStateUnsure
	return out, nil
}

// designBoardDetailRead — the detail read of one picture: an existing slot, a new one by name (the store
// mints it made_by_model, deduplicated by name), or unsure (no slot is made; the person picks).
func designBoardDetailRead(ctx context.Context, ai designBoardChatter, out entity.DesignBoardLabel, url string, slots []designBoardDetailSlot) (entity.DesignBoardLabel, error) {
	out.Source = entity.DesignLabelSourceModelStrong
	if !ai.Enabled(entity.AIPurposeBoardRead) {
		out.State = entity.DesignLabelStateFailed
		return out, nil
	}
	images := []string{url}
	known := make(map[int]bool, len(slots))
	for _, sl := range slots {
		known[sl.ID] = true
		if sl.URL != "" {
			images = append(images, sl.URL)
		}
	}
	res, err := ai.Chat(ctx, entity.AIPurposeBoardRead, aiprov.ChatRequest{
		System: designBoardReadDetailSystemPrompt, User: designBoardReadDetailUserPrompt(slots),
		ImageURLs: images, UserAsParts: true, JSONMode: true,
		MaxTokens: designBoardReadMaxTokens, Effort: designBoardReadEffort,
	})
	if err != nil {
		return out, fmt.Errorf("board detail read: %w", err)
	}
	ans, ok := entity.ParseDesignBoardLabelAnswer(res.Text)
	if !ok {
		return out, fmt.Errorf("board detail read: the answer is not the promised JSON")
	}
	out.LabelModel = res.Model
	out.ModelCaption = ans.Caption
	switch {
	case ans.Confidence < designBoardDetailSure:
	case ans.SlotID > 0 && known[ans.SlotID]:
		// The name rides along: should the slot be deleted meanwhile, the store joins / mints by it.
		out.Role, out.DetailSlotId, out.NewDetailName, out.State = entity.DesignViewDetail, ans.SlotID, ans.Name, entity.DesignLabelStateOk
		return out, nil
	case ans.Name != "":
		// "new" — or a slot number that is not one of ours, read as new by the name (the store joins an
		// existing slot of the same name instead of minting «(2)»).
		out.Role, out.NewDetailName, out.State = entity.DesignViewDetail, ans.Name, entity.DesignLabelStateOk
		return out, nil
	}
	out.State = entity.DesignLabelStateUnsure
	return out, nil
}

// designBoardSureView — the view a model answer settles at this confidence; a side view's flank comes
// from where its front points, else `side` (101 Q2: no question asked for L/R).
func designBoardSureView(a entity.DesignBoardLabelAnswer, sure float64) (string, bool) {
	if a.View == "" || a.View == "unclear" || a.Confidence < sure {
		return "", false
	}
	return a.ResolvedView(), true
}

// ─── the sync: one goroutine per card, re-run when a save lands while it works ───

// designBoardLabeller — per-card sync state. Zero value works.
type designBoardLabeller struct {
	// live — the background sync is on. Only New() sets it: a Server built as a literal (every handler
	// test) never starts a goroutine that would outlive its test and touch its mocks.
	live     bool
	mu       sync.Mutex
	running  map[int]bool
	again    map[int]bool
	lastSync map[int]time.Time
}

// kick starts a sync of the card unless one is running (then it runs once more after it). Never blocks.
func (s *Server) designBoardLabelKick(ctx context.Context, cardID int) {
	// No router (a test server): nothing. AI OFF IS NOT A REASON TO SKIP — the sync then still drops
	// what left the board and settles a stale pending label as `failed` without calling anybody
	// (Codex Ф1 #2: otherwise «…» stays on the tile forever).
	if !s.boardLabels.live || cardID <= 0 || s.repo == nil || s.ai == nil {
		return
	}
	l := &s.boardLabels
	l.mu.Lock()
	if l.running == nil {
		l.running, l.again, l.lastSync = map[int]bool{}, map[int]bool{}, map[int]time.Time{}
	}
	if l.running[cardID] {
		l.again[cardID] = true
		l.mu.Unlock()
		return
	}
	l.running[cardID] = true
	l.lastSync[cardID] = time.Now()
	l.mu.Unlock()

	base := context.WithoutCancel(ctx)
	go func() {
		// The «again» check and the release of ownership happen in ONE critical section (Codex Ф1 #3):
		// a kick landing between them would set `again` on a goroutine that has already decided to stop.
		finish := func() bool {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.again[cardID] {
				l.again[cardID] = false
				l.lastSync[cardID] = time.Now()
				return false
			}
			delete(l.running, cardID)
			delete(l.again, cardID)
			return true
		}
		defer func() {
			if r := recover(); r != nil {
				slog.Default().ErrorContext(base, "design board labels: panic", slog.Int("tech_card_id", cardID),
					slog.String("panic", fmt.Sprint(r)))
				// No other goroutine of this card can exist while `running` is set, so the release is
				// ours to make.
				l.mu.Lock()
				delete(l.running, cardID)
				delete(l.again, cardID)
				l.mu.Unlock()
			}
		}()
		for {
			if err := s.designBoardLabelSync(base, cardID); err != nil {
				slog.Default().ErrorContext(base, "design board labels: sync failed",
					slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
			}
			if finish() {
				return
			}
		}
	}()
}

// designBoardLabelLazy — the band read's repair: a pending label older than the stale window (a lost
// task) or a board not looked at for designBoardResyncEvery kicks a sync.
func (s *Server) designBoardLabelLazy(ctx context.Context, cardID int, refs []entity.DesignReference) {
	staleBefore := time.Now().Add(-entity.DesignBoardLabelStaleAfter)
	stale := false
	for _, r := range refs {
		if entity.IsDesignLabelByModel(r.LabelSource) && r.LabelState == entity.DesignLabelStatePending &&
			(!r.LabelledAt.Valid || r.LabelledAt.Time.Before(staleBefore)) {
			stale = true
			break
		}
	}
	l := &s.boardLabels
	l.mu.Lock()
	last, seen := l.lastSync[cardID]
	l.mu.Unlock()
	if stale || !seen || time.Since(last) > designBoardResyncEvery {
		s.designBoardLabelKick(ctx, cardID)
	}
}

// designBoardLabelSync — read the card and its labels, drop what left, label what came. Pictures are
// labelled one after another (one semaphore slot at a time), so a board of twelve new pictures never
// starves the quiz or the parts of the shared semaphore.
func (s *Server) designBoardLabelSync(ctx context.Context, cardID int) error {
	card, err := s.repo.TechCards().GetTechCardById(ctx, cardID)
	if err != nil {
		return fmt.Errorf("read the card: %w", err)
	}
	refs, err := s.repo.Design().ListReferences(ctx, cardID)
	if err != nil {
		return fmt.Errorf("read the labels: %w", err)
	}
	staleBefore := time.Now().Add(-entity.DesignBoardLabelStaleAfter)
	tasks, drop := designBoardLabelPlan(designBoardPictures(card), refs, staleBefore, designBoardLabelDetails)
	if len(drop) > 0 {
		slots, err := s.repo.Design().DropBoardLabels(ctx, cardID, drop)
		if err != nil {
			return fmt.Errorf("drop labels: %w", err)
		}
		slog.Default().InfoContext(ctx, "design board labels: dropped", slog.Int("tech_card_id", cardID),
			slog.Any("media_ids", drop), slog.Int("slots", slots))
	}
	// WHICH ROUTE A TASK CALLS decides whether it may (Codex Ф2 #1): a detail goes straight to the
	// strong read, everything else starts at the cheap label. A task whose route is off settles an
	// existing row as `failed` for free; a new picture gets no row (the tile's «view ▾» says the same).
	routeOn := func(t designBoardLabelTask) bool {
		if t.Purpose == entity.TechCardMediaRoleDetail {
			return s.ai.Enabled(entity.AIPurposeBoardRead)
		}
		return s.ai.Enabled(entity.AIPurposeBoardLabel)
	}
	kept := tasks[:0]
	for _, t := range tasks {
		t.CallsModel = routeOn(t)
		if t.CallsModel || t.Existing {
			kept = append(kept, t)
		}
	}
	tasks = kept
	if len(tasks) > designBoardLabelMaxPerSync {
		// A board is capped on the client (24); a crafted payload is cut here (Codex Ф1 #1). The rest
		// is labelled by the next sync.
		tasks = tasks[:designBoardLabelMaxPerSync]
	}
	if len(tasks) == 0 {
		return nil
	}
	ids := make([]int, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.MediaID)
	}
	urls, attached, err := s.designBoardPictureURLs(ctx, ids)
	if err != nil {
		return fmt.Errorf("resolve the pictures: %w", err)
	}
	urlOf := make(map[int]string, len(attached))
	for i, id := range attached {
		urlOf[id] = urls[i]
	}
	actor := designActor(ctx)
	admin := authsrv.GetAdminUsername(ctx)
	paid := 0
	for _, t := range tasks {
		// THE HOURLY WINDOW, SHARED WITH THE QUIZ AND THE PARTS: one token per designBoardLabelPerToken
		// pictures that call a model (a picture costs ≈ $0.001, an unclear one or a detail ≈ $0.02),
		// taken BEFORE that batch's first claim. Refused → the rest stays unclaimed; the next save or
		// band read tries again.
		if t.CallsModel {
			if paid%designBoardLabelPerToken == 0 && !s.enhanceRuns.allow(admin) {
				slog.Default().WarnContext(ctx, "design board labels: the hourly window is full; labels wait",
					slog.Int("tech_card_id", cardID))
				return nil
			}
			paid++
		}
		claimed, err := s.repo.Design().BeginBoardLabel(ctx, entity.DesignBoardLabelBegin{
			TechCardId: cardID, MediaId: t.MediaID, Relabel: t.Relabel, StaleBefore: staleBefore, Actor: actor,
		})
		if err != nil {
			return fmt.Errorf("claim media %d: %w", t.MediaID, err)
		}
		if !claimed {
			continue
		}
		var slots []designBoardDetailSlot
		if t.Purpose == entity.TechCardMediaRoleDetail && t.CallsModel {
			// Read fresh per detail: the previous picture of this sync may have minted the slot this
			// one belongs to (two photos of one cuff → one slot).
			if slots, err = s.designBoardDetailSlots(ctx, cardID); err != nil {
				return err
			}
		}
		s.designBoardLabelOne(ctx, cardID, t.designBoardPicture, urlOf[t.MediaID], t.CallsModel, slots)
	}
	return nil
}

// designBoardLabelOne — the fences and the ladder for one claimed picture, then the write.
// aiOn is the sync's decision for this task, frozen: a task that took no hourly token never calls a
// model, even if the route comes back on while it works (Codex Ф1 r2).
func (s *Server) designBoardLabelOne(ctx context.Context, cardID int, pic designBoardPicture, url string, aiOn bool, slots []designBoardDetailSlot) {
	logAttrs := []any{slog.Int("tech_card_id", cardID), slog.Int("media_id", pic.MediaID), slog.String("purpose", string(pic.Purpose))}
	var label entity.DesignBoardLabel
	if !aiOn || url == "" || designBoardNotAPicture(url) {
		// AI off, no file, or a file a model cannot read as a picture: a person labels it.
		label = entity.DesignBoardLabel{MediaId: pic.MediaID, Source: entity.DesignLabelSourceModelCheap, State: entity.DesignLabelStateFailed}
	} else {
		budget := s.ai.ChainBudget(entity.AIPurposeBoardLabel, designBoardLabelMaxTokens) +
			s.ai.ChainBudget(entity.AIPurposeBoardRead, designBoardReadMaxTokens) + designBoardFlightMargin
		cctx, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		started := time.Now()
		var err error
		acquired := false
		// The slot is released by a DEFER (Codex Ф1 #4): a panicking provider must not keep it.
		func() {
			select {
			case s.enhanceSem <- struct{}{}:
				acquired = true
			case <-cctx.Done():
				return
			}
			defer func() { <-s.enhanceSem }()
			label, err = designBoardLabelLadder(cctx, s.ai, pic, url, slots)
		}()
		if !acquired {
			slog.Default().WarnContext(ctx, "design board labels: the assistant stayed busy; the label waits", logAttrs...)
			return
		}
		logAttrs = append(logAttrs, slog.Duration("took", time.Since(started)))
		if err != nil {
			slog.Default().ErrorContext(ctx, "design board labels: the model call failed; the label stays pending",
				append(logAttrs, slog.String("err", err.Error()))...)
			return
		}
	}
	label.TechCardId, label.MediaId = cardID, pic.MediaID
	saved, err := s.repo.Design().FinishBoardLabel(ctx, label)
	if err != nil {
		slog.Default().ErrorContext(ctx, "design board labels: cannot write the label",
			append(logAttrs, slog.String("err", err.Error()))...)
		return
	}
	slog.Default().InfoContext(ctx, "design board label", append(logAttrs,
		slog.String("role", label.Role), slog.String("state", label.State), slog.String("source", label.Source),
		slog.String("proposed_purpose", label.ProposedPurpose), slog.String("model", label.LabelModel),
		slog.Bool("written", saved != nil))...)
}

// designBoardNotAPicture — a board file a vision model cannot read (a PDF, a video): labelled by a person.
func designBoardNotAPicture(url string) bool {
	_, _, bad := designFirstNonPictureInput([]designInputMediaRef{{URL: url}})
	return bad
}

// designBoardDetailSlots — the card's detail slots for the detail read: name and the newest photo
// labelled to each (by media id — the upload order), at most designBoardMaxSlotsShown, newest slots
// first.
func (s *Server) designBoardDetailSlots(ctx context.Context, cardID int) ([]designBoardDetailSlot, error) {
	band, err := s.repo.Design().GetBand(ctx, cardID, 1)
	if err != nil {
		return nil, fmt.Errorf("read the detail slots: %w", err)
	}
	newest := map[int]int{}
	for _, r := range band.References {
		if r.Role == entity.DesignViewDetail && r.DetailSlotId.Valid && entity.DesignReferenceTravels(r) {
			if id := int(r.DetailSlotId.Int32); r.MediaId > newest[id] {
				newest[id] = r.MediaId
			}
		}
	}
	var out []designBoardDetailSlot
	var ids []int
	for _, sl := range band.Bench {
		// The flat, colourway-less detail bench only — the one a flat detail run reads (Codex Ф2 #2).
		if sl.ViewKey != entity.DesignViewDetail || sl.Kind != entity.DesignPictureKindFlat ||
			(sl.ColorwayId.Valid && sl.ColorwayId.Int32 != 0) || strings.TrimSpace(sl.DetailName.String) == "" {
			continue
		}
		out = append(out, designBoardDetailSlot{ID: sl.Id, Name: strings.TrimSpace(sl.DetailName.String)})
		if m := newest[sl.Id]; m > 0 {
			ids = append(ids, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > designBoardMaxSlotsShown {
		out = out[:designBoardMaxSlotsShown]
	}
	urls, attached, err := s.designBoardPictureURLs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("resolve the detail photos: %w", err)
	}
	urlOfMedia := make(map[int]string, len(attached))
	for i, id := range attached {
		urlOfMedia[id] = urls[i]
	}
	for i := range out {
		if m := newest[out[i].ID]; m > 0 && !designBoardNotAPicture(urlOfMedia[m]) {
			out[i].URL = urlOfMedia[m]
		}
	}
	return out, nil
}
