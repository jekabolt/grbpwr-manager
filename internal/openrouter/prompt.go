package openrouter

import (
	"fmt"
	"strings"
)

// systemPrompt casts the model as a garment technologist and pins the output contract: a single
// strict-JSON object of sewing operations, no prose.
//
// EVERY DESCRIPTIVE FIELD IS A TOKEN FROM A LISTED DICTIONARY, and the lists are spelled out in the
// prompt for a concrete reason: the previous version asked for twelve free-text fields, so the model
// wrote «оверлок 4-нит.» on one card and «4-thread overlock» on the next, and both were persisted
// verbatim because nothing could check them. A token either resolves or becomes UNKNOWN for the
// technologist to fix — which is the difference between a draft that can be validated and one that
// can only be believed.
//
// The dictionaries here MUST stay in step with entity.GarmentZoneTokens / SeamClassTokens /
// AttachmentKindTokens / TopstitchModeTokens; a token missing from this text is one the model will
// never emit, and its absence is invisible in the output.
const systemPrompt = `You are an expert garment technologist and sewing production engineer.
Given a tech card's context (garment type, cut-pieces, bill of materials) and a plain-language
description of how the garment is assembled, you produce a clean, ordered list of factory sewing
operations (the assembly order).

Respond with ONE JSON object and NOTHING else — no markdown, no commentary. Shape:

{
  "operations": [
    {
      "operation_number": 10,          // human step number in tens (10, 20, 30, …)
      "operation_type": "overlock",    // REQUIRED, token from the list below — what the step DOES
      "zone": "shoulder",              // REQUIRED, token from the list below — WHERE on the garment
      "seam_class": "ss_plain",        // ISO 4916 class, token from the list below (omit to inherit)
      "stitches_per_cm": "4",          // density in stitches per CENTIMETRE (omit to inherit)
      "seam_allowance_mm": "10",       // seam allowance in MILLIMETRES (omit to inherit the card standard)
      "topstitch_mode": "width",       // "edge" or "width"; omit when the step has no topstitching
      "topstitch_width_mm": "6",       // ONLY with topstitch_mode "width"
      "topstitch_rows": 2,             // 1..4; omit when unknown
      "attachment_kind": "binder",     // token from the list below; omit when no attachment is used
      "smv_minutes": "0.8",            // standard minute value for the step, numeric
      "callout_number": 0,             // sketch callout number if relevant, else 0
      "note": "…"                      // optional remark, free text — the ONLY free-text field
    }
  ],
  "notes": "any global assumptions you made"
}

Rules:
- Order operations in the real sewing sequence and number them 10, 20, 30, ….
- operation_type and zone are REQUIRED on every operation. Everything else may be omitted.
- There is no step title: the heading is composed from the type, the zone and the pieces. Do not
  invent one, and do not put one in "note".
- Use only these operation_type tokens: lockstitch, double_needle, overlock, coverstitch,
  chainstitch, blindhem, bartack, buttonhole, button_attach, fusing, handwork, other.
- Use only these zone tokens: outer, lining, interlining, sleeve, collar, neckline, armhole,
  shoulder, chest, waist, hip, hem, pocket, closure, back, front, other.
- Use only these seam_class tokens (ISO 4916): ss_plain, ss_french, ls_lapped, ls_flat_felled,
  ef_hem_raw, ef_hem_turned, ef_faced, bs_bound, fs_flat, os_topstitch, other.
- Use only these attachment_kind tokens: binder, hemmer_folder, scroll_foot, zipper_foot,
  invisible_zipper_foot, edge_guide, piping_foot, elastic_attachment, other.
- ALL lengths are MILLIMETRES. Stitch density is the one exception and is per centimetre.
- Omit seam_class, stitches_per_cm and seam_allowance_mm when the step simply follows the card's
  default — an omitted field INHERITS, and repeating the default hides which steps genuinely differ.
- Prefer materials and pieces from the provided context; do not invent parts that contradict it.
- Leave a field out rather than guessing when you genuinely do not know.
- Output must be valid JSON parseable as-is.`

