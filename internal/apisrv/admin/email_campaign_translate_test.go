package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestAutoTranslateCampaign(t *testing.T) {
	langs := []entity.Language{
		{Id: 1, Code: "en", IsDefault: true},
		{Id: 5, Code: "ja"},
		{Id: 6, Code: "cn"}, // stored as cn; canonicalizes to zh
	}
	campaign := &entity.EmailCampaignFull{
		ID: 42,
		EmailCampaignInsert: entity.EmailCampaignInsert{
			Name:   "Drop",
			Status: entity.EmailCampaignStatusDraft,
			Variants: []entity.EmailCampaignVariant{{
				ID:          1,
				SubjectI18n: []entity.SubjectTranslation{{LanguageID: 1, Subject: "NEW DROP"}},
				Body: []entity.EmailBlock{{
					Type: entity.EmailBlockTypeRichText,
					Translations: []entity.EmailBlockTranslation{{
						LanguageID: 1,
						Heading:    "WELCOME",
						Body:       `<p>SHOP THE <a href="https://grbpwr.com/x">DROP</a></p>`,
						CTAURL:     "https://grbpwr.com/x", // must be copied, never translated
					}},
				}},
			}},
		},
	}

	translator := mocks.NewMockTranslator(t)
	translator.EXPECT().Enabled().Return(true)
	// One call per target locale; echo a locale-prefixed translation preserving markup.
	translator.EXPECT().Translate(mock.Anything, "en", mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _, tgt string, items []string) ([]string, error) {
			out := make([]string, len(items))
			for i, it := range items {
				out[i] = strings.ToUpper(tgt) + ":" + it
			}
			return out, nil
		})

	campaignsMock := mocks.NewMockCampaigns(t)
	campaignsMock.EXPECT().GetEmailCampaignByID(mock.Anything, 42).Return(campaign, nil)
	var saved *entity.EmailCampaignInsert
	campaignsMock.EXPECT().UpsertEmailCampaign(mock.Anything, 42, mock.Anything).
		RunAndReturn(func(_ context.Context, _ int, ins *entity.EmailCampaignInsert) (int, error) {
			saved = ins
			return 42, nil
		})
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Campaigns().Return(campaignsMock)

	n, err := autoTranslateCampaign(context.Background(), repo, translator, langs, 42, false)
	require.NoError(t, err)
	require.Equal(t, 6, n, "3 non-empty strings (subject + heading + body) × 2 target locales")
	require.NotNil(t, saved)

	v := saved.Variants[0]
	// Subject translated for ja(5) and zh via cn(6).
	require.Equal(t, "JA:NEW DROP", subjectFor(v.SubjectI18n, 5))
	require.Equal(t, "ZH:NEW DROP", subjectFor(v.SubjectI18n, 6))
	// Block heading + body translated; CTAURL preserved.
	tr5 := blockTrFor(v.Body[0].Translations, 5)
	require.Equal(t, "JA:WELCOME", tr5.Heading)
	require.Contains(t, tr5.Body, "<a href=") // markup preserved by the echo
	require.Equal(t, "https://grbpwr.com/x", tr5.CTAURL)
	tr6 := blockTrFor(v.Body[0].Translations, 6)
	require.Equal(t, "ZH:WELCOME", tr6.Heading)
	// Source (en) untouched.
	require.Equal(t, "NEW DROP", subjectFor(v.SubjectI18n, 1))
}

// draftCampaign wraps blocks in a minimal draft campaign (no variants) for the focused
// auto-translate tests below.
func draftCampaign(blocks []entity.EmailBlock) *entity.EmailCampaignFull {
	return &entity.EmailCampaignFull{
		ID: 7,
		EmailCampaignInsert: entity.EmailCampaignInsert{
			Name:   "Drop",
			Status: entity.EmailCampaignStatusDraft,
			Body:   blocks,
		},
	}
}

// translateRepo wires a repository whose campaign load returns full and whose save captures the
// persisted aggregate. saved stays nil when UpsertEmailCampaign is never called.
func translateRepo(t *testing.T, full *entity.EmailCampaignFull, expectSave bool) (*mocks.MockRepository, func() *entity.EmailCampaignInsert) {
	t.Helper()
	campaignsMock := mocks.NewMockCampaigns(t)
	campaignsMock.EXPECT().GetEmailCampaignByID(mock.Anything, full.ID).Return(full, nil)
	var saved *entity.EmailCampaignInsert
	if expectSave {
		campaignsMock.EXPECT().UpsertEmailCampaign(mock.Anything, full.ID, mock.Anything).
			RunAndReturn(func(_ context.Context, id int, ins *entity.EmailCampaignInsert) (int, error) {
				saved = ins
				return id, nil
			})
	}
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Campaigns().Return(campaignsMock)
	return repo, func() *entity.EmailCampaignInsert { return saved }
}

