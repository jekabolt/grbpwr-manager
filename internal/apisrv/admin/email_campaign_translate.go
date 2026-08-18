package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/localeutil"
	"github.com/jekabolt/grbpwr-manager/internal/mail/campaignrender"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/jekabolt/grbpwr-manager/internal/translate"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// campaignTranslateChunkSize bounds how many strings ride in a single translator call. One
	// unbounded request per locale failed deterministically on large campaigns (provider output
	// limits, 60s HTTP timeout) and discarded every locale already paid for.
	campaignTranslateChunkSize = 25
	// maxCampaignTranslateStrings caps the strings auto-translate will spend on for one target
	// locale, so an absurdly large campaign is refused up front instead of firing dozens of
	// sequential model requests inside one RPC.
	maxCampaignTranslateStrings = 300

	// campaignTranslateModelUnavailableMsg is the THIRD copy of the same fault, and the reason it
	// exists: this feature rides the very same s.aiOps client and the very same model slug as the
	// note assistant and the tech-card draft. When the provider retired the default slug all three
	// died together — but only two of them said so. This one fell through to a nameless Internal,
	// on the button nobody happened to press.
	campaignTranslateModelUnavailableMsg = "campaign auto-translation is misconfigured: " + modelUnavailableAdviceMsg
)

var (
	// errCampaignNotDraft marks the precondition failure that used to surface as a generic 500
	// AFTER every LLM call had been paid for: the store only updates draft rows.
	errCampaignNotDraft = errors.New("only draft campaigns can be auto-translated")
	// errCampaignChangedDuringTranslate marks a concurrent edit landing while the model round-trips
	// were in flight; saving the pre-translation snapshot would silently revert that edit.
	errCampaignChangedDuringTranslate = errors.New("campaign changed while it was being translated")
)

// AutoTranslateEmailCampaign fills the non-English locales of a campaign (subject + block
// translations) from the English source via the LLM translator, for the admin to review before
// launch. It reuses the OpenRouter client already wired for AI drafting.
func (s *Server) AutoTranslateEmailCampaign(
	ctx context.Context,
	req *pb_admin.AutoTranslateEmailCampaignRequest,
) (*pb_admin.AutoTranslateEmailCampaignResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "campaign id is required")
	}
	translator := translate.New(s.aiOps)
	if !translator.Enabled() {
		return nil, aiRefusal(aiReasonNotConfigured, "translation is not configured (OPENROUTER_API_KEY unset)", nil)
	}
	n, err := autoTranslateCampaign(ctx, s.repo, translator, cache.GetLanguages(), int(req.GetId()), req.GetOverwrite())
	if err != nil {
		// model/base_url: the same blindness the other two consumers had. The slug reached the beta
		// log only because the provider echoed it in its own sentence, which was luck, not design.
		slog.ErrorContext(ctx, "auto-translate campaign failed",
			slog.String("model", s.aiOps.Model()), slog.String("base_url", s.aiOps.BaseURL()),
			slog.String("err", err.Error()))
		return nil, campaignTranslateError(err, s.aiOps.Model())
	}
	return &pb_admin.AutoTranslateEmailCampaignResponse{TranslatedCount: int32(n)}, nil
}

// campaignTranslateError maps a failure out of autoTranslateCampaign onto the status the caller
// sees. It is a separate function so the whole table can be tested without a campaign, a repository
// and a seeded language cache — the RPC reaches this switch only after several store round-trips,
// which is precisely why the branch below was missing for as long as it was.
func campaignTranslateError(err error, model string) error {
	switch {
	// FIRST, and disjoint from the rest: this one is not about the campaign at all. Every other
	// branch here describes a row in the database; this describes a setting in the deployment, and
	// no amount of editing the campaign will move it.
	case errors.Is(err, openrouter.ErrModelUnavailable):
		return aiModelRefusal(campaignTranslateModelUnavailableMsg, model)
	case errors.Is(err, sql.ErrNoRows):
		return status.Error(codes.NotFound, "email campaign not found")
	case errors.Is(err, errCampaignNotDraft):
		return status.Error(codes.FailedPrecondition,
			"only draft campaigns can be auto-translated; pause or cancel it back to draft first")
	case errors.Is(err, errCampaignChangedDuringTranslate):
		return status.Error(codes.Aborted,
			"campaign was edited during translation; re-run auto-translate")
	}
	return status.Error(codes.Internal, "can't auto-translate campaign")
}

