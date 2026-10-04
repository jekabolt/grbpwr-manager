package admin

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
)

// ─────────────────────────── ARTWORK НА РЕНДЕРЕ (70-ROUND7 B7) ───────────────────────────
//
// РЕНДЕР НЕСЁТ АРТВОРКИ, РАЗМЕЩЁННЫЕ НА ФЛЭТАХ ВЕРСТАКА. Артворк (принт, вышивка, нашивка) — это
// строка BOM секции DECORATION, его картинка — ассет рода hardware, привязанный к паре (колорвей,
// строка), а место на изделии — design_asset_placement: ОДНА метка на ОДНОМ флэте, четырёхугольник
// (POLYGON из 4 углов TL, TR, BR, BL) в долях кадра этого флэта.
//
// ⚠ БЕЗ PROTO, И ЭТО РЕШЕНИЕ ПЛАНА: сервер на запуске уже держит верстак, колорвей и полосу —
// он сам читает разметку, ЗАМОРАЖИВАЕТ её копией в снимок входов (`inputs.artworks`, ключ,
// которого нет в DesignInputSnapshot — читатели полосы разбирают снимок с DiscardUnknown, а
// designgen читает его своим узким читателем) и промпт рендера пишет абзац ARTWORK по копии.
// История читает копию: живая разметка полосы может позже разойтись — и это правильно.

// designMaxRunArtworks — сколько артворков уезжает в один рендер. Каждый — лишняя картинка вызова,
// а потолок картинок у движка общий с плитами, референсами, картами и тканями. Больше — отказ
// двери (designErrorCodeTooManyArtworks), а не тихая обрезка.
const designMaxRunArtworks = 4

// designErrorCodeTooManyArtworks — refusal reason: the render would carry more placed artworks than
// designMaxRunArtworks.
const designErrorCodeTooManyArtworks = "too_many_artworks"

// designFrozenArtwork — ОДИН размещённый артворк, замороженный в снимок входов прогона. Ключи
// snake_case — их читает designgen.artworkUse.
type designFrozenArtwork struct {
	AssetID     int                 `json:"asset_id"`
	BomItemID   int                 `json:"bom_item_id"`
	Name        string              `json:"name"`
	MediaID     int                 `json:"media_id"`
	View        string              `json:"view"`
	PictureID   int                 `json:"picture_id"`
	FlatMediaID int                 `json:"flat_media_id"`
	Corners     []designArtworkCorn `json:"corners"`
	Note        string              `json:"note"`
}

// designArtworkCorn — угол четырёхугольника в долях кадра флэта (0..1, x слева, y сверху).
type designArtworkCorn struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// designArtworkQuad reads a placement's annotation as the four corners TL, TR, BR, BL.
//
// POLYGON из ровно четырёх точек — четырёхугольник как есть (свободная перспектива). DIM/BRACKET из
// двух точек — легаси-прямоугольник «левый верх, правый низ» (так рисовала выноска-артворк), и он
// разворачивается в четыре угла. Всё остальное — не место артворка, и оно молча не едет: метка,
// которую нельзя назвать четырьмя углами, не может стать абзацем промпта.
func designArtworkQuad(raw []byte) ([]designArtworkCorn, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	a := &pb_common.TechCardAnnotation{}
	if err := designUnmarshalJSON(raw, a); err != nil {
		return nil, false
	}
	pts := make([]designArtworkCorn, 0, len(a.GetPoints()))
	for _, p := range a.GetPoints() {
		x, errX := strconv.ParseFloat(strings.TrimSpace(p.GetX().GetValue()), 64)
		y, errY := strconv.ParseFloat(strings.TrimSpace(p.GetY().GetValue()), 64)
		if errX != nil || errY != nil || x < 0 || x > 1 || y < 0 || y > 1 {
			return nil, false
		}
		pts = append(pts, designArtworkCorn{X: x, Y: y})
	}
	switch a.GetKind() {
	case pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON:
		if len(pts) != 4 {
			return nil, false
		}
		return pts, true
	case pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM,
		pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_BRACKET:
		if len(pts) != 2 {
			return nil, false
		}
		x0, x1 := pts[0].X, pts[1].X
		y0, y1 := pts[0].Y, pts[1].Y
		if x0 > x1 {
			x0, x1 = x1, x0
		}
		if y0 > y1 {
			y0, y1 = y1, y0
		}
		if x1-x0 <= 0 || y1-y0 <= 0 {
			return nil, false
		}
		return []designArtworkCorn{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}, true
	}
	return nil, false
}

