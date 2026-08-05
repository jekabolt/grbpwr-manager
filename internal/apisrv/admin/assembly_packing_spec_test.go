package admin

import (
	"context"
	"database/sql"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// packingSpecColorway builds the minimum of entity.Colorway the packing spec reads: id, style and the
// colourway's dictionary colour (the thing an assembly component's colour is matched against).
func packingSpecColorway(id, styleID int, colorCode, colorName string) entity.Colorway {
	c := entity.Colorway{Id: id, StyleId: styleID}
	c.ProductDisplay.ProductBody.ProductBodyInsert.ColorCode = colorCode
	c.ProductDisplay.ProductBody.ProductBodyInsert.Color = colorName
	return c
}

func packingSpecVariant(id, cardID int, code, colorName string, materialID int, materialName string) entity.TechCardOutputVariant {
	v := entity.TechCardOutputVariant{TechCardId: cardID, ColorName: colorName, MaterialName: materialName}
	v.Id = id
	v.ColorCode = code
	v.MaterialId = materialID
	v.Active = true
	return v
}

func packingSpecRetired(v entity.TechCardOutputVariant) entity.TechCardOutputVariant {
	v.Active = false
	return v
}

// assemblyLine is one stored bill line: component card + its LEGACY single output material, which is
// what every card had before colour variants existed.
func assemblyLine(id, styleID, componentID int, name string, legacyMaterialID int, legacyMaterialName string) entity.StyleAssembly {
	return entity.StyleAssembly{
		Id: id, StyleId: styleID, ComponentTechCardId: componentID,
		Qty: decimal.NewFromInt(1), Active: true, ComponentName: name,
		OutputMaterialId:   sql.NullInt32{Int32: int32(legacyMaterialID), Valid: legacyMaterialID > 0},
		OutputMaterialName: sql.NullString{String: legacyMaterialName, Valid: legacyMaterialName != ""},
		// Stale on purpose: the store's COUNT and the batched variant read are two queries, and the
		// packing spec must answer from the read it actually resolved against.
		OutputVariantCount: 99,
	}
}

// packingSpecRepo wires the four sub-repos GetOrderPackingSpec touches.
type packingSpecRepo struct {
	repo     *mocks.MockRepository
	orders   *mocks.MockOrder
	products *mocks.MockProducts
	tc       *mocks.MockTechCards
	ms       *mocks.MockMaterialStock
}

func newPackingSpecRepo(t *testing.T) packingSpecRepo {
	t.Helper()
	r := packingSpecRepo{
		repo: mocks.NewMockRepository(t), orders: mocks.NewMockOrder(t),
		products: mocks.NewMockProducts(t), tc: mocks.NewMockTechCards(t),
		ms: mocks.NewMockMaterialStock(t),
	}
	r.repo.EXPECT().Order().Return(r.orders)
	r.repo.EXPECT().Products().Return(r.products)
	r.repo.EXPECT().TechCards().Return(r.tc)
	r.repo.EXPECT().MaterialStock().Return(r.ms)
	return r
}

// Two colourways of ONE style on one order: the black jacket must be packed with the black dust bag and
// the white one with the white dust bag, from a single style bill that names the dust-bag card once
// (R10). The colour lives on the order item, not on the bill.
func TestGetOrderPackingSpecResolvesColourPerItem(t *testing.T) {
	const styleID, dustBagCard, careLabelCard = 7, 40, 41

	r := newPackingSpecRepo(t)
	r.orders.EXPECT().GetOrderFullByUUID(mock.Anything, "ord-1").Return(&entity.OrderFull{
		Order: entity.Order{Id: 3, UUID: "ord-1"},
		OrderItems: []entity.OrderItem{
			{Id: 11, OrderItemInsert: entity.OrderItemInsert{ProductId: 100, Quantity: decimal.NewFromInt(1)}},
			{Id: 12, OrderItemInsert: entity.OrderItemInsert{ProductId: 200, Quantity: decimal.NewFromInt(1)}},
		},
	}, nil)
	r.products.EXPECT().GetProductsByIds(mock.Anything, []int{100, 200}).Return([]entity.Colorway{
		packingSpecColorway(100, styleID, "BLK", "black"),
		packingSpecColorway(200, styleID, "WHT", "white"),
	}, nil)
	r.tc.EXPECT().GetTechCardNames(mock.Anything, []int{styleID}).Return(map[int]string{styleID: "jacket"}, nil)
	r.tc.EXPECT().ListStyleAssembly(mock.Anything, styleID).Return([]entity.StyleAssembly{
		assemblyLine(1, styleID, dustBagCard, "dust bag", 900, "dust bag (legacy)"),
		assemblyLine(2, styleID, careLabelCard, "care label", 910, "care label"),
	}, nil).Once()

	// ONE variant read for the whole order, over the DISTINCT component cards (R11).
	variantCalls := 0
	r.tc.EXPECT().ListOutputVariantsByCardIds(mock.Anything, mock.MatchedBy(func(ids []int) bool {
		variantCalls++
		return len(ids) == 2 && ids[0] == dustBagCard && ids[1] == careLabelCard
	})).Return(map[int][]entity.TechCardOutputVariant{
		dustBagCard: {
			packingSpecVariant(1, dustBagCard, "BLK", "black", 101, "dust bag — black"),
			packingSpecVariant(2, dustBagCard, "WHT", "white", 102, "dust bag — white"),
			// A retired colour nobody ordered: counted nowhere, resolves nothing.
			packingSpecRetired(packingSpecVariant(3, dustBagCard, "GRY", "grey", 103, "dust bag — grey")),
		},
		// careLabelCard is absent: legacy single-output mode.
	}, nil).Once()
	r.ms.EXPECT().ResolveOrderPackaging(mock.Anything, 3).Return(nil, nil)

	s := &Server{repo: r.repo}
	resp, err := s.GetOrderPackingSpec(context.Background(), &pb_admin.GetOrderPackingSpecRequest{OrderUuid: "ord-1"})
	require.NoError(t, err)
	require.Equal(t, 1, variantCalls, "colour resolution must not multiply queries per order item")
	require.Len(t, resp.Items, 2)

	byItem := map[int32]*pb_admin.OrderPackingSpecItem{}
	for _, it := range resp.Items {
		byItem[it.OrderItemId] = it
	}

	// The garment's own colour travels with the item, so the packer sees both sides.
	require.Equal(t, "BLK", byItem[11].ColorCode)
	require.Equal(t, "black", byItem[11].ColorName)
	require.Equal(t, "WHT", byItem[12].ColorCode)

	black := byItem[11].Assembly
	require.Len(t, black, 2)
	require.Equal(t, "BLK", black[0].ResolvedColorCode)
	require.Equal(t, "black", black[0].ResolvedColorName)
	require.Equal(t, int32(101), black[0].ResolvedMaterialId)
	require.Equal(t, "dust bag — black", black[0].ResolvedMaterialName)
	require.False(t, black[0].Unresolved)
	require.Equal(t, pb_common.AssemblyResolutionBasis_ASSEMBLY_RESOLUTION_BASIS_COLOR_MATCH, black[0].ResolutionBasis)
	require.Equal(t, int32(2), black[0].OutputVariantCount,
		"the badge counts LIVE colours only — the retired grey is not one")

	white := byItem[12].Assembly
	require.Equal(t, int32(102), white[0].ResolvedMaterialId, "the white jacket ships the white dust bag")
	require.Equal(t, "WHT", white[0].ResolvedColorCode)

	// The care label has no colours: the legacy single output stands, and says so.
	for _, lines := range [][]*pb_admin.StyleAssemblyLine{black, white} {
		require.Equal(t, int32(0), lines[1].OutputVariantCount)
		require.Equal(t, int32(910), lines[1].ResolvedMaterialId)
		require.Empty(t, lines[1].ResolvedColorCode)
		require.False(t, lines[1].Unresolved)
		require.Equal(t, pb_common.AssemblyResolutionBasis_ASSEMBLY_RESOLUTION_BASIS_LEGACY_OUTPUT, lines[1].ResolutionBasis)
	}
}

// A colourway the component card has no colour for is flagged, never guessed — and the card's stale
// single output material is NOT offered as a consolation answer.
func TestGetOrderPackingSpecFlagsUnresolvedColour(t *testing.T) {
	const styleID, dustBagCard = 7, 40

	r := newPackingSpecRepo(t)
	r.orders.EXPECT().GetOrderFullByUUID(mock.Anything, "ord-2").Return(&entity.OrderFull{
		Order: entity.Order{Id: 4, UUID: "ord-2"},
		OrderItems: []entity.OrderItem{
			{Id: 21, OrderItemInsert: entity.OrderItemInsert{ProductId: 300, Quantity: decimal.NewFromInt(1)}},
		},
	}, nil)
	r.products.EXPECT().GetProductsByIds(mock.Anything, []int{300}).Return([]entity.Colorway{
		packingSpecColorway(300, styleID, "GRN", "green"),
	}, nil)
	r.tc.EXPECT().GetTechCardNames(mock.Anything, []int{styleID}).Return(map[int]string{styleID: "jacket"}, nil)
	r.tc.EXPECT().ListStyleAssembly(mock.Anything, styleID).Return([]entity.StyleAssembly{
		assemblyLine(1, styleID, dustBagCard, "dust bag", 900, "dust bag (legacy)"),
	}, nil).Once()
	r.tc.EXPECT().ListOutputVariantsByCardIds(mock.Anything, []int{dustBagCard}).
		Return(map[int][]entity.TechCardOutputVariant{
			dustBagCard: {
				packingSpecVariant(1, dustBagCard, "BLK", "black", 101, "dust bag — black"),
				packingSpecVariant(2, dustBagCard, "WHT", "white", 102, "dust bag — white"),
			},
		}, nil).Once()
	r.ms.EXPECT().ResolveOrderPackaging(mock.Anything, 4).Return(nil, nil)

	s := &Server{repo: r.repo}
	resp, err := s.GetOrderPackingSpec(context.Background(), &pb_admin.GetOrderPackingSpecRequest{OrderUuid: "ord-2"})
	require.NoError(t, err)
	require.Len(t, resp.Items, 1)
	line := resp.Items[0].Assembly[0]
	require.True(t, line.Unresolved, "no green dust bag exists — say so")
	require.Equal(t, pb_common.AssemblyResolutionBasis_ASSEMBLY_RESOLUTION_BASIS_NO_COLOR_MATCH, line.ResolutionBasis)
	require.Zero(t, line.ResolvedMaterialId)
	require.Empty(t, line.ResolvedColorCode)
	require.Equal(t, int32(2), line.OutputVariantCount)
	// The legacy column still travels for provenance, but it is not the RESOLVED answer.
	require.Equal(t, int32(900), line.OutputMaterialId)
}

// The review's headline case, end to end: the black bucket exists but is RETIRED, and the card happens
// to have exactly one live colour. Before the fix the packer was confidently handed the white bag.
func TestGetOrderPackingSpecRefusesToSubstituteARetiredColour(t *testing.T) {
	const styleID, dustBagCard = 7, 40

	r := newPackingSpecRepo(t)
	r.orders.EXPECT().GetOrderFullByUUID(mock.Anything, "ord-3").Return(&entity.OrderFull{
		Order: entity.Order{Id: 5, UUID: "ord-3"},
		OrderItems: []entity.OrderItem{
			{Id: 31, OrderItemInsert: entity.OrderItemInsert{ProductId: 400, Quantity: decimal.NewFromInt(1)}},
		},
	}, nil)
	r.products.EXPECT().GetProductsByIds(mock.Anything, []int{400}).Return([]entity.Colorway{
		packingSpecColorway(400, styleID, "BLK", "black"),
	}, nil)
	r.tc.EXPECT().GetTechCardNames(mock.Anything, []int{styleID}).Return(map[int]string{styleID: "jacket"}, nil)
	r.tc.EXPECT().ListStyleAssembly(mock.Anything, styleID).Return([]entity.StyleAssembly{
		assemblyLine(1, styleID, dustBagCard, "dust bag", 900, "dust bag (legacy)"),
	}, nil).Once()
	r.tc.EXPECT().ListOutputVariantsByCardIds(mock.Anything, []int{dustBagCard}).
		Return(map[int][]entity.TechCardOutputVariant{
			dustBagCard: {
				packingSpecRetired(packingSpecVariant(1, dustBagCard, "BLK", "black", 101, "dust bag — black")),
				packingSpecVariant(2, dustBagCard, "WHT", "white", 102, "dust bag — white"),
			},
		}, nil).Once()
	r.ms.EXPECT().ResolveOrderPackaging(mock.Anything, 5).Return(nil, nil)

	s := &Server{repo: r.repo}
	resp, err := s.GetOrderPackingSpec(context.Background(), &pb_admin.GetOrderPackingSpecRequest{OrderUuid: "ord-3"})
	require.NoError(t, err)
	line := resp.Items[0].Assembly[0]
	require.True(t, line.Unresolved, "the black bucket is retired, not missing — a human decides")
	require.Equal(t, pb_common.AssemblyResolutionBasis_ASSEMBLY_RESOLUTION_BASIS_RETIRED_COLOR, line.ResolutionBasis)
	require.Zero(t, line.ResolvedMaterialId, "the white bag must NOT be prescribed for a black jacket")
	require.Equal(t, int32(1), line.OutputVariantCount)
}

// Two gaps the review asked for: an order item whose colourway GetProductsByIds does not return
// (lifecycle-filtered — archived/hidden/draft), and a DEACTIVATED assembly line, which must not even
// reach the variant read.
func TestGetOrderPackingSpecHandlesMissingProductAndInactiveLines(t *testing.T) {
	const styleID, dustBagCard, retiredLineCard = 7, 40, 42

	r := newPackingSpecRepo(t)
	r.orders.EXPECT().GetOrderFullByUUID(mock.Anything, "ord-4").Return(&entity.OrderFull{
		Order: entity.Order{Id: 6, UUID: "ord-4"},
		OrderItems: []entity.OrderItem{
			{Id: 41, OrderItemInsert: entity.OrderItemInsert{ProductId: 500, Quantity: decimal.NewFromInt(1)}},
			// 600 is archived: GetProductsByIds filters lifecycle_status=2 and simply omits it.
			{Id: 42, OrderItemInsert: entity.OrderItemInsert{ProductId: 600, Quantity: decimal.NewFromInt(1)}},
		},
	}, nil)
	r.products.EXPECT().GetProductsByIds(mock.Anything, []int{500, 600}).Return([]entity.Colorway{
		packingSpecColorway(500, styleID, "BLK", "black"),
	}, nil)
	r.tc.EXPECT().GetTechCardNames(mock.Anything, []int{styleID}).Return(map[int]string{styleID: "jacket"}, nil)
	inactive := assemblyLine(2, styleID, retiredLineCard, "discontinued hangtag", 920, "hangtag")
	inactive.Active = false
	r.tc.EXPECT().ListStyleAssembly(mock.Anything, styleID).Return([]entity.StyleAssembly{
		assemblyLine(1, styleID, dustBagCard, "dust bag", 900, "dust bag (legacy)"),
		inactive,
	}, nil).Once()
	// The deactivated line's component is NOT asked about: it never reaches a packer.
	r.tc.EXPECT().ListOutputVariantsByCardIds(mock.Anything, []int{dustBagCard}).
		Return(map[int][]entity.TechCardOutputVariant{
			dustBagCard: {
				packingSpecVariant(1, dustBagCard, "BLK", "black", 101, "dust bag — black"),
				packingSpecVariant(2, dustBagCard, "WHT", "white", 102, "dust bag — white"),
			},
		}, nil).Once()
	r.ms.EXPECT().ResolveOrderPackaging(mock.Anything, 6).Return(nil, nil)

	s := &Server{repo: r.repo}
	resp, err := s.GetOrderPackingSpec(context.Background(), &pb_admin.GetOrderPackingSpecRequest{OrderUuid: "ord-4"})
	require.NoError(t, err)
	require.Len(t, resp.Items, 2)

	byItem := map[int32]*pb_admin.OrderPackingSpecItem{}
	for _, it := range resp.Items {
		byItem[it.OrderItemId] = it
	}

	live := byItem[41]
	require.Len(t, live.Assembly, 1, "the deactivated line never reaches the packer")
	require.Equal(t, int32(101), live.Assembly[0].ResolvedMaterialId)

	// The archived colourway resolves to no style at all (a pre-existing property of the
	// lifecycle-filtered product read), so it carries no colour and no assembly — and does not panic.
	missing := byItem[42]
	require.Empty(t, missing.ColorCode)
	require.Empty(t, missing.ColorName)
	require.Zero(t, missing.StyleId)
	require.Empty(t, missing.Assembly)
}