// defaultCampaignLanguage returns the source language id + canonical locale code for
// auto-translation (the is_default language, i.e. English). Falls back to the smallest id.
func defaultCampaignLanguage(langs []entity.Language) (int, string) {
	id, code := 0, localeutil.Default
	for _, l := range langs {
		if l.IsDefault {
			return l.Id, canonicalOrDefault(l.Code)
		}
		if id == 0 || l.Id < id {
			id, code = l.Id, canonicalOrDefault(l.Code)
		}
	}
	return id, code
}

func canonicalOrDefault(code string) string { return localeutil.CanonicalOrDefault(code) }

func findSubjectIdx(subs []entity.SubjectTranslation, langID int) int {
	for i := range subs {
		if subs[i].LanguageID == langID {
			return i
		}
	}
	return -1
}

func ensureSubject(subs *[]entity.SubjectTranslation, langID int) int {
	if i := findSubjectIdx(*subs, langID); i >= 0 {
		return i
	}
	*subs = append(*subs, entity.SubjectTranslation{LanguageID: langID})
	return len(*subs) - 1
}

func findBlockTrIdx(trs []entity.EmailBlockTranslation, langID int) int {
	for i := range trs {
		if trs[i].LanguageID == langID {
			return i
		}
	}
	return -1
}

func ensureBlockTr(trs *[]entity.EmailBlockTranslation, langID int) int {
	if i := findBlockTrIdx(*trs, langID); i >= 0 {
		return i
	}
	*trs = append(*trs, entity.EmailBlockTranslation{LanguageID: langID})
	return len(*trs) - 1
}

// translateBatch accumulates one target locale's work: the source strings queued for the model and
// the setters that write each answer back into the campaign. Setters materialize the target
// translation row lazily, so a locale with nothing to translate never gets an empty row (which the
// renderer would prefer over the default-language copy, blanking the block for that locale).
type translateBatch struct {
	srcs    []string
	setters []func(string)
	cleared int
	// undo restores what clear() blanked. It exists because clearing happens BEFORE the model is
	// called: without it, a locale whose translation then fails is left blanked, and — since a
	// blanked field counted as a mutation — the campaign was saved that way and the RPC answered
	// success. The person pressed "translate", read "done", and found empty translations where
	// their text had been.
	undo []func()
}

func (b *translateBatch) add(src string, set func(string)) {
	if strings.TrimSpace(src) == "" {
		return
	}
	b.srcs = append(b.srcs, src)
	b.setters = append(b.setters, set)
}

// clear blanks a target field whose source has since been emptied (overwrite runs only), so the
// locale stays a faithful projection of the source instead of keeping copy the English version no
// longer has.
func (b *translateBatch) clear(previous string, set func(string)) {
	set("")
	b.cleared++
	b.undo = append(b.undo, func() { set(previous) })
}

// restoreCleared puts every blanked TEXT field back and forgets the count. The stale text it
// restores is the lesser evil by a wide margin: it is what was there a second ago, and the next
// successful run blanks it again.
//
// IT IS NOT A FULL UNDO, and calling it one would mislead. clear() reaches its field through
// target(), which on an overwrite run also refreshes the row's CTAURL and Links from the source —
// those are copied, never translated — and nothing here puts the previous ones back. So a locale
// that failed can still leave refreshed links behind if some OTHER locale succeeded and the
// campaign was saved. That behaviour predates the undo log and is unchanged by it, and the values
// left behind are the ones a successful run would have written anyway; it is recorded here only so
// the next reader does not mistake this for a complete rollback.
func (b *translateBatch) restoreCleared() {
	for _, undo := range b.undo {
		undo()
	}
	b.undo = nil
	b.cleared = 0
}