// designFreezeArtworks — артворки этого рендер-прогона: разметка на ФЛЭТАХ, которые прогон
// действительно отправляет (inputs.slots), ассетов, привязанных к колорвею прогона на строках
// DECORATION. Колорвей 0 — артворков нет (связка без колорвея не существует). Порядок —
// стабильный: по виду (как плиты), затем по id разметки.
func designFreezeArtworks(kind string, params *pb_common.DesignRunParams, card *entity.TechCard,
	band *entity.DesignBand, inputs *pb_common.DesignInputSnapshot) []designFrozenArtwork {
	if kind != entity.DesignRunKindRender || band == nil || card == nil || inputs == nil {
		return nil
	}
	cw := int(params.GetColorwayId())
	if cw <= 0 {
		return nil
	}
	// Флэты, которые уезжают: media плит снимка.
	sent := map[int]string{}
	for _, s := range inputs.GetSlots() {
		if s.GetMediaId() > 0 {
			sent[int(s.GetMediaId())] = s.GetViewKey()
		}
	}
	// picture_id флэта верстака → (вид, media), только для отправляемых плит.
	type flat struct {
		view  string
		media int
	}
	flats := map[int]flat{}
	for _, slot := range band.Bench {
		if entity.DesignKindOrFlat(slot.Kind) != entity.DesignPictureKindFlat || slot.Picture == nil ||
			!slot.PictureId.Valid || slot.Picture.MediaId <= 0 {
			continue
		}
		if _, ok := sent[slot.Picture.MediaId]; !ok {
			continue
		}
		flats[int(slot.PictureId.Int32)] = flat{view: slot.ViewKey, media: slot.Picture.MediaId}
	}
	if len(flats) == 0 {
		return nil
	}
	decoration := map[int]entity.TechCardBomItem{}
	for _, line := range card.BomItems {
		if line.Section == entity.BomSectionDecoration {
			decoration[line.Id] = line
		}
	}
	// ассет → строка DECORATION, к которой он привязан на колорвее прогона.
	boundTo := map[int]int{}
	for _, b := range band.AssetBindings {
		if b.ColorwayId != cw {
			continue
		}
		if _, ok := decoration[b.BomItemId]; ok {
			boundTo[b.AssetId] = b.BomItemId
		}
	}
	if len(boundTo) == 0 {
		return nil
	}
	assets := map[int]entity.DesignAsset{}
	for _, a := range band.Assets {
		assets[a.Id] = a
	}
	placements := append([]entity.DesignAssetPlacement(nil), band.AssetPlacements...)
	sort.SliceStable(placements, func(i, j int) bool {
		vi, vj := designViewRank(flats[placements[i].PictureId].view), designViewRank(flats[placements[j].PictureId].view)
		if vi != vj {
			return vi < vj
		}
		return placements[i].Id < placements[j].Id
	})
	var out []designFrozenArtwork
	for _, pl := range placements {
		f, onFlat := flats[pl.PictureId]
		bom, bound := boundTo[pl.AssetId]
		if !onFlat || !bound {
			continue
		}
		a, ok := assets[pl.AssetId]
		if !ok || !a.MediaId.Valid || a.MediaId.Int32 <= 0 {
			continue
		}
		corners, ok := designArtworkQuad(pl.Annotation)
		if !ok {
			continue
		}
		line := decoration[bom]
		name := strings.TrimSpace(line.Name)
		if name == "" {
			name = strings.TrimSpace(a.Name)
		}
		// ТЕХНИКА: слова метки (клиент пишет туда только технику), иначе слова ассета, иначе spec
		// строки BOM (туда `+ artwork` кладёт технику).
		// Маркеры клиента (« · cut», «artwork = picture 1») — учёт, а не описание: в промпт не едут.
		note := entity.DesignArtworkTechniqueWords(pl.Note.String)
		if note == "" {
			note = entity.DesignArtworkTechniqueWords(a.Note.String)
		}
		if note == "" {
			note = entity.DesignArtworkTechniqueWords(line.Spec.String)
		}
		out = append(out, designFrozenArtwork{
			AssetID:     a.Id,
			BomItemID:   bom,
			Name:        name,
			MediaID:     int(a.MediaId.Int32),
			View:        f.view,
			PictureID:   pl.PictureId,
			FlatMediaID: f.media,
			Corners:     corners,
			Note:        note,
		})
	}
	// НЕ ОБРЕЗАЕТСЯ: пятый артворк, молча выкинутый здесь, — это принт, о котором человек думает,
	// что он на рендере. Лишнее отказывает дверь (designRefuseRenderArtworks) до денег.
	return out
}

// designViewRank — порядок сторон, как у плит (front, back, side_l, side_r, detail).
func designViewRank(v string) int {
	switch v {
	case "front":
		return 0
	case "back":
		return 1
	case "side_l":
		return 2
	case "side_r":
		return 3
	}
	return 4
}

