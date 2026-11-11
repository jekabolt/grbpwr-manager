package entity

import (
	"database/sql"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

const (
	cutTestLine  = "01JABCDEFGHJKMNPQRSTVWXYL1"
	cutTestLineB = "01JABCDEFGHJKMNPQRSTVWXYL2"
	cutTestStrip = "01JABCDEFGHJKMNPQRSTVWXYS1"
)

func cutDec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func cutSeam(sizeID int, seam string) TechCardCutStripSize {
	return TechCardCutStripSize{SizeId: sizeID, SeamMm: decimal.NewNullDecimal(cutDec(seam))}
}

func cutManual(sizeID, mm int) TechCardCutStripSize {
	return TechCardCutStripSize{SizeId: sizeID, ManualCutMm: sql.NullInt32{Int32: int32(mm), Valid: true}}
}

func cutFromSeam(fit, overlap string, sizes ...TechCardCutStripSize) TechCardCutStrip {
	return TechCardCutStrip{
		LineKey: cutTestStrip, BomLineKey: cutTestLine, LengthMode: CutStripModeFromSeam,
		FitPct: cutDec(fit), OverlapMm: cutDec(overlap), WidthMm: cutDec("30"), Qty: 1, Sizes: sizes,
	}
}

func cutOneLength(mm int32) TechCardCutStrip {
	return TechCardCutStrip{
		LineKey: cutTestStrip, BomLineKey: cutTestLine, LengthMode: CutStripModeOneLength, FitPct: cutDec("100"),
		WidthMm: cutDec("30"), Qty: 1, OneLengthMm: sql.NullInt32{Int32: mm, Valid: mm != 0},
	}
}

func TestCutStripLengthMm(t *testing.T) {
	seam := func(fit, overlap, mm string) TechCardCutStrip { return cutFromSeam(fit, overlap, cutSeam(1, mm)) }
	bothOnOne := TechCardCutStripSize{
		SizeId: 1, SeamMm: decimal.NewNullDecimal(cutDec("700")), ManualCutMm: sql.NullInt32{Int32: 650, Valid: true},
	}
	perSize := TechCardCutStrip{LengthMode: CutStripModePerSize, FitPct: cutDec("100"), Sizes: []TechCardCutStripSize{cutManual(1, 600)}}
	perSizeSeamOnly := TechCardCutStrip{LengthMode: CutStripModePerSize, FitPct: cutDec("100"), Sizes: []TechCardCutStripSize{cutSeam(1, "700")}}
	oneWithManual := cutOneLength(500)
	oneWithManual.Sizes = []TechCardCutStripSize{cutManual(1, 999)}
	manualZero := cutFromSeam("100", "0", TechCardCutStripSize{SizeId: 1, ManualCutMm: sql.NullInt32{Valid: true}})
	staleSeam := seam("100", "0", "700")
	staleSeam.SeamStale = true
	staleWithManual := cutFromSeam("90", "20", bothOnOne)
	staleWithManual.SeamStale = true

	for name, tc := range map[string]struct {
		strip TechCardCutStrip
		size  int
		mm    int
		known bool
	}{
		"plain formula":                     {seam("90", "20", "700"), 1, 650, true},
		"half rounds up":                    {seam("100", "0", "412.5"), 1, 413, true},
		"just under half rounds down":       {seam("100", "0", "412.49"), 1, 412, true},
		"half from the fit rounds up":       {seam("50", "0", "411"), 1, 206, true},
		"half from the overlap rounds up":   {seam("100", "0.5", "100"), 1, 101, true},
		"manual wins over the formula":      {cutFromSeam("90", "20", bothOnOne), 1, 650, true},
		"a size with no row is unknown":     {seam("90", "20", "700"), 2, 0, false},
		"from seam with no seam is unknown": {cutFromSeam("90", "20", TechCardCutStripSize{SizeId: 1}), 1, 0, false},
		"a zero seam is unknown, not 0":     {seam("100", "0", "0"), 1, 0, false},
		"a manual 0 is no manual":           {manualZero, 1, 0, false},
		"per size reads the typed value":    {perSize, 1, 600, true},
		"per size never uses the formula":   {perSizeSeamOnly, 1, 0, false},
		"one length covers any size":        {cutOneLength(500), 77, 500, true},
		"one length ignores a manual":       {oneWithManual, 1, 500, true},
		"one length with no number":         {cutOneLength(0), 1, 0, false},
		"a stale seam has no length":        {staleSeam, 1, 0, false},
		"a manual wins over a stale seam":   {staleWithManual, 1, 650, true},
	} {
		t.Run(name, func(t *testing.T) {
			mm, known := CutStripLengthMm(&tc.strip, tc.size)
			require.Equal(t, tc.known, known)
			require.Equal(t, tc.mm, mm)
		})
	}
	mm, known := CutStripLengthMm(nil, 1)
	require.False(t, known)
	require.Zero(t, mm)
}

func TestCutStripSewnIsCutMinusOverlap(t *testing.T) {
	strip := cutFromSeam("90", "20", cutSeam(1, "700"))
	require.Equal(t, "630", CutStripSewnMm(&strip, 650).String())
}

func cutBomLine(unit string) []TechCardBomItem {
	return []TechCardBomItem{{
		Id: 502, LineKey: cutTestLine, Section: BomSectionHardware, Name: "rubber",
		Unit: sql.NullString{String: unit, Valid: unit != ""},
	}}
}

func TestCutStripLineNorms(t *testing.T) {
	t.Run("a line's norm is the sum of cut times qty over its strips, in metres", func(t *testing.T) {
		a := cutFromSeam("90", "20", cutSeam(1, "700"), cutSeam(2, "800"))
		b := cutFromSeam("100", "0", cutSeam(1, "100"), cutSeam(2, "100"))
		b.LineKey, b.Qty = "01JABCDEFGHJKMNPQRSTVWXYS2", 2
		norms := CutStripLineNorms(cutBomLine("m"), []TechCardCutStrip{a, b}, []int{1, 2})
		require.Len(t, norms, 1)
		require.True(t, norms[0].Complete)
		require.False(t, norms[0].Flat.Valid)
		require.Equal(t, []string{"0.85", "0.94"}, cutNormStrings(norms[0].BySize))
	})
	t.Run("one length gives one flat norm and needs no range", func(t *testing.T) {
		norms := CutStripLineNorms(cutBomLine("m"), []TechCardCutStrip{cutOneLength(500)}, nil)
		require.Len(t, norms, 1)
		require.True(t, norms[0].Complete)
		require.Equal(t, "0.5", norms[0].Flat.Decimal.String())
		require.Empty(t, norms[0].BySize)
	})
	t.Run("a one length strip beside a graded one makes the line graded", func(t *testing.T) {
		b := cutOneLength(100)
		b.LineKey = "01JABCDEFGHJKMNPQRSTVWXYS2"
		norms := CutStripLineNorms(cutBomLine("m"),
			[]TechCardCutStrip{cutFromSeam("100", "0", cutSeam(1, "700"), cutSeam(2, "800")), b}, []int{1, 2})
		require.False(t, norms[0].Flat.Valid)
		require.Equal(t, []string{"0.8", "0.9"}, cutNormStrings(norms[0].BySize))
	})
	t.Run("an unknown size has no row and the line is not complete", func(t *testing.T) {
		norms := CutStripLineNorms(cutBomLine("m"), []TechCardCutStrip{cutFromSeam("100", "0", cutSeam(1, "700"))}, []int{1, 2})
		require.False(t, norms[0].Complete)
		require.Len(t, norms[0].BySize, 1)
		require.Equal(t, 1, norms[0].BySize[0].SizeId)
	})
	t.Run("a line not bought by the metre gets no number at all", func(t *testing.T) {
		norms := CutStripLineNorms(cutBomLine("pcs"), []TechCardCutStrip{cutOneLength(500)}, nil)
		require.False(t, norms[0].Complete)
		require.False(t, norms[0].Flat.Valid)
		require.Empty(t, norms[0].BySize)
	})
	t.Run("no strips, no norms", func(t *testing.T) {
		require.Nil(t, CutStripLineNorms(cutBomLine("m"), nil, []int{1}))
	})
	t.Run("a card with no range takes its sizes from the rows", func(t *testing.T) {
		norms := CutStripLineNorms(cutBomLine("m"), []TechCardCutStrip{cutFromSeam("100", "0", cutSeam(2, "800"), cutSeam(1, "700"))}, nil)
		require.True(t, norms[0].Complete)
		require.Equal(t, []string{"0.7", "0.8"}, cutNormStrings(norms[0].BySize))
	})
}

func rollCutBom(width string) []TechCardBomItem {
	line := TechCardBomItem{
		Id: 502, LineKey: cutTestLine, Section: BomSectionLining, Name: "binding",
		Unit:    sql.NullString{String: "m", Valid: true},
		Purpose: sql.NullString{String: "binding", Valid: true},
	}
	if width != "" {
		line.FabricWidth = decimal.NewNullDecimal(cutDec(width))
	}
	return []TechCardBomItem{line}
}

func rollCutArticle(id int, width, selvedge string) MaterialWithPrice {
	material := MaterialWithPrice{Material: Material{Id: id}}
	if width != "" {
		material.FabricAttr = &MaterialFabricAttr{
			WidthCm: decimal.NewNullDecimal(cutDec(width)), SelvedgeCm: cutDec(selvedge),
		}
	}
	return material
}

func TestRollCutStripLineNormsUseWholeLanes(t *testing.T) {
	one := cutOneLength(1200)

	t.Run("thirty millimetres from a 140 centimetre roll", func(t *testing.T) {
		norm := DerivedLineNorms(rollCutBom("140"), []TechCardCutStrip{one}, nil, nil)[0]
		require.True(t, norm.Complete)
		require.Equal(t, "0.026", norm.Flat.Decimal.String(), "1200 mm / floor(1400 / 30)")
	})
	t.Run("wide strips use six lanes, not 6.9", func(t *testing.T) {
		wide := one
		wide.WidthMm = cutDec("200")
		norm := DerivedLineNorms(rollCutBom("138"), []TechCardCutStrip{wide}, nil, nil)[0]
		require.Equal(t, "0.2", norm.Flat.Decimal.String())
	})
	t.Run("different widths sum before the only rounding", func(t *testing.T) {
		a, b := one, one
		a.OneLengthMm = sql.NullInt32{Int32: 1000, Valid: true}
		b.OneLengthMm = sql.NullInt32{Int32: 1000, Valid: true}
		a.WidthMm, b.WidthMm = cutDec("300"), cutDec("500")
		b.LineKey = cutTestLineB
		norm := DerivedLineNorms(rollCutBom("140"), []TechCardCutStrip{a, b}, nil, nil)[0]
		require.Equal(t, "0.75", norm.Flat.Decimal.String(), "1000/4 + 1000/2 millimetres")
	})

	for name, strip := range map[string]TechCardCutStrip{
		"from seam":  cutFromSeam("100", "0", cutSeam(1, "1200")),
		"per size":   {BomLineKey: cutTestLine, LineKey: cutTestStrip, LengthMode: CutStripModePerSize, WidthMm: cutDec("30"), Qty: 1, Sizes: []TechCardCutStripSize{cutManual(1, 1200)}},
		"one length": one,
	} {
		t.Run(name, func(t *testing.T) {
			norm := DerivedLineNorms(rollCutBom("140"), []TechCardCutStrip{strip}, nil, []int{1})[0]
			if strip.LengthMode == CutStripModeOneLength {
				require.Equal(t, "0.026", norm.Flat.Decimal.String())
			} else {
				require.Equal(t, []string{"0.026"}, cutNormStrings(norm.BySize))
			}
		})
	}
}

func TestRollCutStripLineNormsAreIncompleteWithoutEveryDimension(t *testing.T) {
	valid := cutOneLength(1200)
	for name, tc := range map[string]struct {
		bom  []TechCardBomItem
		edit func(*TechCardCutStrip)
	}{
		"missing strip width": {rollCutBom("140"), func(s *TechCardCutStrip) { s.WidthMm = decimal.Zero }},
		"missing roll width":  {rollCutBom(""), func(*TechCardCutStrip) {}},
		"strip wider than roll": {rollCutBom("140"), func(s *TechCardCutStrip) {
			s.WidthMm = cutDec("1401")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			strip := valid
			tc.edit(&strip)
			norm := DerivedLineNorms(tc.bom, []TechCardCutStrip{strip}, nil, nil)[0]
			require.False(t, norm.Complete)
			require.False(t, norm.Flat.Valid)
			require.Empty(t, norm.BySize)
		})
	}
}

func TestRollCutStripWidthResolutionAndColourwayPins(t *testing.T) {
	bom := rollCutBom("140")
	bom[0].MaterialId = sql.NullInt64{Int64: 10, Valid: true}
	strip := cutOneLength(1000)
	strip.WidthMm = cutDec("200")
	linked := map[int]MaterialWithPrice{
		10: rollCutArticle(10, "100", "5"), // usable 90 cm: 4 lanes
		20: rollCutArticle(20, "200", "0"), // 10 lanes
		30: rollCutArticle(30, "", "0"),
	}

	defaultNorm := DerivedLineNormsForRecipe(bom, []TechCardCutStrip{strip}, nil, nil, linked, nil)[0]
	require.Equal(t, "0.25", defaultNorm.Flat.Decimal.String(), "article usable width beats the 140 cm line")
	lineOnly := DerivedLineNormsForRecipe(bom, []TechCardCutStrip{strip}, nil, nil, nil, nil)[0]
	require.Equal(t, "0.143", lineOnly.Flat.Decimal.String(), "line width is the default-article fallback")

	usage := func(materialID int64) TechCardColorwayUsage {
		return TechCardColorwayUsage{
			BomItemId:  sql.NullInt64{Int64: 502, Valid: true},
			MaterialId: sql.NullInt64{Int64: materialID, Valid: true},
		}
	}
	pinned := []TechCardColorwayUsage{usage(20)}
	pinnedNorm := DerivedLineNormsForRecipe(bom, []TechCardCutStrip{strip}, nil, nil, linked, pinned)[0]
	require.Equal(t, "0.1", pinnedNorm.Flat.Decimal.String())
	require.Equal(t, "0.25", defaultNorm.Flat.Decimal.String(), "card-level norm stays on the default article")

	sameAsDefault := []TechCardColorwayUsage{usage(10)}
	sameNorm := DerivedLineNormsForRecipe(bom, []TechCardCutStrip{strip}, nil, nil, linked, sameAsDefault)[0]
	require.Equal(t, "0.25", sameNorm.Flat.Decimal.String(), "a pin equal to default is unpinned")

	missing := []TechCardColorwayUsage{usage(30)}
	missingNorm := DerivedLineNormsForRecipe(bom, []TechCardCutStrip{strip}, nil, nil, linked, missing)[0]
	require.False(t, missingNorm.Complete, "a pinned article cannot borrow the line width")

	conflict := []TechCardColorwayUsage{usage(20), usage(30)}
	conflictNorm := DerivedLineNormsForRecipe(bom, []TechCardCutStrip{strip}, nil, nil, linked, conflict)[0]
	require.False(t, conflictNorm.Complete)
}

func TestRollCutStripLeavesTopstitchInRunningMetres(t *testing.T) {
	bom := rollCutBom("140")
	bom[0].ThreadPerMm = decimal.NewNullDecimal(decimal.NewFromInt(1))
	strip := cutOneLength(1200)
	top := TechCardTopstitchLine{
		BomLineKey: cutTestLine, Rows: 1,
		Sizes: []TechCardTopstitchLineSize{{SizeId: 1, LineMm: decimal.NewNullDecimal(decimal.NewFromInt(1000))}},
	}
	norm := DerivedLineNorms(bom, []TechCardCutStrip{strip}, []TechCardTopstitchLine{top}, []int{1})[0]
	require.Equal(t, []string{"1.026"}, cutNormStrings(norm.BySize), "only the strip is divided into lanes")
}

func cutNormStrings(rows []TechCardBomSizeConsumption) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Consumption.String())
	}
	return out
}

