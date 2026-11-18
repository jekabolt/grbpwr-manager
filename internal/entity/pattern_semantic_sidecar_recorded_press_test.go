package entity

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func recordedPressFixture(t *testing.T, alter func(map[string]any, []map[string]any, *PatternProductCompositeSidecarExpected)) compositeV7TestFixture {
	t.Helper()
	return buildCompositeV7TestFixtureWithGraph(t, savedFullAssemblyNativeDrawnSewingProfileV1, func(g map[string]any, e *PatternProductCompositeSidecarExpected) {
		raw, err := os.ReadFile("testdata/recorded-terminal-press-operations.json")
		require.NoError(t, err)
		var rows []map[string]any
		require.NoError(t, json.Unmarshal(raw, &rows))
		rawKeys := rows[0]["inputKeys"].([]any)
		g["profile"] = recordedTerminalPressGraphProfile
		g["productionOrder"] = []any{map[string]any{"role": "simulated-stitch", "sourceOperationNumber": 10}, map[string]any{"role": "recorded-terminal-press", "sourceOperationNumber": 20, "canonicalSourceOperation": string(canonicalV5TestJSON(t, rows[1]))}}
		g["pieces"] = []any{map[string]any{"pieceLineKey": rawKeys[0]}, map[string]any{"pieceLineKey": rawKeys[1]}}
		g["seams"] = []any{map[string]any{"id": "seam", "sourceSeamKey": rows[0]["seamKeys"].([]any)[0], "clientOperationId": "join"}}
		g["operations"] = []any{map[string]any{"kind": "stitch", "stage": 0, "order": 0, "sourceOperationNumber": 10, "clientOperationId": "join", "seamIds": []any{"seam"}, "inputKeys": rawKeys, "outputUnitKey": ""}}
		valid := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
		e.SelectedOperationNumber = 10
		e.AssemblyOrderCard = &TechCard{Id: e.Row.TechCardID}
		e.AssemblyOrderCard.Operations = []TechCardOperation{{OperationType: OpTypeMachine, OperationNumber: sql.NullInt32{Int32: 10, Valid: true}}, {
			OperationType: OpTypePress, Zone: ZoneOther, OperationNumber: sql.NullInt32{Int32: 20, Valid: true}, Note: valid(rows[1]["note"].(string)), Work: valid("press_flat"), PressEquipment: valid("iron"), SeamKeys: []string{},
		}}
		if alter != nil {
			alter(g, rows, e)
		}
		e.AssemblyOrderContentCanonicalJSON = string(canonicalV5TestJSON(t, map[string]any{"operationsPayloadCanonicalJson": string(canonicalV5TestJSON(t, []any{rows[0], rows[1]}))}))
		digest := sha256.Sum256(append([]byte("saved-full-assembly-physical-ao-content/1\x00"), []byte(e.AssemblyOrderContentCanonicalJSON)...))
		e.AssemblyOrderContentSHA256 = hex.EncodeToString(digest[:])
	})
}

func TestV7RecordedProductionPressPreservesCurrentV6AndExactSource(t *testing.T) {
	f := recordedPressFixture(t, nil)
	require.NoError(t, ValidatePatternProductCompositeSidecarV7Binding(f.Payload, f.Expected))
	require.NoError(t, ValidatePatternProductCompositeSidecarV7Transition(f.Payload, PatternProductCompositeSidecarV6MediaType, f.V6))
	require.NoError(t, ValidateStoredPatternProductCompositeSidecarV7Exact(f.Payload, f.Expected.Row, f.Expected.PatternVersion, f.Expected.PostSaveReceiptSHA256))
	expected := f.Expected
	expected.AssemblyOrderContentCanonicalJSON = ""
	require.Error(t, ValidatePatternProductCompositeSidecarV7Binding(f.Payload, expected))
	expected = f.Expected
	expected.AssemblyOrderCard = nil
	require.Error(t, ValidatePatternProductCompositeSidecarV7Binding(f.Payload, expected))
	expected = f.Expected
	expected.SelectedOperationNumber = 20
	require.Error(t, ValidatePatternProductCompositeSidecarV7Binding(f.Payload, expected))
	legacy := buildCompositeV7TestFixtureWithProfile(t, savedFullAssemblyNativeDrawnSewingProfileV1)
	legacy.Expected.AssemblyOrderCard = f.Expected.AssemblyOrderCard
	require.ErrorContains(t, ValidatePatternProductCompositeSidecarV7Binding(legacy.Payload, legacy.Expected), "legacy stitch graph")
}

func TestV7RecordedPressRefusesResealedManifestAndSourceDrift(t *testing.T) {
	for _, name := range []string{"inputKeys", "pieceIds", "foldKeys", "seamKeys", "bomLineKeys", "outputUnitKey", "press", "pressTemperatureC", "note", "extra", "order", "piece-injection", "seam-replacement", "inherited-iron", "stitch-order", "operation-extra", "hidden-geometry", "hidden-zero-setting", "forged-admitted-work"} {
		t.Run(name, func(t *testing.T) {
			f := recordedPressFixture(t, func(g map[string]any, rows []map[string]any, e *PatternProductCompositeSidecarExpected) {
				switch name {
				case "note":
					rows[1]["note"] = "new note after review"
				case "order":
					rows[0], rows[1] = rows[1], rows[0]
				case "piece-injection":
					g["pieces"].([]any)[1].(map[string]any)["pieceLineKey"] = "other"
				case "seam-replacement":
					g["seams"].([]any)[0].(map[string]any)["sourceSeamKey"] = "another-seam"
				case "stitch-order":
					g["operations"].([]any)[0].(map[string]any)["order"] = 1
				case "operation-extra":
					g["operations"].([]any)[0].(map[string]any)["press"] = true
				case "inherited-iron":
					e.AssemblyOrderCard.Construction = &TechCardConstruction{EquipmentDefaults: &TechCardEquipmentDefaults{Presses: []TechCardPressProfile{{PressEquipment: "iron", PressTemperatureC: sql.NullInt32{Valid: true, Int32: 150}}}}}
				case "hidden-geometry":
					e.AssemblyOrderCard.Operations[1].AutoFusingPieceKey = sql.NullString{String: "piece", Valid: true}
				case "hidden-zero-setting":
					e.AssemblyOrderCard.Operations[1].PressSteam = sql.NullBool{Bool: false, Valid: true}
				case "forged-admitted-work":
					rows[1]["work"] = "ease_in"
				case "pressTemperatureC":
					rows[1][name] = 160
				case "outputUnitKey":
					rows[1][name] = "body"
				case "press":
					rows[1][name] = map[string]any{"action": "PRESS_ACTION_MOULD"}
				default:
					rows[1][name] = []any{"attached"}
				}
				if name != "note" && name != "order" {
					g["productionOrder"].([]any)[1].(map[string]any)["canonicalSourceOperation"] = string(canonicalV5TestJSON(t, rows[1]))
				}
			})
			require.Error(t, ValidatePatternProductCompositeSidecarV7Binding(f.Payload, f.Expected))
		})
	}
}
