package openrouter

import (
	"fmt"
	"strings"
)

// systemPrompt casts the model as a garment technologist and pins the output
// contract: a single strict-JSON object of sewing operations, no prose. The allowed
// operation_type / zone tokens mirror the TechCardOperationType / ConstructionZone
// enums so the handler can map them straight onto the persisted operation.
const systemPrompt = `You are an expert garment technologist and sewing production engineer.
Given a tech card's context (garment type, cut-pieces, bill of materials) and a plain-language
description of how the garment is assembled, you produce a clean, ordered list of factory sewing
operations (the "Обработка" / construction sheet).

Respond with ONE JSON object and NOTHING else — no markdown, no commentary. Shape:

{
  "operations": [
    {
      "operation_number": 10,                 // human step number in tens (10, 20, 30, …)
      "node": "join shoulder seams",          // REQUIRED short узел / operation name
      "description": "…",                     // what the operator does, one or two sentences
      "seam_type": "…",                        // seam / stitch type (e.g. "4-thread overlock", "French seam")
      "operation_type": "overlock",           // one of the tokens listed below (or omit)
      "machine": "…",                          // machine / equipment (e.g. "504 overlock")
      "stitches_per_cm": "4",                  // density, numeric (string or number)
      "topstitch_width": "…",                  // topstitch width if any (e.g. "6 mm")
      "seam_allowance": "…",                   // seam allowance for this node (e.g. "1 cm")
      "thread": "…",                           // thread reference / type
      "needle": "…",                           // needle size / type
      "attachment": "…",                       // folder / binder / guide / presser-foot if used
      "time_norm_minutes": "0.8",              // SAM / time norm in minutes, numeric
      "zone": "outer",                         // construction band token (or omit)
      "callout_number": 0,                     // sketch callout number if relevant, else 0
      "placement": "…",                        // garment part this operation works on
      "note": "…"                              // optional remark
    }
  ],
  "notes": "any global assumptions you made"
}

Rules:
- Order operations in the real sewing sequence and number them 10, 20, 30, ….
- "node" is REQUIRED and non-empty for every operation.
- Use only these operation_type tokens: lockstitch, double_needle, overlock, coverstitch,
  chainstitch, blindhem, bartack, buttonhole, button_attach, fusing, handwork, other.
- Use only these zone tokens: outer, lining, interlining, other.
- Prefer materials and pieces from the provided context; do not invent parts that contradict it.
- Leave a field out (or empty) rather than guessing when you genuinely do not know.
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

	if c := tcx.Construction; c != nil {
		var parts []string
		if v := strings.TrimSpace(c.MainStitchType); v != "" {
			parts = append(parts, "main stitch: "+v)
		}
		if v := strings.TrimSpace(c.StitchDensity); v != "" {
			parts = append(parts, "density: "+v)
		}
		if v := strings.TrimSpace(c.OverlockThreads); v != "" {
			parts = append(parts, "overlock threads: "+v)
		}
		if v := strings.TrimSpace(c.SeamAllowances); v != "" {
			parts = append(parts, "seam allowances: "+v)
		}
		if len(parts) > 0 {
			writeKV(&b, "Default construction", strings.Join(parts, "; "))
		}
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
			if p.Mirrored {
				attrs = append(attrs, "mirrored pair")
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