func TestResolveCutStripUsages(t *testing.T) {
	bom := cutBomLine("m")
	row := func() TechCardColorwayUsage {
		return TechCardColorwayUsage{BomItemId: sql.NullInt64{Int64: 502, Valid: true}}
	}
	graded := CutStripLineNorms(bom, []TechCardCutStrip{cutFromSeam("100", "0", cutSeam(1, "700"), cutSeam(2, "800"))}, []int{1, 2})
	flat := CutStripLineNorms(bom, []TechCardCutStrip{cutOneLength(500)}, nil)

	t.Run("a colourway with no row gets one that is never written", func(t *testing.T) {
		got := ResolveCutStripUsages(nil, bom, graded)
		require.Len(t, got, 1)
		require.Zero(t, got[0].Id)
		require.True(t, got[0].IsCutLengthNorm())
		require.Equal(t, []string{"0.7", "0.8"}, cutNormStrings(got[0].SizeConsumptions))
		require.False(t, got[0].Consumption.Valid)
	})
	t.Run("a typed number is replaced, the pin and the colour stay", func(t *testing.T) {
		u := row()
		u.Id, u.Consumption = 9, decimal.NewNullDecimal(cutDec("12"))
		u.Quantity = decimal.NewNullDecimal(cutDec("3"))
		u.SizeConsumptions = []TechCardBomSizeConsumption{{SizeId: 1, Consumption: cutDec("99")}}
		u.MaterialId = sql.NullInt64{Int64: 200, Valid: true}
		u.Color = sql.NullString{String: "black", Valid: true}
		u.ConsumptionSource = sql.NullString{String: ConsumptionSourceManual, Valid: true}
		got := ResolveCutStripUsages([]TechCardColorwayUsage{u}, bom, flat)
		require.Len(t, got, 1)
		require.Equal(t, "0.5", got[0].Consumption.Decimal.String())
		require.False(t, got[0].Quantity.Valid)
		require.Empty(t, got[0].SizeConsumptions)
		require.True(t, got[0].IsCutLengthNorm())
		require.Equal(t, int64(200), got[0].MaterialId.Int64)
		require.Equal(t, "black", got[0].Color.String)
	})
	t.Run("a second placement cannot multiply the norm", func(t *testing.T) {
		got := ResolveCutStripUsages([]TechCardColorwayUsage{row(), row()}, bom, flat)
		require.Len(t, got, 2)
		require.Equal(t, "0.5", got[0].Consumption.Decimal.String())
		require.True(t, got[1].Consumption.Valid)
		require.True(t, got[1].Consumption.Decimal.IsZero())
	})
	t.Run("a piece-bound row states a fabric and stays as it is", func(t *testing.T) {
		piece := row()
		piece.PieceId = sql.NullInt64{Int64: 7, Valid: true}
		got := ResolveCutStripUsages([]TechCardColorwayUsage{piece}, bom, flat)
		require.Len(t, got, 2, "the norm gets a row of its own")
		require.False(t, got[0].IsCutLengthNorm())
		require.False(t, got[0].Consumption.Valid)
		require.Equal(t, "0.5", got[1].Consumption.Decimal.String())
	})
	t.Run("a positional row is part of the slot", func(t *testing.T) {
		pos := TechCardColorwayUsage{BomItemIndex: sql.NullInt32{Int32: 0, Valid: true}}
		got := ResolveCutStripUsages([]TechCardColorwayUsage{pos}, bom, flat)
		require.Len(t, got, 1)
		require.Equal(t, "0.5", got[0].Consumption.Decimal.String())
	})
	t.Run("a row of another line is not touched", func(t *testing.T) {
		other := TechCardColorwayUsage{BomItemId: sql.NullInt64{Int64: 501, Valid: true}, Consumption: decimal.NewNullDecimal(cutDec("2"))}
		got := ResolveCutStripUsages([]TechCardColorwayUsage{other}, bom, flat)
		require.Len(t, got, 2)
		require.Equal(t, "2", got[0].Consumption.Decimal.String())
		require.False(t, got[0].IsCutLengthNorm())
	})
	t.Run("an unknown length gives a row with no number, not a zero", func(t *testing.T) {
		unknown := CutStripLineNorms(bom, []TechCardCutStrip{cutFromSeam("100", "0")}, []int{1, 2})
		got := ResolveCutStripUsages(nil, bom, unknown)
		require.Len(t, got, 1)
		require.False(t, got[0].Consumption.Valid)
		require.Empty(t, got[0].SizeConsumptions)
	})
}