// collectBlockFields queues the source block-translation fields (that are non-empty) for
// translation into the target language. It recurses into two_column children first: their copy
// lives on the child blocks and the container itself usually carries no translations at all, so
// everything nested inside a two-column section used to stay in English. URLs (CTAURL) and Links
// are copied from the source, never translated, and hand-authored target values are kept
// field-by-field unless overwrite is set.
func collectBlockFields(b *entity.EmailBlock, srcID, tgtID int, overwrite bool, batch *translateBatch) {
	if b.TwoColumn != nil {
		for i := range b.TwoColumn.Left {
			collectBlockFields(&b.TwoColumn.Left[i], srcID, tgtID, overwrite, batch)
		}
		for i := range b.TwoColumn.Right {
			collectBlockFields(&b.TwoColumn.Right[i], srcID, tgtID, overwrite, batch)
		}
	}
	si := findBlockTrIdx(b.Translations, srcID)
	if si < 0 {
		return
	}
	src := b.Translations[si] // value copy — safe across the appends below
	var tgt entity.EmailBlockTranslation
	if ti := findBlockTrIdx(b.Translations, tgtID); ti >= 0 {
		tgt = b.Translations[ti]
	}
	// target materializes the target row on first write and re-copies the untranslatable fields.
	target := func() *entity.EmailBlockTranslation {
		ti := ensureBlockTr(&b.Translations, tgtID)
		row := &b.Translations[ti]
		if overwrite || strings.TrimSpace(row.CTAURL) == "" {
			row.CTAURL = src.CTAURL // URLs are not translated
		}
		if overwrite || len(row.Links) == 0 {
			row.Links = append([]entity.EmailLink(nil), src.Links...)
		}
		return row
	}
	field := func(srcValue, tgtValue string, assign func(*entity.EmailBlockTranslation, string)) {
		if !overwrite && strings.TrimSpace(tgtValue) != "" {
			return // keep the existing (possibly hand-edited) target field
		}
		if strings.TrimSpace(srcValue) == "" {
			if strings.TrimSpace(tgtValue) != "" { // reachable only with overwrite
				batch.clear(tgtValue, func(v string) { assign(target(), v) })
			}
			return
		}
		batch.add(srcValue, func(v string) { assign(target(), v) })
	}
	field(src.Heading, tgt.Heading, func(t *entity.EmailBlockTranslation, v string) { t.Heading = v })
	field(src.Subheading, tgt.Subheading, func(t *entity.EmailBlockTranslation, v string) { t.Subheading = v })
	field(src.Body, tgt.Body, func(t *entity.EmailBlockTranslation, v string) { t.Body = v })
	field(src.Caption, tgt.Caption, func(t *entity.EmailBlockTranslation, v string) { t.Caption = v })
	field(src.CTALabel, tgt.CTALabel, func(t *entity.EmailBlockTranslation, v string) { t.CTALabel = v })
	field(src.AltText, tgt.AltText, func(t *entity.EmailBlockTranslation, v string) { t.AltText = v })
	field(src.Preheader, tgt.Preheader, func(t *entity.EmailBlockTranslation, v string) { t.Preheader = v })
}

// collectSubject queues a variant's subject for one target locale under the same per-field rules as
// block copy (lazy target row, keep hand-authored text unless overwrite).
func collectSubject(v *entity.EmailCampaignVariant, srcID, tgtID int, overwrite bool, batch *translateBatch) {
	si := findSubjectIdx(v.SubjectI18n, srcID)
	if si < 0 {
		return
	}
	srcSubject := v.SubjectI18n[si].Subject
	tgtSubject := ""
	if ti := findSubjectIdx(v.SubjectI18n, tgtID); ti >= 0 {
		tgtSubject = v.SubjectI18n[ti].Subject
	}
	if !overwrite && strings.TrimSpace(tgtSubject) != "" {
		return
	}
	set := func(value string) {
		ti := ensureSubject(&v.SubjectI18n, tgtID)
		v.SubjectI18n[ti].Subject = value
	}
	if strings.TrimSpace(srcSubject) == "" {
		if strings.TrimSpace(tgtSubject) != "" { // reachable only with overwrite
			batch.clear(tgtSubject, set)
		}
		return
	}
	batch.add(srcSubject, set)
}

