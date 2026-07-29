package admin

import (
	"context"
	"strings"
	"testing"

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
			Name: "Drop",
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