func TestApplyCutStripNormsMarksTheLineOnACardWithNoColourways(t *testing.T) {
	bom := cutBomLine("m")
	ApplyCutStripNorms(bom, []TechCardCutStrip{cutOneLength(500)}, []int{1}, nil)
	require.True(t, bom[0].CutToLength)

	untouched := cutBomLine("m")
	ApplyCutStripNorms(untouched, nil, []int{1}, nil)
	require.False(t, untouched[0].CutToLength)
}

// The mark silences the countable path of a hardware line, and only the mark does.
func TestCutToLengthLeavesTheCountablePath(t *testing.T) {
	line := TechCardBomItem{Section: BomSectionHardware, QtyPerGarment: decimal.NewNullDecimal(cutDec("5")), Unit: sql.NullString{String: "m", Valid: true}}
	require.True(t, SlotCarriesCountableNorm(&line, decimal.NullDecimal{}))
	line.CutToLength = true
	require.False(t, SlotCarriesCountableNorm(&line, decimal.NullDecimal{}))
	row := &TechCardColorwayUsage{Quantity: decimal.NewNullDecimal(cutDec("3"))}
	qty, basis := CountablePairQty([]*TechCardColorwayUsage{row}, &line, decimal.NullDecimal{})
	require.False(t, qty.Valid)
	require.Equal(t, CountableBasisNone, basis)

	line.CutToLength = false
	qty, basis = CountablePairQty([]*TechCardColorwayUsage{row}, &line, decimal.NullDecimal{})
	require.True(t, qty.Valid)
	require.Equal(t, CountableBasisRows, basis)
}