// buildUserPrompt renders the tech-card grounding context plus the technologist's
// free-text brief into the user message.
func buildUserPrompt(tcx TechCardContext, description string) string {
	var b strings.Builder

	b.WriteString("TECH CARD CONTEXT\n")
	writeKV(&b, "Style name", tcx.StyleName)
	writeKV(&b, "Style number", tcx.StyleNumber)
	writeKV(&b, "Garment type / category", tcx.Category)
	writeKV(&b, "Target gender", tcx.Gender)
	writeKV(&b, "Brand", tcx.Brand)
	writeKV(&b, "Design concept", tcx.Concept)
	writeKV(&b, "Card notes", tcx.Notes)

	// The card's defaults, stated so the model can OMIT the fields that match them. An empty default
	// contributes nothing: naming one nobody configured would invent a fact to reason from.
	if c := tcx.Construction; c != nil {
		var parts []string
		if v := strings.TrimSpace(c.DefaultSeamClass); v != "" {
			parts = append(parts, "seam class: "+v)
		}
		if v := strings.TrimSpace(c.DefaultStitchesPerCm); v != "" {
			parts = append(parts, "density: "+v+" stitches/cm")
		}
		if c.OverlockThreadCount > 0 {
			parts = append(parts, fmt.Sprintf("overlock threads: %d", c.OverlockThreadCount))
		}
		if len(parts) > 0 {
			writeKV(&b, "Card defaults (omit a field to inherit it)", strings.Join(parts, "; "))
		}
	}
	if v := strings.TrimSpace(tcx.RequiredSeamAllowanceMm); v != "" {
		writeKV(&b, "Required seam allowance (mm)", v)
	}

	if len(tcx.Pieces) > 0 {
		b.WriteString("\nCUT PIECES (детали кроя):\n")
		for _, p := range tcx.Pieces {
			name := strings.TrimSpace(p.Name)
			if name == "" {
				continue
			}
			b.WriteString("- ")
			b.WriteString(name)
			var attrs []string
			if p.PiecesPerGarment > 0 {
				attrs = append(attrs, fmt.Sprintf("x%d per garment", p.PiecesPerGarment))
			}
			// КАК КРОИТСЯ (0275), spelled out in English for the model. An unmarked piece contributes
			// NOTHING here — silence is the honest rendering of «не размечено», and a default of
			// "identical copies" would be a fact this file made up.
			switch strings.TrimSpace(p.CutSymmetry) {
			case "mirrored":
				attrs = append(attrs, "mirrored pair (left + right)")
			case "fold":
				attrs = append(attrs, "cut on the fold")
			case "identical":
				attrs = append(attrs, "identical copies")
			}
			if v := strings.TrimSpace(p.Grainline); v != "" {
				attrs = append(attrs, "grainline "+v)
			}
			if p.Fused {
				attrs = append(attrs, "fused/interlined")
			}
			if v := strings.TrimSpace(p.Note); v != "" {
				attrs = append(attrs, "note: "+v)
			}
			if len(attrs) > 0 {
				b.WriteString(" (" + strings.Join(attrs, ", ") + ")")
			}
			b.WriteString("\n")
		}
	}

	if len(tcx.BOM) > 0 {
		b.WriteString("\nBILL OF MATERIALS (BOM):\n")
		for _, m := range tcx.BOM {
			name := strings.TrimSpace(m.Name)
			if name == "" {
				continue
			}
			b.WriteString("- ")
			if s := strings.TrimSpace(m.Section); s != "" {
				b.WriteString("[" + s + "] ")
			}
			b.WriteString(name)
			var attrs []string
			if v := strings.TrimSpace(m.Composition); v != "" {
				attrs = append(attrs, "composition "+v)
			}
			if v := strings.TrimSpace(m.Color); v != "" {
				attrs = append(attrs, "colour "+v)
			}
			if v := strings.TrimSpace(m.Spec); v != "" {
				attrs = append(attrs, "spec "+v)
			}
			if v := strings.TrimSpace(m.Supplier); v != "" {
				attrs = append(attrs, "supplier "+v)
			}
			if len(attrs) > 0 {
				b.WriteString(" (" + strings.Join(attrs, ", ") + ")")
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\nDESCRIPTION OF THE OPERATIONS TO GENERATE:\n")
	b.WriteString(strings.TrimSpace(description))
	b.WriteString("\n\nReturn the operations as the specified JSON object.")

	return b.String()
}

// writeKV appends "Label: value" when value is non-empty (after trimming).
func writeKV(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteString("\n")
}