// echoTranslator prefixes every string with the upper-cased target locale, preserving markup.
func echoTranslator(t *testing.T) *mocks.MockTranslator {
	t.Helper()
	translator := mocks.NewMockTranslator(t)
	translator.EXPECT().Enabled().Return(true)
	translator.EXPECT().Translate(mock.Anything, "en", mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _, tgt string, items []string) ([]string, error) {
			out := make([]string, len(items))
			for i, it := range items {
				out[i] = strings.ToUpper(tgt) + ":" + it
			}
			return out, nil
		}).Maybe()
	return translator
}

func TestAutoTranslateCampaignTwoColumnChildren(t *testing.T) {
	langs := []entity.Language{{Id: 1, Code: "en", IsDefault: true}, {Id: 5, Code: "ja"}}
	full := draftCampaign([]entity.EmailBlock{{
		// The container itself carries no translations — the copy lives on the children.
		Type: entity.EmailBlockTypeTwoColumn,
		TwoColumn: &entity.EmailTwoColumnBlock{
			Left: []entity.EmailBlock{{
				Type: entity.EmailBlockTypeRichText,
				Translations: []entity.EmailBlockTranslation{{
					LanguageID: 1,
					Heading:    "LEFT",
					Body:       "<p>Left column</p>",
				}},
			}},
			Right: []entity.EmailBlock{{
				Type: entity.EmailBlockTypeCTAButton,
				Translations: []entity.EmailBlockTranslation{{
					LanguageID: 1,
					CTALabel:   "SHOP",
					CTAURL:     "https://grbpwr.com/x",
				}},
			}},
		},
	}})
	repo, saved := translateRepo(t, full, true)

	n, err := autoTranslateCampaign(context.Background(), repo, echoTranslator(t), langs, full.ID, false)
	require.NoError(t, err)
	require.Equal(t, 3, n, "left heading + left body + right cta label")

	insert := saved()
	require.NotNil(t, insert)
	left := blockTrFor(insert.Body[0].TwoColumn.Left[0].Translations, 5)
	require.Equal(t, "JA:LEFT", left.Heading)
	require.Equal(t, "JA:<p>Left column</p>", left.Body)
	right := blockTrFor(insert.Body[0].TwoColumn.Right[0].Translations, 5)
	require.Equal(t, "JA:SHOP", right.CTALabel)
	require.Equal(t, "https://grbpwr.com/x", right.CTAURL, "URLs are copied, never translated")
}

func TestAutoTranslateCampaignSkipsEmptyTargetRows(t *testing.T) {
	langs := []entity.Language{{Id: 1, Code: "en", IsDefault: true}, {Id: 5, Code: "ja"}}
	full := draftCampaign([]entity.EmailBlock{
		// Nothing translatable: an all-empty source row must not produce a target row, because an
		// empty row shadows the default-language copy at render time.
		{
			Type:         entity.EmailBlockTypeSpacer,
			Translations: []entity.EmailBlockTranslation{{LanguageID: 1}},
		},
		// A half-authored target row: the empty body must be filled without overwrite, the
		// hand-written heading kept.
		{
			Type: entity.EmailBlockTypeRichText,
			Translations: []entity.EmailBlockTranslation{
				{LanguageID: 1, Heading: "EN HEAD", Body: "<p>EN body</p>"},
				{LanguageID: 5, Heading: "手書き"},
			},
		},
	})
	repo, saved := translateRepo(t, full, true)

	n, err := autoTranslateCampaign(context.Background(), repo, echoTranslator(t), langs, full.ID, false)
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the empty body of the half-authored row")

	insert := saved()
	require.NotNil(t, insert)
	require.Equal(t, -1, findBlockTrIdx(insert.Body[0].Translations, 5),
		"a block with nothing to translate must not get an empty target row")
	partial := blockTrFor(insert.Body[1].Translations, 5)
	require.Equal(t, "手書き", partial.Heading, "hand-authored field must survive")
	require.Equal(t, "JA:<p>EN body</p>", partial.Body)
}

