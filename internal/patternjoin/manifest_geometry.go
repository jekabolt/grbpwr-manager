package patternjoin

import (
	"encoding/json"
	"math"
	"slices"
)

// Compare writer-authored semantic markers with the actual entities, as the fresh
// reader would. In this narrow profile every internal mark is a typed three-point dart.
func verifyManifestGeometry(raw []byte, blocks map[string]*block, s Selection, manifestPurpose string) error {
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil || !only(doc, "v", "generator", "createdAt", "techCardId", "scope", "units", "layers", "cutLayerIsFinal", "allowanceMm", "sizes", "pieces", "blocks", "source", "gate", "passport") {
		return invalid
	}
	var allowance *float64
	if json.Unmarshal(doc["allowanceMm"], &allowance) != nil || allowance == nil || *allowance != 0 {
		return invalid
	}
	var layers map[string]string
	if json.Unmarshal(doc["layers"], &layers) != nil || len(layers) != 5 || layers["cut"] != "1" || layers["seam"] != "14" || layers["notch"] != "4" || layers["internal"] != "8" || layers["grain"] != "7" {
		return invalid
	}
	var pieces []map[string]json.RawMessage
	if json.Unmarshal(doc["pieces"], &pieces) != nil {
		return invalid
	}
	for _, m := range pieces {
		if !only(m, "identity", "code", "mods", "displayName", "pairHand", "pairOf", "unfoldedFold", "piecesPerGarment", "fabrics", "fused", "ungraded", "allowanceMm", "nameOrigin") {
			return invalid
		}
		var fabrics []string
		wanted := []string{s.FabricPurpose, s.BOMLineKey}
		if manifestPurpose != s.FabricPurpose {
			wanted = []string{s.FabricPurpose, manifestPurpose, s.BOMLineKey}
		}
		if json.Unmarshal(m["fabrics"], &fabrics) != nil || !slices.Equal(fabrics, wanted) ||
			string(m["allowanceMm"]) != "0" || string(m["piecesPerGarment"]) != "1" || string(m["mods"]) != "[]" || string(m["pairHand"]) != "null" || string(m["pairOf"]) != "null" ||
			string(m["fused"]) != "false" || string(m["ungraded"]) != "false" || string(m["unfoldedFold"]) != "false" ||
			string(m["code"]) != string(m["identity"]) || string(m["displayName"]) != string(m["identity"]) {
			return invalid
		}
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(doc["blocks"], &entries) != nil {
		return invalid
	}
	for _, m := range entries {
		if !only(m, "block", "identity", "sizeToken", "sizeId", "bboxMm", "areaMm2", "hasGrain", "notches", "drills", "internal", "hasSeam", "grain", "contour", "authoredInternal", "authoredText") {
			return invalid
		}
		var name string
		if json.Unmarshal(m["block"], &name) != nil || blocks[name] == nil {
			return invalid
		}
		b := blocks[name]
		grain := struct {
			Origin   string
			Evidence []string
		}{}
		if json.Unmarshal(m["grain"], &grain) != nil || grain.Origin != "detected" || !slices.Equal(grain.Evidence, []string{"dxf-layer"}) {
			return invalid
		}
		if string(m["notches"]) != "0" || string(m["hasSeam"]) != "true" || string(m["hasGrain"]) != "true" {
			return invalid
		}
		p, ok := rectangle(b)
		if !ok {
			return invalid
		}
		var bbox []float64
		if json.Unmarshal(m["bboxMm"], &bbox) != nil || !slices.Equal(bbox, []float64{millimetres(p[0].X), millimetres(p[0].Y), millimetres(p[2].X), millimetres(p[2].Y)}) {
			return invalid
		}
		w, h := p[2].X-p[0].X, p[2].Y-p[0].Y
		// Keep the manifest's 0.1 mm differential contour exact in this profile.
		var contour struct {
			Dev *int
			Pts []int64
		}
		if w%100000 != 0 || h%100000 != 0 || json.Unmarshal(m["contour"], &contour) != nil || contour.Dev == nil || *contour.Dev != 0 || !slices.Equal(contour.Pts, []int64{0, 0, w / 100000, 0, 0, h / 100000, -w / 100000, 0}) {
			return invalid
		}
		area := w * h
		var holes [][2]coord
		for _, hole := range b.Outer[1:] {
			q := hole.Points
			if len(q) != 4 || q[0].X >= q[2].X || q[0].Y >= q[2].Y || q[1] != (coord{q[0].X, q[2].Y}) || q[3] != (coord{q[2].X, q[0].Y}) || !inside(q[0], p) || !inside(q[2], p) {
				return invalid
			}
			for _, prev := range holes {
				if q[0].X <= prev[1].X && q[2].X >= prev[0].X && q[0].Y <= prev[1].Y && q[2].Y >= prev[0].Y {
					return invalid
				}
			}
			holes = append(holes, [2]coord{q[0], q[2]})
			area -= (q[2].X - q[0].X) * (q[2].Y - q[0].Y)
		}
		var declared float64
		if json.Unmarshal(m["areaMm2"], &declared) != nil || declared != float64(area)/1e12 || math.IsNaN(declared) {
			return invalid
		}
		if err := verifyMarks(m, b, p); err != nil {
			return err
		}
	}
	return nil
}
func millimetres(v int64) float64 { return float64(v) / 1e6 }
func inside(q coord, p []coord) bool {
	return q.X > p[0].X && q.X < p[2].X && q.Y > p[0].Y && q.Y < p[2].Y
}
func only(m map[string]json.RawMessage, allowed ...string) bool {
	for k := range m {
		if !slices.Contains(allowed, k) {
			return false
		}
	}
	return true
}
func verifyMarks(m map[string]json.RawMessage, b *block, outer []coord) error {
	var internal []struct {
		Index    *int
		Label    string
		Closed   *bool
		Vertices []int64
	}
	var texts []struct {
		Anchor []int64
		Text   string
	}
	if raw := m["authoredInternal"]; raw != nil && json.Unmarshal(raw, &internal) != nil {
		return invalid
	}
	if raw := m["authoredText"]; raw != nil && json.Unmarshal(raw, &texts) != nil {
		return invalid
	}
	din, txt, drills := 0, 0, 0
	for _, p := range b.Marks {
		for _, q := range p.Points {
			if !inside(q, outer) {
				return invalid
			}
		}
		if p.Layer == "8" {
			if p.Closed {
				drills++
				continue
			}
			if din >= len(internal) || len(p.Points) != 3 {
				return invalid
			}
			v := internal[din]
			if v.Index == nil || *v.Index != din || v.Label != "draft-cad:dart" || v.Closed == nil || *v.Closed || len(v.Vertices) != 6 {
				return invalid
			}
			for i, q := range p.Points {
				if v.Vertices[2*i] != q.X || v.Vertices[2*i+1] != q.Y {
					return invalid
				}
			}
			din++
		}
		if p.Layer == "GRBPWR_CAD_TEXT" {
			if txt >= len(texts) {
				return invalid
			}
			v := texts[txt]
			q := p.Points[0]
			if len(v.Anchor) != 2 || v.Anchor[0] != q.X || v.Anchor[1] != q.Y || v.Text != p.Style[0] || v.Text == "" {
				return invalid
			}
			txt++
		}
	}
	var countInternal, countDrills *int
	if json.Unmarshal(m["internal"], &countInternal) != nil || json.Unmarshal(m["drills"], &countDrills) != nil || countInternal == nil || countDrills == nil || *countDrills != drills || *countInternal != din || din != len(internal) || txt != len(texts) {
		return invalid
	}
	return nil
}