func TestClearCutStripRecipeRow(t *testing.T) {
	bare := TechCardColorwayUsage{
		Consumption:       decimal.NewNullDecimal(cutDec("0.5")),
		ConsumptionSource: sql.NullString{String: ConsumptionSourceCutLength, Valid: true},
		MaterialIdSet:     true,
	}
	require.False(t, ClearCutStripRecipeRow(&bare), "an echo of the made-up row is not stored")

	pinned := TechCardColorwayUsage{
		Consumption:   decimal.NewNullDecimal(cutDec("12")),
		MaterialId:    sql.NullInt64{Int64: 200, Valid: true},
		MaterialIdSet: true,
	}
	require.True(t, ClearCutStripRecipeRow(&pinned))
	require.False(t, pinned.Consumption.Valid, "the norm of a strip line is not typed")
	require.Equal(t, ConsumptionSourceManual, pinned.ConsumptionSource.String)

	coloured := TechCardColorwayUsage{Color: sql.NullString{String: "black", Valid: true}, MaterialIdSet: true}
	require.True(t, ClearCutStripRecipeRow(&coloured))

	stale := TechCardColorwayUsage{Consumption: decimal.NewNullDecimal(cutDec("1"))}
	require.True(t, ClearCutStripRecipeRow(&stale), "a stale client's row has a stored pin to keep")
	require.False(t, stale.Consumption.Valid)
}