// autoTranslateCampaign fills every non-source locale's subject + block translations from the
// English source via the LLM translator, then re-persists the campaign. With overwrite=false it
// only fills locales/fields the admin hasn't authored; with true it re-translates all. Returns the
// number of translated strings.
//
// Failures are per-locale, never all-or-nothing: a locale whose model call fails is logged and
// skipped, the locales that did translate are still saved (their strings were already paid for),
// and fields left empty fall back to the default language at render time. Strings are sent in
// chunks of campaignTranslateChunkSize.
func autoTranslateCampaign(
	ctx context.Context,
	repo dependency.Repository,
	translator dependency.Translator,
	langs []entity.Language,
	campaignID int,
	overwrite bool,
) (int, error) {
	if translator == nil || !translator.Enabled() {
		return 0, fmt.Errorf("translator is not configured")
	}
	if len(langs) == 0 {
		return 0, fmt.Errorf("no languages configured")
	}
	full, err := repo.Campaigns().GetEmailCampaignByID(ctx, campaignID)
	if err != nil {
		return 0, fmt.Errorf("load campaign %d: %w", campaignID, err)
	}
	// The store only updates draft rows, so translating anything else burns the whole LLM spend
	// and then fails at save. Refuse before the first request.
	if full.Status != entity.EmailCampaignStatusDraft {
		return 0, fmt.Errorf("campaign %d is %q: %w", campaignID, full.Status, errCampaignNotDraft)
	}
	srcID, srcCode := defaultCampaignLanguage(langs)
	insert := full.EmailCampaignInsert

	total, mutated := 0, 0
	// []error, NOT []string. Stringifying an error before returning it severs every sentinel in it
	// — silently, and forever after: errors.Is downstream simply answers false. That is exactly how
	// the retired-slug branch below shipped unreachable. Joined at the end, every locale's chain
	// survives, including sentinels nobody has invented yet.
	var failures []error
	for _, lang := range langs {
		if lang.Id == srcID {
			continue
		}
		tgtCode := localeutil.Canonical(lang.Code)
		if tgtCode == "" || tgtCode == srcCode {
			continue
		}
		if err := ctx.Err(); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", tgtCode, err))
			break
		}

		batch := &translateBatch{}
		for vi := range insert.Variants {
			v := &insert.Variants[vi]
			collectSubject(v, srcID, lang.Id, overwrite, batch)
			for bi := range v.Body {
				collectBlockFields(&v.Body[bi], srcID, lang.Id, overwrite, batch)
			}
		}
		for bi := range insert.Body {
			collectBlockFields(&insert.Body[bi], srcID, lang.Id, overwrite, batch)
		}
		// NOTE: batch.cleared is counted at the END of the iteration, not here. Counting a blanked
		// field as a mutation before the model has answered is what let a failed run save an empty
		// translation over a real one.
		if len(batch.srcs) == 0 {
			mutated += batch.cleared
			continue
		}
		if len(batch.srcs) > maxCampaignTranslateStrings {
			failures = append(failures, fmt.Errorf("%s: %d translatable strings exceed the %d limit",
				tgtCode, len(batch.srcs), maxCampaignTranslateStrings))
			batch.restoreCleared()
			continue
		}

		translatedForLocale := 0
		localeFailed := false
		for start := 0; start < len(batch.srcs); start += campaignTranslateChunkSize {
			end := min(start+campaignTranslateChunkSize, len(batch.srcs))
			chunk := batch.srcs[start:end]
			translated, err := translator.Translate(ctx, srcCode, tgtCode, chunk)
			if err == nil && len(translated) != len(chunk) {
				err = fmt.Errorf("got %d results for %d inputs", len(translated), len(chunk))
			}
			if err != nil {
				slog.ErrorContext(ctx, "campaign auto-translate locale failed",
					slog.Int("campaign_id", campaignID),
					slog.String("locale", tgtCode),
					slog.String("err", err.Error()),
				)
				// A DEAD MODEL SLUG IS GLOBAL, so this returns instead of collecting: the remaining
				// locales would each pay for one more doomed round-trip to answer the same thing.
				// Returning here is also what keeps the campaign unsaved — no partial write, no
				// blanked translations, and a refusal the caller can name (the %w is load-bearing:
				// campaignTranslateError branches on this sentinel).
				if errors.Is(err, openrouter.ErrModelUnavailable) {
					// The rollback carries NO production weight on this path, and the earlier
					// version of this comment claimed otherwise. Nothing is saved here, and `full`
					// is built fresh from the database inside this very call (Campaigns().
					// GetEmailCampaignByID — no cache, no shared aggregate), so the blanks would
					// die with it. `insert` does share slice backing with `full`, but the only
					// place anybody observes that is the test, whose mock hands back an aggregate
					// it still holds and asserts the previous text is intact.
					//
					// It stays because the rule is worth more than the call: clearing is rolled
					// back on EVERY failure, without exceptions to remember. An undo that applies
					// "except on the early return" is the kind of asymmetry a later reader has to
					// rediscover the hard way.
					batch.restoreCleared()
					return 0, fmt.Errorf("auto-translate campaign %d: %w", campaignID, err)
				}
				failures = append(failures, fmt.Errorf("%s: %w", tgtCode, err))
				localeFailed = true
				break // keep the locales/chunks already translated
			}
			for i, value := range translated {
				batch.setters[start+i](value)
			}
			translatedForLocale += len(chunk)
		}
		if localeFailed {
			// Only the BLANKING is rolled back. The chunks that landed before the failure stay and
			// are saved — that is the deliberate behaviour the `break` above names, and it is not
			// what this undo is about. What must not survive is a field emptied in preparation for
			// a translation that never arrived.
			batch.restoreCleared()
		}
		total += translatedForLocale
		mutated += translatedForLocale + batch.cleared
	}

	if mutated == 0 {
		if len(failures) > 0 {
			return 0, fmt.Errorf("auto-translate campaign %d: %w", campaignID, errors.Join(failures...))
		}
		return 0, nil
	}

	// Model output is untrusted input on the write side exactly like admin-authored copy, so it
	// passes the same sanitizer UpsertEmailCampaign applies before it reaches the DB (and from
	// there the admin's rich-text editor, which does not sanitize on read).
	campaignrender.SanitizeBlocks(insert.Body)
	for i := range insert.Variants {
		campaignrender.SanitizeBlocks(insert.Variants[i].Body)
	}

	// The model round-trips take seconds to minutes; re-read before writing the whole aggregate so
	// a concurrent edit is reported instead of silently reverted.
	fresh, err := repo.Campaigns().GetEmailCampaignByID(ctx, campaignID)
	if err != nil {
		return 0, fmt.Errorf("reload campaign %d: %w", campaignID, err)
	}
	if !fresh.UpdatedAt.Equal(full.UpdatedAt) {
		return 0, fmt.Errorf("campaign %d changed at %s (loaded %s): %w",
			campaignID, fresh.UpdatedAt, full.UpdatedAt, errCampaignChangedDuringTranslate)
	}
	if _, err := repo.Campaigns().UpsertEmailCampaign(ctx, campaignID, &insert); err != nil {
		return 0, fmt.Errorf("save translated campaign %d: %w", campaignID, err)
	}
	if len(failures) > 0 {
		slog.ErrorContext(ctx, "campaign auto-translate saved with locale failures",
			slog.Int("campaign_id", campaignID),
			slog.Int("translated", total),
			slog.String("failures", errors.Join(failures...).Error()),
		)
	}
	return total, nil
}
