package openrouter

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
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
// THE LISTS ARE RENDERED FROM THE entity SLICES, not typed out here, and the previous version of
// this file is the argument: it listed eight attachment kinds and the vocabulary had grown to
// twelve, so `teflon_foot` was a token the model could never emit and nothing anywhere said so. A
// dictionary written down twice drifts, and this half drifts silently — a token missing from this
// text simply never appears in an answer.
var systemPrompt = buildSystemPrompt()

const systemPromptTemplate = `You are an expert garment technologist and sewing production engineer.
Given a tech card's context (garment type, cut-pieces, bill of materials) and a plain-language
description of how the garment is assembled, you produce a clean, ordered list of factory sewing
operations (the assembly order).

Respond with ONE JSON object and NOTHING else — no markdown, no commentary. Shape:

{
  "operations": [
    {
      "operation_number": 10,          // human step number in tens (10, 20, 30, …)
      "operation_type": "machine",     // REQUIRED, token from the list below — what the step DOES
      "zone": "shoulder",              // REQUIRED, token from the list below — WHERE on the garment
      "seam_class": "ss_plain",        // ISO 4916 class, token from the list below (omit to inherit)
      "stitches_per_cm": "4",          // density in stitches per CENTIMETRE (omit to inherit)
      "seam_allowance_mm": "10",       // seam allowance in MILLIMETRES (omit to inherit the card standard)
      "topstitch_mode": "width",       // {{TOPSTITCH_MODES}}; omit when the step has no topstitching
      "topstitch_width_mm": "6",       // ONLY with topstitch_mode "width"
      "topstitch_rows": 2,             // 1..{{MAX_TOPSTITCH_ROWS}}; omit when unknown
      "attachment_kind": "binder",     // token below; "none" = SEWN BARE. Omit only to inherit the machine profile
      "smv_minutes": "0.8",            // standard minute value for the step, numeric
      "callout_number": 0,             // sketch callout number if relevant, else 0
      "note": "…",                     // optional remark, free text — the ONLY free-text field

      // MACHINE STEPS ONLY (operation_type "machine"). Omit every one of these on any other type.
      "machine_type": "overlock",      // REQUIRED on a machine step — WHAT THE STEP IS SEWN ON
      "thread_count": 4,               // threads on that machine, {{THREAD_COUNT_RANGE}}
      "needle_type": "ballpoint",      // needle POINT, token from the list below
      "needle_size_nm": 90,            // needle size in Nm ({{NEEDLE_SIZE_RANGE}}); Nm 90 = 0.90 mm blade
      "thread_tension": "normal",      // token from the list below, RELATIVE to that machine's usual
      "thread_tension_note": "на 0.5 туже", // ≤{{MAX_TENSION_NOTE}} chars qualifying that scale; ONLY together with thread_tension
      "stitch_width_mm": "5",          // zigzag amplitude / overlock bite in mm, {{STITCH_WIDTH_RANGE}}

      // PRESSING STEPS ONLY / ВТО (operation_type "press", "press_open" or "fusing"). Omit elsewhere.
      "press_equipment": "iron",       // REQUIRED on a pressing step — WHAT IT IS PRESSED WITH
      "press_temperature_c": 150,      // °C, {{PRESS_TEMPERATURE_RANGE}}
      "press_dwell_sec": 12,           // seconds under the press, {{PRESS_DWELL_RANGE}}
      "press_pressure_n_cm2": "3.5",   // pressure ON THE CLOTH in N/cm² (NOT bar), {{PRESS_PRESSURE_RANGE}}
      "press_steam": true,             // true or false; omit when not stated
      "press_cloth": "press_cloth"     // token from the list below; "none" = deliberately bare
    }
  ],
  "notes": "any global assumptions you made"
}

Rules:
- Order operations in the real sewing sequence and number them 10, 20, 30, ….
- operation_type and zone are REQUIRED on every operation. Everything else may be omitted.
- There is no step title: the heading is composed from the type, the zone and the pieces. Do not
  invent one, and do not put one in "note".
- A STEP ANSWERS TWO QUESTIONS, NOT ONE: operation_type says what is done, machine_type says what it
  is done on. Use only these operation_type tokens: {{OPERATION_TYPES}}.
  Do NOT use a stitch class (lockstitch, double_needle, overlock, coverstitch, chainstitch, blindhem,
  bartack, buttonhole, button_attach) as an operation_type — that names a MACHINE, not a verb: write
  operation_type "machine" and put the machine in machine_type.
- "press" is pressing in general (приутюжить / заутюжить / отпарить / finishing), "press_open" is
  specifically pressing a seam open (разутюжка), "fusing" is fusing/дублирование with an interlining.
- machine_type is REQUIRED when operation_type is "machine"; press_equipment is REQUIRED when
  operation_type is "press", "press_open" or "fusing". A step saved without them is refused.
- Use only these machine_type tokens: {{MACHINE_TYPES}}.
- Use only these press_equipment tokens: {{PRESS_EQUIPMENT}}.
- Use only these needle_type tokens: {{NEEDLE_TYPES}}.
- Use only these thread_tension tokens: {{THREAD_TENSIONS}}.
- Use only these press_cloth tokens: {{PRESS_CLOTHS}}.
- Use only these zone tokens: {{ZONES}}.
- Use only these seam_class tokens (ISO 4916): {{SEAM_CLASSES}}.
- Use only these attachment_kind tokens: {{ATTACHMENT_KINDS}}.
- AN OMITTED FIELD INHERITS; IT DOES NOT MEAN "NO". attachment_kind and press_cloth each carry a
  "none" token for that, and the two answers are not the same one: omitting attachment_kind on a
  step whose machine profile lists a binder puts the BINDER on that step, while "none" says the step
  is sewn bare. Write "none" whenever the step deliberately runs without the attachment or without a
  press cloth, and omit the field only when the profile's answer is the right one.
- thread_tension_note qualifies the scale and never replaces it: send it only together with
  thread_tension, and above all with thread_tension "other", which says nothing on its own. A note
  without a scale is refused by the save.
- ALL lengths are MILLIMETRES. Stitch density is the one exception and is per centimetre. Pressure is
  in N/cm² on the cloth — never bar, never a dial number.
- RANGES THE SAVE ENFORCES, so a draft outside them cannot be accepted as written: stitches_per_cm
  {{STITCHES_PER_CM_RANGE}} (3-5 is ordinary sewing; never 0 — omit it instead), seam_allowance_mm and
  topstitch_width_mm {{SEAM_ALLOWANCE_RANGE}}, thread_count {{THREAD_COUNT_RANGE}}, needle_size_nm {{NEEDLE_SIZE_RANGE}},
  stitch_width_mm {{STITCH_WIDTH_RANGE}}, press_temperature_c {{PRESS_TEMPERATURE_RANGE}},
  press_dwell_sec {{PRESS_DWELL_RANGE}}, press_pressure_n_cm2 {{PRESS_PRESSURE_RANGE}}.
- Omit seam_class, stitches_per_cm and seam_allowance_mm when the step simply follows the card's
  default — an omitted field INHERITS, and repeating the default hides which steps genuinely differ.
- THE EQUIPMENT SETTINGS FOLLOW THAT RULE ONLY WHERE THERE IS SOMETHING TO INHERIT FROM, and the
  context says exactly where. A profile line with no marker is the card's ONLY profile of that
  machine or that pressing equipment: naming the type is enough, the step is attached to it for you,
  and every setting that matches it must be OMITTED. A line marked SEVERAL means the card holds more
  than one profile of that equipment — nothing can be attached, nothing is inherited, and a step on
  it has to STATE every setting it needs. Equipment named in no line at all inherits nothing either.
- A PRESSING LINE ALSO BELONGS TO A PROCESS. A line that reads «for fusing», «for press» or «for
  press_open» is inherited by a step of THAT process alone — an ironing setup is not a fusing recipe,
  however well the equipment matches — while a line that names no process serves every ВТО step. A
  press / press_open / fusing step whose equipment has no line for its own process inherits nothing
  and must state press_temperature_c and press_dwell_sec itself.
- Fill thread_count, needle_type, needle_size_nm, thread_tension, stitch_width_mm,
  press_temperature_c, press_dwell_sec, press_pressure_n_cm2, press_steam or press_cloth where the
  step genuinely deviates from a profile it inherits, and wherever there is no profile behind it at
  all — there, state as much as you can.
- You do not create equipment profiles and you never name or invent an identifier for one: answer
  with machine_type / press_equipment, and the linking is done for you or by the technologist.
- Prefer materials and pieces from the provided context; do not invent parts that contradict it.
- Leave a field out rather than guessing when you genuinely do not know.
- Output must be valid JSON parseable as-is.`

