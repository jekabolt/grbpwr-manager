package admin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/localeutil"
	"github.com/jekabolt/grbpwr-manager/internal/translate"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
		return nil, status.Error(codes.FailedPrecondition, "translation is not configured (OPENROUTER_API_KEY unset)")
	}
	n, err := autoTranslateCampaign(ctx, s.repo, translator, cache.GetLanguages(), int(req.GetId()), req.GetOverwrite())
	if err != nil {
		slog.ErrorContext(ctx, "auto-translate campaign failed", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't auto-translate campaign")
	}
	return &pb_admin.AutoTranslateEmailCampaignResponse{TranslatedCount: int32(n)}, nil
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

// collectBlockFields queues the source block-translation fields (that are non-empty) for
// translation into the target language, ensuring exactly one target EmailBlockTranslation per
// block. URLs (CTAURL) and Links are copied from source, never translated.
func collectBlockFields(b *entity.EmailBlock, srcID, tgtID int, overwrite bool, collect func(src string, set func(string))) {
	si := findBlockTrIdx(b.Translations, srcID)
	if si < 0 {
		return
	}
	if !overwrite && findBlockTrIdx(b.Translations, tgtID) >= 0 {
		return // keep the existing (possibly hand-edited) target translation
	}
	src := b.Translations[si] // value copy — safe across the append below
	ti := ensureBlockTr(&b.Translations, tgtID)
	b.Translations[ti].CTAURL = src.CTAURL // URLs are not translated
	b.Translations[ti].Links = src.Links
	collect(src.Heading, func(t string) { b.Translations[ti].Heading = t })
	collect(src.Subheading, func(t string) { b.Translations[ti].Subheading = t })
	collect(src.Body, func(t string) { b.Translations[ti].Body = t })
	collect(src.Caption, func(t string) { b.Translations[ti].Caption = t })
	collect(src.CTALabel, func(t string) { b.Translations[ti].CTALabel = t })
	collect(src.AltText, func(t string) { b.Translations[ti].AltText = t })
	collect(src.Preheader, func(t string) { b.Translations[ti].Preheader = t })
}

// autoTranslateCampaign fills every non-source locale's subject + block translations from the
// English source via the LLM translator, then re-persists the campaign. With overwrite=false it
// only fills locales/fields the admin hasn't authored; with true it re-translates all. Returns the
// number of translated strings. One LLM call per target locale (all strings batched).
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
	srcID, srcCode := defaultCampaignLanguage(langs)
	insert := full.EmailCampaignInsert

	total := 0
	for _, lang := range langs {
		if lang.Id == srcID {
			continue
		}
		tgtCode := localeutil.Canonical(lang.Code)
		if tgtCode == "" || tgtCode == srcCode {
			continue
		}

		var srcs []string
		var setters []func(string)
		collect := func(src string, set func(string)) {
			if strings.TrimSpace(src) == "" {
				return
			}
			srcs = append(srcs, src)
			setters = append(setters, set)
		}

		for vi := range insert.Variants {
			v := &insert.Variants[vi]
			if si := findSubjectIdx(v.SubjectI18n, srcID); si >= 0 {
				srcSubject := v.SubjectI18n[si].Subject
				if overwrite || findSubjectIdx(v.SubjectI18n, lang.Id) < 0 {
					ti := ensureSubject(&v.SubjectI18n, lang.Id)
					collect(srcSubject, func(t string) { v.SubjectI18n[ti].Subject = t })
				}
			}
			for bi := range v.Body {
				collectBlockFields(&v.Body[bi], srcID, lang.Id, overwrite, collect)
			}
		}
		for bi := range insert.Body {
			collectBlockFields(&insert.Body[bi], srcID, lang.Id, overwrite, collect)
		}

		if len(srcs) == 0 {
			continue
		}
		translated, err := translator.Translate(ctx, srcCode, tgtCode, srcs)
		if err != nil {
			return total, fmt.Errorf("translate campaign %d to %s: %w", campaignID, tgtCode, err)
		}
		if len(translated) != len(setters) {
			return total, fmt.Errorf("translate campaign %d to %s: got %d results for %d inputs",
				campaignID, tgtCode, len(translated), len(setters))
		}
		for i, set := range setters {
			set(translated[i])
		}
		total += len(srcs)
	}

	if _, err := repo.Campaigns().UpsertEmailCampaign(ctx, campaignID, &insert); err != nil {
		return total, fmt.Errorf("save translated campaign %d: %w", campaignID, err)
	}
	return total, nil
}