// StoredRow is the row as the table holds it: a resolved row gives back what the resolver replaced, the
// second row of a line included, and a row the resolver made up has nothing to give.
func TestCutStripStoredRow(t *testing.T) {
	bom := cutBomLine("m")
	flat := CutStripLineNorms(bom, []TechCardCutStrip{cutOneLength(500)}, nil)
	line := sql.NullInt64{Int64: 502, Valid: true}
	usages := []TechCardColorwayUsage{
		{
			Id: 7, BomItemId: line,
			Consumption: decimal.NewNullDecimal(cutDec("0.75")), Quantity: decimal.NewNullDecimal(cutDec("2")),
			ConsumptionSource: sql.NullString{String: ConsumptionSourceMarker, Valid: true},
			WasteSelvedgePct:  decimal.NewNullDecimal(cutDec("1.65")), WasteCutPct: decimal.NewNullDecimal(cutDec("21.95")),
			NormMarkerId:     sql.NullInt64{Int64: 3, Valid: true},
			SizeConsumptions: []TechCardBomSizeConsumption{{SizeId: 1, Consumption: cutDec("0.7")}},
		},
		{Id: 8, BomItemId: line, Consumption: decimal.NewNullDecimal(cutDec("1.1"))},
	}
	plain := TechCardColorwayUsage{Id: 9, Consumption: decimal.NewNullDecimal(cutDec("4"))}
	require.Equal(t, plain, plain.StoredRow(), "a row the resolver did not touch")

	resolved := ResolveCutStripUsages(usages, bom, flat)
	resolved = ResolveCutStripUsages(resolved, bom, flat) // a second pass does not lose them
	require.True(t, resolved[0].Consumption.Decimal.Equal(cutDec("0.5")))
	require.True(t, resolved[1].Consumption.Decimal.IsZero())

	first := resolved[0].StoredRow()
	require.True(t, first.Consumption.Decimal.Equal(cutDec("0.75")))
	require.True(t, first.Quantity.Decimal.Equal(cutDec("2")))
	require.Equal(t, ConsumptionSourceMarker, first.ConsumptionSource.String)
	require.True(t, first.WasteCutPct.Decimal.Equal(cutDec("21.95")))
	require.Equal(t, int64(3), first.NormMarkerId.Int64)
	require.Len(t, first.SizeConsumptions, 1)
	require.True(t, resolved[1].StoredRow().Consumption.Decimal.Equal(cutDec("1.1")))

	madeUp := ResolveCutStripUsages(nil, bom, flat)
	require.Len(t, madeUp, 1)
	blank := madeUp[0].StoredRow()
	require.False(t, blank.Consumption.Valid)
	require.False(t, blank.ConsumptionSource.Valid)
}