// designSpliceArtworks кладёт `artworks` в закодированный снимок входов. Пустой список не пишется
// вовсе — снимок без артворков байт в байт тот же, что до этой волны.
func designSpliceArtworks(inputsJSON []byte, arts []designFrozenArtwork) ([]byte, error) {
	if len(arts) == 0 {
		return inputsJSON, nil
	}
	obj := map[string]json.RawMessage{}
	if len(inputsJSON) > 0 {
		if err := json.Unmarshal(inputsJSON, &obj); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(arts)
	if err != nil {
		return nil, err
	}
	obj["artworks"] = raw
	return json.Marshal(obj)
}

// designParentArtworks — артворки РОДИТЕЛЯ рерана, сырыми байтами: реран посылает то же самое, и
// разметка, замороженная родителем, едет как была (protojson-разбор снимка родителя её отбрасывает).
func designParentArtworks(parent *entity.DesignRun) []designFrozenArtwork {
	if parent == nil || len(parent.Inputs) == 0 {
		return nil
	}
	var in struct {
		Artworks []designFrozenArtwork `json:"artworks"`
	}
	if err := json.Unmarshal(parent.Inputs, &in); err != nil {
		return nil
	}
	return in.Artworks
}

// designRunArtworks — the artworks THIS run carries: a rerun the parent's frozen copy, a fresh
// render the freeze of today's markup. Called BEFORE the media doors and the reserve, so the
// artwork pictures are validated, counted against the engine ceiling and priced like every input.
func designRunArtworks(kind string, params *pb_common.DesignRunParams, card *entity.TechCard,
	band *entity.DesignBand, inputs *pb_common.DesignInputSnapshot, parent *entity.DesignRun) []designFrozenArtwork {
	if kind != entity.DesignRunKindRender {
		return nil
	}
	if parent != nil {
		return designParentArtworks(parent)
	}
	return designFreezeArtworks(kind, params, card, band, inputs)
}

// designArtworkMediaRefs — refs plus every artwork picture not already among them (dedup by media id,
// first source wins, the same rule as designRunInputMediaRefs and the worker's `add`).
func designArtworkMediaRefs(refs []designInputMediaRef, arts []designFrozenArtwork) []designInputMediaRef {
	if len(arts) == 0 {
		return refs
	}
	seen := make(map[int]struct{}, len(refs)+len(arts))
	for _, r := range refs {
		seen[r.ID] = struct{}{}
	}
	out := append([]designInputMediaRef(nil), refs...)
	for _, a := range arts {
		if a.MediaID <= 0 {
			continue
		}
		if _, dup := seen[a.MediaID]; dup {
			continue
		}
		seen[a.MediaID] = struct{}{}
		where := "the artwork placed on the " + a.View + " flat"
		if name := strings.TrimSpace(a.Name); name != "" {
			where = "the artwork «" + name + "» placed on the " + a.View + " flat"
		}
		out = append(out, designInputMediaRef{ID: a.MediaID, Where: where})
	}
	return out
}

// designRefuseRenderArtworks — the render's artworks, asked BEFORE the reserve:
//   - more than designMaxRunArtworks placed artworks is refused in words (never truncated: a dropped
//     print is one the person believes is on the render);
//   - with artworks, the whole call (plates, references, extras, maps, cloths AND artwork pictures)
//     must fit the engine's reference ceiling — the worker refuses an over-ceiling call only after
//     the money is reserved.
//
// A render with no artworks is untouched (its ceiling stays with the provider client, as before).
func (s *Server) designRefuseRenderArtworks(kind string, params *pb_common.DesignRunParams,
	inputs *pb_common.DesignInputSnapshot, arts []designFrozenArtwork) error {
	if kind != entity.DesignRunKindRender || len(arts) == 0 {
		return nil
	}
	if len(arts) > designMaxRunArtworks {
		return designRefusal(codes.InvalidArgument, designErrorCodeTooManyArtworks,
			fmt.Sprintf("this render carries %d placed artworks: at most %d placed artworks per render · "+
				"remove one on PARTS. Nothing was reserved and nothing was charged", len(arts), designMaxRunArtworks),
			map[string]string{
				"artworks": strconv.Itoa(len(arts)),
				"ceiling":  strconv.Itoa(designMaxRunArtworks),
			})
	}
	engine, ok := designgen.FindEngine(s.designEngineTable(), params.GetImage().GetModel())
	if !ok || engine.MaxRefs <= 0 {
		return nil
	}
	if n := designImageCallImagesWithArtworks(kind, params, inputs, arts, 0); n > engine.MaxRefs {
		return designRefusal(codes.InvalidArgument, "too_many_pictures",
			fmt.Sprintf("this render would send %d images in one call (its placed artworks included) and %s "+
				"takes at most %d. Remove an artwork on PARTS or a picture. Nothing was reserved and nothing "+
				"was charged", n, engine.Label, engine.MaxRefs),
			map[string]string{
				"images":   strconv.Itoa(n),
				"ceiling":  strconv.Itoa(engine.MaxRefs),
				"model":    engine.Slug,
				"artworks": strconv.Itoa(len(arts)),
			})
	}
	return nil
}