func TestAutoTranslateCampaignKeepsSucceededLocales(t *testing.T) {
	langs := []entity.Language{
		{Id: 1, Code: "en", IsDefault: true},
		{Id: 5, Code: "ja"},
		{Id: 6, Code: "cn"},
	}
	full := draftCampaign([]entity.EmailBlock{{
		Type: entity.EmailBlockTypeRichText,
		Translations: []entity.EmailBlockTranslation{{
			LanguageID: 1, Heading: "WELCOME", Body: "<p>EN body</p>",
		}},
	}})
	repo, saved := translateRepo(t, full, true)

	translator := mocks.NewMockTranslator(t)
	translator.EXPECT().Enabled().Return(true)
	translator.EXPECT().Translate(mock.Anything, "en", mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _, tgt string, items []string) ([]string, error) {
			if tgt == "ja" {
				return nil, errors.New("provider timeout")
			}
			out := make([]string, len(items))
			for i, it := range items {
				out[i] = strings.ToUpper(tgt) + ":" + it
			}
			return out, nil
		})

	n, err := autoTranslateCampaign(context.Background(), repo, translator, langs, full.ID, false)
	require.NoError(t, err, "one failing locale must not discard the locales already paid for")
	require.Equal(t, 2, n, "heading + body for zh only")

	insert := saved()
	require.NotNil(t, insert)
	require.Equal(t, "ZH:WELCOME", blockTrFor(insert.Body[0].Translations, 6).Heading)
	require.Equal(t, -1, findBlockTrIdx(insert.Body[0].Translations, 5),
		"the failed locale must not leave an empty row shadowing the source copy")
}

func TestAutoTranslateCampaignChunksLargeLocales(t *testing.T) {
	langs := []entity.Language{{Id: 1, Code: "en", IsDefault: true}, {Id: 5, Code: "ja"}}
	blocks := make([]entity.EmailBlock, campaignTranslateChunkSize+3)
	for i := range blocks {
		blocks[i] = entity.EmailBlock{
			Type:         entity.EmailBlockTypeRichText,
			Translations: []entity.EmailBlockTranslation{{LanguageID: 1, Heading: "HEAD"}},
		}
	}
	full := draftCampaign(blocks)
	repo, saved := translateRepo(t, full, true)

	translator := mocks.NewMockTranslator(t)
	translator.EXPECT().Enabled().Return(true)
	var perCall []int
	translator.EXPECT().Translate(mock.Anything, "en", "ja", mock.Anything).
		RunAndReturn(func(_ context.Context, _, tgt string, items []string) ([]string, error) {
			perCall = append(perCall, len(items))
			out := make([]string, len(items))
			for i, it := range items {
				out[i] = "JA:" + it
			}
			return out, nil
		})

	n, err := autoTranslateCampaign(context.Background(), repo, translator, langs, full.ID, false)
	require.NoError(t, err)
	require.Equal(t, len(blocks), n)
	require.Equal(t, []int{campaignTranslateChunkSize, 3}, perCall)
	require.NotNil(t, saved())
}

func TestAutoTranslateCampaignRejectsNonDraftBeforeSpending(t *testing.T) {
	langs := []entity.Language{{Id: 1, Code: "en", IsDefault: true}, {Id: 5, Code: "ja"}}
	full := draftCampaign([]entity.EmailBlock{{
		Type:         entity.EmailBlockTypeRichText,
		Translations: []entity.EmailBlockTranslation{{LanguageID: 1, Heading: "WELCOME"}},
	}})
	full.Status = entity.EmailCampaignStatusScheduled
	repo, saved := translateRepo(t, full, false)

	translator := mocks.NewMockTranslator(t)
	translator.EXPECT().Enabled().Return(true)
	// No Translate expectation: the precondition must fail before any model call.

	n, err := autoTranslateCampaign(context.Background(), repo, translator, langs, full.ID, false)
	require.ErrorIs(t, err, errCampaignNotDraft)
	require.Zero(t, n)
	require.Nil(t, saved())
}