// buildSystemPrompt fills the dictionary and range slots of the template from the domain
// vocabularies. A placeholder syntax rather than fmt verbs on purpose: the prompt text is full of
// percent-free prose today and a single «%» added to it later would silently corrupt every list.
func buildSystemPrompt() string {
	return strings.NewReplacer(
		// The six verbs. "unknown" is a storage placeholder, not something to answer with.
		"{{OPERATION_TYPES}}", promptDict(entity.OperationTypeTokens, "unknown"),
		"{{MACHINE_TYPES}}", promptDict(entity.MachineTypeTokens),
		"{{PRESS_EQUIPMENT}}", promptDict(entity.PressEquipmentTokens),
		"{{NEEDLE_TYPES}}", promptDict(entity.NeedleTypeTokens),
		"{{THREAD_TENSIONS}}", promptDict(entity.ThreadTensionTokens),
		"{{PRESS_CLOTHS}}", promptDict(entity.PressClothTokens),
		"{{ZONES}}", promptDict(entity.GarmentZoneTokens, "unknown"),
		"{{SEAM_CLASSES}}", promptDict(entity.SeamClassTokens),
		"{{ATTACHMENT_KINDS}}", promptDict(entity.AttachmentKindTokens),
		"{{TOPSTITCH_MODES}}", promptQuotedDict(entity.TopstitchModeTokens),
		"{{STITCHES_PER_CM_RANGE}}", promptRange(entity.MinStitchesPerCm, entity.MaxStitchesPerCm),
		"{{SEAM_ALLOWANCE_RANGE}}", promptRange(entity.MinSeamAllowanceMm, entity.MaxSeamAllowanceMm),
		"{{MAX_TOPSTITCH_ROWS}}", fmt.Sprint(entity.MaxTopstitchRows),
		"{{MAX_TENSION_NOTE}}", fmt.Sprint(entity.MaxThreadTensionNoteLen),
		"{{THREAD_COUNT_RANGE}}", promptRange(entity.MinThreadCount, entity.MaxThreadCount),
		"{{NEEDLE_SIZE_RANGE}}", promptRange(entity.MinNeedleSizeNm, entity.MaxNeedleSizeNm),
		"{{STITCH_WIDTH_RANGE}}", promptRange(entity.MinStitchWidthMm, entity.MaxStitchWidthMm),
		"{{PRESS_TEMPERATURE_RANGE}}", promptRange(entity.MinPressTemperatureC, entity.MaxPressTemperatureC),
		"{{PRESS_DWELL_RANGE}}", promptRange(entity.MinPressDwellSec, entity.MaxPressDwellSec),
		"{{PRESS_PRESSURE_RANGE}}", promptRange(entity.MinPressPressureNCm2, entity.MaxPressPressureNCm2),
	).Replace(systemPromptTemplate)
}

