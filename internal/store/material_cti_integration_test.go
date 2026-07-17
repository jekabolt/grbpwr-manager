package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestMaterialCTIAttrs is the acceptance test for the material class-table-inheritance typing
// (WS3 / S15, migration 0157): a fabric material round-trips its typed fabric attributes, changing
// the class full-replaces the side-tables (stale fabric attrs are cleared, the new class's attrs
// appear), and created_by survives an update while updated_by is refreshed.
func TestMaterialCTIAttrs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	tc := s.TechCards()

	nd := func(v string) decimal.NullDecimal { return decimal.NewNullDecimal(decimal.RequireFromString(v)) }
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	id, err := tc.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "CTI Fabric", Section: "fabric", MaterialClass: "fabric",
		CreatedBy: "alice", UpdatedBy: "alice",
		FabricAttr: &entity.MaterialFabricAttr{
			WidthCm: nd("150"), WeightGsm: nd("320"), FabricDirection: ns("lengthwise"),
		},
	})
	require.NoError(t, err)

	m, err := tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "fabric", m.MaterialClass)
	require.Equal(t, "alice", m.CreatedBy)
	require.NotNil(t, m.FabricAttr)
	require.True(t, m.FabricAttr.WidthCm.Valid)
	require.True(t, m.FabricAttr.WidthCm.Decimal.Equal(decimal.RequireFromString("150")))
	require.Equal(t, "lengthwise", m.FabricAttr.FabricDirection.String)
	require.Nil(t, m.HardwareAttr, "a fabric material has no hardware attrs")

	// Change the class to hardware: the fabric side-table row must be cleared and hardware appear.
	require.NoError(t, tc.UpdateMaterial(ctx, id, &entity.MaterialInsert{
		Name: "CTI now hardware", Section: "hardware", MaterialClass: "hardware",
		UpdatedBy:    "bob",
		HardwareAttr: &entity.MaterialHardwareAttr{Finish: ns("brushed nickel"), DiameterMm: nd("12.5")},
	}, 0))

	m, err = tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "hardware", m.MaterialClass)
	require.Nil(t, m.FabricAttr, "stale fabric attrs must be cleared on class change")
	require.NotNil(t, m.HardwareAttr)
	require.Equal(t, "brushed nickel", m.HardwareAttr.Finish.String)
	require.Equal(t, "alice", m.CreatedBy, "created_by is not overwritten on update")
	require.Equal(t, "bob", m.UpdatedBy, "updated_by is refreshed")

	// A material with no typed attrs simply keeps nil pointers (no side-table row).
	bare, err := tc.CreateMaterial(ctx, &entity.MaterialInsert{Name: "Bare", Section: "other", MaterialClass: "other"})
	require.NoError(t, err)
	bm, err := tc.GetMaterial(ctx, bare)
	require.NoError(t, err)
	require.Equal(t, "other", bm.MaterialClass)
	require.Nil(t, bm.FabricAttr)
	require.Nil(t, bm.HardwareAttr)
	require.Nil(t, bm.ThreadAttr)
	require.Nil(t, bm.PackagingAttr)
}
