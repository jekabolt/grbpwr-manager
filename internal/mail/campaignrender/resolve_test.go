package campaignrender

import (
	"context"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/mock"
)

func TestResolveMissingProductSkipsBlockAndWarns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rep := mocks.NewMockRepository(t)
	products := mocks.NewMockProducts(t)
	rep.On("Products").Return(products)
	products.On("GetProductsByIds", mock.Anything, mock.Anything).Return([]entity.Colorway{}, nil)

	blocks := []entity.EmailBlock{
		{
			Type:        entity.EmailBlockTypeProductCard,
			ProductCard: &entity.EmailProductCardBlock{ProductID: 404},
		},
		{
			Type:     entity.EmailBlockTypeRichText,
			RichText: &entity.EmailRichTextBlock{},
			Translations: []entity.EmailBlockTranslation{{
				LanguageID: 1,
				Body:       "<p>Sibling survives</p>",
			}},
		},
	}
	r := newResolver(rep)
	r.prime(ctx, collectMediaIDs(blocks), collectProductIDs(blocks))
	resolved, warnings := resolveBlocks(ctx, r, blocks, nil, 0, 0)

	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want exactly one", warnings)
	}
	if warnings[0].BlockIndex != 0 || !strings.Contains(warnings[0].Reason, "404") {
		t.Fatalf("warning = %#v", warnings[0])
	}
	if len(resolved) != 1 || resolved[0].Type != entity.EmailBlockTypeRichText {
		t.Fatalf("resolved = %#v, want surviving rich-text sibling", resolved)
	}
}