func TestCarryStoredCutStrips(t *testing.T) {
	strip := cutFromSeam("100", "0", cutSeam(1, "700"), cutSeam(2, "800"))
	gone := cutOneLength(500)
	gone.BomLineKey = cutTestLineB
	stored := &TechCardInsert{CutStrips: []TechCardCutStrip{strip, gone}}

	in := &TechCardInsert{BomItems: []TechCardBomItem{{LineKey: cutTestLine}}, SizeIds: []int{1}}
	CarryStoredCutStrips(in, stored)
	require.Len(t, in.CutStrips, 1, "a strip whose line goes, goes with it")
	require.Len(t, in.CutStrips[0].Sizes, 1, "a size the save removes from the range goes too")
	require.Len(t, stored.CutStrips[0].Sizes, 2, "the stored card is not edited")

	aware := &TechCardInsert{CutStripsAware: true, BomItems: []TechCardBomItem{{LineKey: cutTestLine}}}
	CarryStoredCutStrips(aware, stored)
	require.Empty(t, aware.CutStrips, "an aware client said what it wants")
}

func TestNormalizeCutStrip(t *testing.T) {
	valid := func() TechCardCutStrip { return cutFromSeam("90", "20", cutSeam(2, "800"), cutSeam(1, "700")) }

	t.Run("defaults and order", func(t *testing.T) {
		s := valid()
		s.FitPct, s.Qty = decimal.Zero, 0
		require.NoError(t, NormalizeCutStrip(&s, "cut_strips[0]"))
		require.Equal(t, "100", s.FitPct.String())
		require.Equal(t, 1, s.Qty)
		require.Equal(t, 1, s.Sizes[0].SizeId)
	})
	t.Run("a width nobody typed yet still saves", func(t *testing.T) {
		s := valid()
		s.WidthMm = decimal.Zero
		require.NoError(t, NormalizeCutStrip(&s, "p"))
	})
	t.Run("an empty size row is no row", func(t *testing.T) {
		s := valid()
		s.Sizes = append(s.Sizes, TechCardCutStripSize{SizeId: 3})
		require.NoError(t, NormalizeCutStrip(&s, "p"))
		require.Len(t, s.Sizes, 2)
	})
	for name, spoil := range map[string]func(*TechCardCutStrip){
		"no line":          func(s *TechCardCutStrip) { s.BomLineKey = " " },
		"unknown mode":     func(s *TechCardCutStrip) { s.LengthMode = "by_eye" },
		"fit over the cap": func(s *TechCardCutStrip) { s.FitPct = cutDec("201") },
		"negative fit":     func(s *TechCardCutStrip) { s.FitPct = cutDec("-1") },
		"negative overlap": func(s *TechCardCutStrip) { s.OverlapMm = cutDec("-1") },
		"negative width":   func(s *TechCardCutStrip) { s.WidthMm = cutDec("-1") },
		"negative qty":     func(s *TechCardCutStrip) { s.Qty = -1 },
		"qty over the cap": func(s *TechCardCutStrip) { s.Qty = CutStripMaxQty + 1 },
		"long seam key": func(s *TechCardCutStrip) {
			s.SeamKey = sql.NullString{String: "012345678901234567890123456", Valid: true}
		},
		"negative one size": func(s *TechCardCutStrip) { s.OneLengthMm = sql.NullInt32{Int32: -1, Valid: true} },
		"size with no id":   func(s *TechCardCutStrip) { s.Sizes[0].SizeId = 0 },
		"twice one size":    func(s *TechCardCutStrip) { s.Sizes[1].SizeId = s.Sizes[0].SizeId },
		"negative seam":     func(s *TechCardCutStrip) { s.Sizes[0].SeamMm = decimal.NewNullDecimal(cutDec("-1")) },
		"manual over the cap": func(s *TechCardCutStrip) {
			s.Sizes[0].ManualCutMm = sql.NullInt32{Int32: CutStripMaxMm + 1, Valid: true}
		},
		"one length without a length": func(s *TechCardCutStrip) { s.LengthMode = CutStripModeOneLength },
	} {
		t.Run(name, func(t *testing.T) {
			s := valid()
			spoil(&s)
			require.Error(t, NormalizeCutStrip(&s, "p"))
		})
	}
}