func TestAutoTranslateCampaignAbortsOnConcurrentEdit(t *testing.T) {
	langs := []entity.Language{{Id: 1, Code: "en", IsDefault: true}, {Id: 5, Code: "ja"}}
	loaded := draftCampaign([]entity.EmailBlock{{
		Type:         entity.EmailBlockTypeRichText,
		Translations: []entity.EmailBlockTranslation{{LanguageID: 1, Heading: "WELCOME"}},
	}})
	loaded.UpdatedAt = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	edited := draftCampaign(loaded.Body)
	edited.UpdatedAt = loaded.UpdatedAt.Add(time.Minute)

	campaignsMock := mocks.NewMockCampaigns(t)
	campaignsMock.EXPECT().GetEmailCampaignByID(mock.Anything, loaded.ID).Return(loaded, nil).Once()
	campaignsMock.EXPECT().GetEmailCampaignByID(mock.Anything, loaded.ID).Return(edited, nil).Once()
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Campaigns().Return(campaignsMock)

	_, err := autoTranslateCampaign(context.Background(), repo, echoTranslator(t), langs, loaded.ID, false)
	require.ErrorIs(t, err, errCampaignChangedDuringTranslate)
}

func TestAutoTranslateCampaignOverwriteClearsStaleTarget(t *testing.T) {
	langs := []entity.Language{{Id: 1, Code: "en", IsDefault: true}, {Id: 5, Code: "ja"}}
	full := draftCampaign([]entity.EmailBlock{{
		Type: entity.EmailBlockTypeRichText,
		Translations: []entity.EmailBlockTranslation{
			{LanguageID: 1, Heading: "EN HEAD"}, // subheading has since been deleted in English
			{LanguageID: 5, Heading: "JA HEAD", Subheading: "古いサブ"},
		},
	}})
	repo, saved := translateRepo(t, full, true)

	n, err := autoTranslateCampaign(context.Background(), repo, echoTranslator(t), langs, full.ID, true)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	insert := saved()
	require.NotNil(t, insert)
	target := blockTrFor(insert.Body[0].Translations, 5)
	require.Equal(t, "JA:EN HEAD", target.Heading)
	require.Empty(t, target.Subheading, "target text whose source was emptied must be cleared")
}

func subjectFor(subs []entity.SubjectTranslation, id int) string {
	if i := findSubjectIdx(subs, id); i >= 0 {
		return subs[i].Subject
	}
	return ""
}

func blockTrFor(trs []entity.EmailBlockTranslation, id int) entity.EmailBlockTranslation {
	if i := findBlockTrIdx(trs, id); i >= 0 {
		return trs[i]
	}
	return entity.EmailBlockTranslation{}
}

// TestCampaignTranslateErrorMapping — ТРЕТИЙ ПОТРЕБИТЕЛЬ ТОГО ЖЕ КЛИЕНТА.
//
// Авто-перевод кампаний ходит тем же `s.aiOps` и тем же слугом, что заметки и черновик операций:
// когда провайдер снял слуг с обслуживания, эта кнопка умерла ровно тогда же. Две другие кнопки
// уже отвечают «настройка», а эта проваливалась в `Internal, "can't auto-translate campaign"` —
// то есть в следующий раз соврала бы одна из трёх, и именно та, по которой не жаловались.
//
// Проверяется ВСЯ таблица, а не одна новая ветка: ветка, вставленная перед `sql.ErrNoRows`, могла
// бы затенить соседей, и «не найдено» стало бы «почините настройку».
func TestCampaignTranslateErrorMapping(t *testing.T) {
	const model = "anthropic/claude-sonnet-5"

	t.Run("слуг не обслуживается — это настройка, а не Internal", func(t *testing.T) {
		// Именно в такой обёртке ошибка и приходит: translate.go оборачивает через %w
		// («translate en→fr: …»), поэтому проверяется цепочка, а не голый сентинел.
		err := campaignTranslateError(
			fmt.Errorf("translate en→fr: %w", openrouter.ErrModelUnavailable), model)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		msg := status.Convert(err).Message()
		require.Contains(t, msg, "OPENROUTER_MODEL", "отказ обязан называть настройку")
		require.Contains(t, msg, model, "и действующий слуг: без него человек не знает, что менять")
		require.NotContains(t, msg, "can't auto-translate campaign", "это не безымянный сбой")
	})

	t.Run("остальные ветки не затенены", func(t *testing.T) {
		for name, tc := range map[string]struct {
			err  error
			want codes.Code
		}{
			"кампании нет":           {sql.ErrNoRows, codes.NotFound},
			"не черновик":            {errCampaignNotDraft, codes.FailedPrecondition},
			"правка во время работы": {errCampaignChangedDuringTranslate, codes.Aborted},
			"ключа нет":              {openrouter.ErrNotConfigured, codes.Internal},
			"всё остальное":          {errors.New("boom"), codes.Internal},
		} {
			t.Run(name, func(t *testing.T) {
				require.Equal(t, tc.want, status.Code(campaignTranslateError(tc.err, model)))
			})
		}
	})
}