// promptDict renders a vocabulary in its reading order, dropping the tokens the model must not
// answer with (the "unknown" placeholder, which is legal in a row and refused on a write).
func promptDict(tokens []string, drop ...string) string {
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if !slices.Contains(drop, t) {
			out = append(out, t)
		}
	}
	return strings.Join(out, ", ")
}

// promptQuotedDict is promptDict for a slot rendered inline in the JSON sample, where the tokens
// read as JSON values rather than as a list.
func promptQuotedDict(tokens []string) string {
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, `"`+t+`"`)
	}
	return strings.Join(out, " or ")
}

func promptRange(min, max int) string { return fmt.Sprintf("%d..%d", min, max) }

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
		if len(parts) > 0 {
			writeKV(&b, "Card defaults (omit a field to inherit it)", strings.Join(parts, "; "))
		}
		// The equipment park. It is grounding of the strongest kind available here — «this style is
		// sewn on THESE machines» — and it is what makes the omission rule usable on the settings: a
		// step that runs on the listed overlock at the listed density says so by naming the machine
		// and staying silent about the rest. A card with no park contributes nothing, exactly like an
		// unset default.
		//
		// THE HEADINGS PROMISE ONLY WHAT THE MAPPER DELIVERS. A step is attached to a profile by the
		// CALLER, after the answer comes back, and it can only do that where the machine or the
		// pressing equipment names ONE profile — the card may legitimately hold two identical overlocks, and
		// the model has no way to say which one it meant: it never sees a profile key and could not
		// answer with one. So a line for an equipment with several profiles is marked SEVERAL, and
		// the model is told to state its settings rather than omit them into a link that will not be
		// made. Promising inheritance there is how an omitted setting became no setting at all.
		writeBullets(&b, "CARD MACHINES (name machine_type; an unmarked line is the card's only profile of that machine and a step naming it INHERITS it — omit every setting that matches. A line marked SEVERAL inherits NOTHING: state the settings)", c.MachineProfiles)
		writeBullets(&b, "CARD PRESSING EQUIPMENT / ВТО (name press_equipment; same rule PLUS the process — an unmarked line is inherited by a press / press_open / fusing step naming that equipment FOR THE PROCESS THE LINE STATES, a line that states no process by any of them, and a line marked SEVERAL by none)", c.PressProfiles)
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

// writeBullets appends a titled bullet list, or nothing at all when every line is blank — the same
// silence-over-invention rule the rest of this file follows.
func writeBullets(b *strings.Builder, title string, lines []string) {
	var kept []string
	for _, l := range lines {
		if v := strings.TrimSpace(l); v != "" {
			kept = append(kept, v)
		}
	}
	if len(kept) == 0 {
		return
	}
	b.WriteString("\n" + title + ":\n")
	for _, l := range kept {
		b.WriteString("- " + l + "\n")
	}
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