func TestCutStripLineProblem(t *testing.T) {
	require.Equal(t, "", CutStripLineProblem("m", BomSectionHardware, ""), "the golden line")
	require.Equal(t, "", CutStripLineProblem("m", BomSectionTrim, ""))
	require.Equal(t, "", CutStripLineProblem("m", BomSectionLining, "lining"))
	require.Equal(t, "cut_strip_not_metres", CutStripLineProblem("pcs", BomSectionHardware, ""))
	require.Equal(t, "cut_strip_main_fabric", CutStripLineProblem("m", BomSectionFabric, "main"))
	require.Equal(t, "", CutStripLineProblem("m", BomSectionFabric, ""),
		"a fabric line with no purpose is not sorted yet, it is not known to be the main fabric")
	require.Equal(t, "", CutStripLineProblem("m", BomSectionFabric, "binding"))
}

func TestStripInputsClassifyAsStrips(t *testing.T) {
	steps := ClassifyAssemblyInputs(map[string]bool{"FR": true}, map[string]bool{cutTestStrip: true}, []string{"FR", cutTestStrip, "UNIT"})
	require.Equal(t, []OperationInput{
		{Kind: AssemblyInputPiece, Key: "FR"},
		{Kind: AssemblyInputStrip, Key: cutTestStrip},
		{Kind: AssemblyInputUnit, Key: "UNIT"},
	}, steps)
}

func sweepRules(res AssemblyResult) []AssemblyRule {
	out := make([]AssemblyRule, 0, len(res.Violations))
	for _, v := range res.Violations {
		out = append(out, v.Rule)
	}
	return out
}

// A strip joins a step the way a piece does and never shows on the table. Rule 4 does not ask where it went.
func TestStripInTheAssemblySweep(t *testing.T) {
	pieces := []AssemblyPiece{{LineKey: "FR", Name: "front"}, {LineKey: "BK", Name: "back"}}
	strip := OperationInput{Kind: AssemblyInputStrip, Key: cutTestStrip}
	front := OperationInput{Kind: AssemblyInputPiece, Key: "FR"}
	back := OperationInput{Kind: AssemblyInputPiece, Key: "BK"}

	t.Run("an input of a step", func(t *testing.T) {
		steps := []AssemblyStep{{Inputs: []OperationInput{front, back, strip}, OutputUnitKey: "SHELL"}}
		res := AssemblySweep(pieces, steps)
		require.Empty(t, res.Violations)
		require.Equal(t, []string{"SHELL"}, res.Frontier)
		require.Empty(t, AssemblyReleaseCheck(pieces, steps, res))
	})
	t.Run("a strip nobody takes is not a hole", func(t *testing.T) {
		steps := []AssemblyStep{{Inputs: []OperationInput{front, back}, OutputUnitKey: "SHELL"}}
		res := AssemblySweep(pieces, steps)
		require.Empty(t, res.Violations)
		require.Empty(t, AssemblyReleaseCheck(pieces, steps, res))
	})
	t.Run("a step cannot take it twice", func(t *testing.T) {
		steps := []AssemblyStep{
			{Inputs: []OperationInput{front, back, strip}, OutputUnitKey: "SHELL"},
			{Inputs: []OperationInput{{Kind: AssemblyInputUnit, Key: "SHELL"}, strip}, OutputUnitKey: "DONE"},
		}
		require.Contains(t, sweepRules(AssemblySweep(pieces, steps)), AssemblyRuleSingleUse)
	})
	t.Run("a unit cannot take the key of a strip", func(t *testing.T) {
		steps := []AssemblyStep{{Inputs: []OperationInput{front, back, strip}, OutputUnitKey: cutTestStrip}}
		require.Contains(t, sweepRules(AssemblySweep(pieces, steps)), AssemblyRuleNamespace)
	})
}
