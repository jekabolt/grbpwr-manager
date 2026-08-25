# GRBPWR tech-card archive — format v1.0

The single source of truth for the tech-card ZIP: what an export writes and what an import is
allowed to assume. Both sides read THIS file; `format.go` is its Go transcription and
`reasons.go` is the closed dictionary of hole codes referenced from here.

An archive is a **portable copy of one style's tech card**, not a backup: it carries the card's
structure verbatim and re-resolves every foreign identity (sizes, category, media, materials,
markers) against the target database on import. It carries **no money and no signature**.

---

## 1. Tree

```
techcard-<style_number>-<yyyymmdd-hhmm>.zip
├── manifest.json      # паспорт: format, format_version, exported_at/by, source{host, tech_card_id,
│                      #   style_number, lock_version, approval_state_at_export, app_version},
│                      #   money_policy, id_maps{sizes: {"3":"s",...}, category_path: [...],
│                      #   colorways: {"812":"BLK",...}}, contents{media,patterns,markers,materials},
│                      #   export_holes[{entity, ref, reason, detail}]
├── card.json          # protojson common.TechCard: деньги вырезаны, подписи/релизы/URL вычищены
├── sizechart.json     # мерки + грейд-правило, размеры и мерки ИМЕНАМИ (§5.1)
├── assembly.json      # [{component_style_number, size_name?, qty, print_note, position_note, active}]
├── colorways.json     # [{color_code, base_sku, recipe[], piece_materials[]}] — без денег
├── materials/index.json  # паспорта материалов (code, name, supplier, supplier_ref, composition,
│                         #   spec, unit, class, CTI-атрибуты, cutting_coefficient) — БЕЗ цен
├── media/index.json      # [{ref: media_id из card.json, file, sha256, kind, caption, w, h}]
├── media/<sha256>.<ext>
├── patterns/index.json   # [{line_key, file, sha256, size_name?, version, name, filename,
│                         #   fabric_purpose?, bom_line_key?}]
├── patterns/<sha256>.<dxf|pdf>
├── markers/index.json    # [{file, size_name, marker_name, bom_line_key?}]
└── markers/<size_name>-<n>.json  # protojson common.TechCardMarker c layout
```

`manifest.json` and `card.json` are **mandatory**; every other entry is optional (a card with no
media produces no `media/` directory at all, and an empty index is equally legal). An entry whose
name matches none of the patterns above is **not an error**: the reader lists it and the import
report says `unknown_entry` — that is the MINOR-compatibility rule of §3 in action. The one
exception is an entry an INDEX names and the import actually reads (a media or pattern file whose
extension is outside §1.1): it is known by its index row, so it produces no `unknown_entry` line —
one file may not be both planned and unrecognised.

### 1.1 Names of the entries

* **`file` fields in every index are ZIP entry names relative to the ARCHIVE ROOT** —
  `media/9f86d0…1e5c.jpg`, not `9f86d0…1e5c.jpg`. One rule for all three indexes, and the value is
  the key the reader hands to the ZIP directory without a join step. (The tree above abbreviates
  the value to `<sha256>.<ext>` for width; the root-relative form is the one that ships.)
* **Binary files are named by the sha256 of their content**, lower-case hex, plus the extension
  the source object key carried (`.jpg`, `.jpeg`, `.png`, `.webp`, `.gif`, `.mp4`, `.webm`,
  `.dxf`, `.pdf`). Integrity, dedup inside the archive (one photo used by two slots is one file)
  and dedup on import (`media.content_hash`) all follow from that one convention.
* **Marker files are `markers/<slug>-<n>.json`**, where `<slug>` is the marker's size name
  lower-cased with anything outside `[a-z0-9]` replaced by `-`, or the literal `mixed` for a
  marker that has no single size (a смешанный настил, `size_id = 0`), and `<n>` is a 1-based
  counter making the name unique inside the archive. The name is display sugar: the authority is
  `markers/index.json`, and a reader must locate a marker through the index, never by parsing the
  file name.
* Directory entries are not written. No entry name may contain `..`, a leading `/`, or a
  backslash.

### 1.2 Digests

`media/index.json` and `patterns/index.json` carry `sha256` per entry and a reader verifies it
while streaming the file — a mismatch is **corruption and fails the whole import**, never a hole.
Marker files carry no digest (they are JSON, validated by parsing) and neither do the four
top-level JSON files.

### 1.3 Limits (enforced by the reader, stated here because they are part of the contract)

| what | ceiling |
| --- | --- |
| entries in the ZIP | 4096 |
| total uncompressed bytes | 1 GiB |
| `card.json` | 16 MB |
| `manifest.json` and any sidecar or index JSON | 16 MB (the same row as `card.json`: each is read whole into memory, and one number is safer than a new one per file) |
| one marker file | 3 MiB (the save path caps the layout blob at 2 MiB; the file is summary + layout) |
| uploaded archive body (import route) | 256 MiB |

---

## 2. Manifest

```json
{
  "format": "grbpwr-techcard-archive",
  "format_version": "1.0",
  "exported_at": "2026-08-25T14:00:00Z",
  "exported_by": "im",
  "source": {
    "host": "backend.grbpwr.com",
    "tech_card_id": 214,
    "style_number": "GRB-SS26-014",
    "lock_version": 37,
    "approval_state_at_export": "released",
    "app_version": "abc1234"
  },
  "money_policy": "stripped-v1",
  "id_maps": {
    "sizes": {"3": "s", "4": "m", "5": "l"},
    "category_path": ["clothing", "outerwear", "jacket"],
    "colorways": {"812": "BLK", "813": "OLV"}
  },
  "contents": {
    "media": 14,
    "patterns": 6,
    "markers": 3,
    "materials": 11
  },
  "export_holes": [
    {
      "entity": "media",
      "ref": "media_id=4021",
      "reason": "media_object_missing",
      "detail": "full_size object 2026/08/1a2b.jpg: 404 from bucket"
    },
    {
      "entity": "material",
      "ref": "bom_line_key=01J8ZC4Q0FQ8M6R0K2",
      "reason": "material_not_found",
      "detail": "material_id=8121 deleted from catalogue; the BOM line keeps its own name/supplier/unit"
    },
    {
      "entity": "assembly",
      "ref": "component_tech_card_id=902",
      "reason": "assembly_component_not_found",
      "detail": "component card deleted; the line is not exported"
    }
  ]
}
```

* `source.*` is **provenance, never instruction**. An importer may show it and must not resolve
  anything through it: `tech_card_id` and `lock_version` belong to the exporting instance.
* `id_maps` are the only dictionaries that survive the trip. `sizes` maps **every size id that
  appears anywhere in the archive** — `card.json`, every sidecar and every marker blob — to size
  names (`size.name` is UNIQUE in every instance). A SUPERSET is legal and preferred (the source's
  whole size dictionary is the simplest thing to ship); a subset is not, and this is not pedantry:
  §5.7 remaps every `size_id` inside a marker blob through this same table, and a смешанный настил
  names sizes that need never appear in `card.json` at all — a miss there is a `size_unknown` hole
  that drops the WHOLE marker. `category_path` is the category triple by name; `colorways` maps
  colourway ids (= `product.id` on the source) to their `color_code` — reference only, because a
  colourway is a product and does not travel (§5.3).
* `contents` counts what the archive claims to contain. It is a **positive control**: an import
  whose parse produced zero media while `contents.media > 0` has a broken parser, not a clean
  card, and must fail rather than report success.
* `export_holes` is what the EXPORT could not put in the archive. The `reason` is a code from
  `reasons.go`; `detail` is free human text and carries no contract. A hole is not an error: the
  export completes, the import re-reports the hole, and the operator sees the same words twice on
  purpose.
* `money_policy` is mandatory and must read `stripped-v1`. See §4.

---

## 3. Version and compatibility

`format_version` is `"MAJOR.MINOR"`; v1 is `"1.0"`.

* **MAJOR mismatch = refusal of the whole import**, with words saying the archive is newer or
  older than this server. A MAJOR bump is what renaming a field, changing its meaning, or moving a
  file costs.
* **MINOR is additive and readable by any server of the same MAJOR.** `card.json` is parsed with
  protojson `DiscardUnknown: true` (the same mode release snapshots are read with), unknown files
  are ignored and listed in the report as `unknown_entry`, and unknown JSON fields in the sidecars
  are ignored.
* Backwards rule: a server must read every MINOR of its own MAJOR. Forwards rule: new fields only,
  never a rename and never a re-meaning.
* The marker blob's own `layout.schema_version` lives its own life inside its own file — the
  archive does not duplicate it (a derived value stored twice is a value that will disagree with
  itself).

---

## 4. What the format deliberately does NOT carry

* **Money.** Costing, BOM prices, colourway unit costs, `latest_price`, `unit_price`, `currency`,
  `cost_price`, `line_total`, `size_run_total`, price provenance — all of it is cut, twice: by
  name and by a recursive protoreflect pass, unconditionally, without looking at the caller's
  rights. Stripping is a property of the FORMAT, not of who asked. `money_policy: "stripped-v1"`
  is the flag that sits **next to the check**: an import refuses any archive that does not carry
  it, so a hand-made or pre-versioned bundle with money in it cannot slip in quietly. Free text
  (notes, comments) is a residual channel — named here, not closed.
* **Cryptographic signature fields.** None, not even reserved (owner decision B-4). The archive is
  not evidence of anything; a card arriving from one carries no authority.
* **The exporting instance's URLs, tokens and object keys.** Pattern `url` / `view_url` /
  `download_url`, resolved media URLs and the like are blanked — the bytes travel in the archive,
  the links do not.
* **Role assignments.** `role_assignments` name accounts (`admin_id`, `username`) in the SOURCE
  instance's admins table — an assignment is a row in a table the target does not share, unlike the
  `created_by` / `updated_by` names below, which resolve nothing.
* **The fit model.** `base_model_id` is a row in the source's `model` table — the same class as
  `role_assignments`, and no model dictionary travels beside it. It is blanked on export, so an
  archive never carries a number that is accidentally somebody's on the receiving side (§6.2). An
  import clears it anyway, which is the defence against a hand-made archive.
* **Section digests.** `section_digests` are recomputed on every read, so storing them would be
  storing a derived value; and the costing section's was fingerprinted from content whose money
  this format then cut, which makes it a trace of exactly what §4 removes. The importing instance
  computes its own.
* **Approval.** The importing side forces the card to `DRAFT`, clears `released_at` /
  `approved_at` and drops every signoff before writing anything, so an imported card can never
  *look* signed. See §6.

What **does** travel as it stands (owner decisions B-2 / B-3, no partner profile exists):
`created_by` / `updated_by`, the revision journal, and the `parsed_by` stamps of measurements.

---

## 5. Sidecars

Decimals are plain JSON strings (`"1.03"`), never floats; `""` / absent means unset. Ids of the
exporting instance appear only where a `ref` field says so, and a `ref` is a lookup key into
another file of the same archive — never something to write into the target database.

### 5.1 `sizechart.json`

The style's measurement grid and the grade rule it was authored from, with **both axes by name**:
size names (`size.name`, UNIQUE) and measurement names (`measurement_name.name`, UNIQUE).

```json
{
  "cells": [
    {"size_name": "s", "measurement": "chest", "value": "50"},
    {"size_name": "m", "measurement": "chest", "value": "52"}
  ],
  "grade_base_size_name": "m",
  "grade_steps": [
    {"measurement": "chest", "step": "2"}
  ]
}
```

It mirrors `common.StyleSizeChart` field for field, with two deliberate differences: ids are
replaced by names, and `style_id` / `lock_version` do not travel (they are the source instance's).
This is why the file is not raw protojson — protojson of that message cannot express a name in an
`int32` field.

`grade_base_size_name` empty and `grade_steps` empty mean the chart was typed cell by cell, which
is what every pre-existing chart is.

### 5.2 `assembly.json`

The style's assembly bill — auxiliary items (labels, tags, packaging) attached to the garment.

```json
[
  {
    "component_style_number": "GRB-AUX-0012",
    "size_name": null,
    "qty": "1",
    "print_note": "brand logo, artwork A-14",
    "position_note": "centre back neck",
    "active": true
  }
]
```

The component travels **by style number**, never by id. `size_name: null` = the line applies to
all sizes (`size_id = 0` on the source). A component card that no longer exists on the source is
an export hole (`assembly_component_not_found`) and the line is not written; a component that
does not exist in the TARGET is an import hole with the same code.

### 5.3 `colorways.json`

Reference payload: colourways are **products**, and an import does not create products. The file
travels so a later, explicit «create colourways from archive» action can build draft colourways and their recipes from it, and
so a human can read what the source card's colourways were.

```json
[
  {
    "color_code": "BLK",
    "base_sku": "GRB-SS26-014-BLK",
    "recipe": [
      {
        "bom_line_key": "01J8ZC4Q0FQ8M6R0K2",
        "piece_line_key": "",
        "placement": "outer",
        "color": "black",
        "pantone": "19-4005 TCX",
        "consumption": "1.42",
        "quantity": "",
        "size_consumptions": {"s": "1.38", "m": "1.42", "l": "1.47"},
        "material_ref": 8120,
        "consumption_source": "marker",
        "waste_selvedge_pct": "2.1",
        "waste_cut_pct": "12.4"
      }
    ],
    "piece_materials": [
      {
        "piece_line_key": "01J8ZC5R7NQ1PP0A31",
        "bom_line_key": "01J8ZC4Q0FQ8M6R0K2",
        "fusing_bom_line_key": "",
        "note": ""
      }
    ]
  }
]
```

* Rows address the card by the stable `line_key` family, which travels verbatim and is valid on
  the imported card without any remap.
* A row with a `piece_line_key` is a **material assignment** («деталь X кроится из артикула Y»),
  never a norm; the norm lives on the garment-level row with the piece unset.
* `material_ref` is the source `material_id` of the pin and resolves through
  `materials/index.json` → the target catalogue by passport match (§5.4). It is never written as
  an id.
* `size_consumptions` is keyed by SIZE NAME.
* No `line_total`, no `size_run_total`, no prices — not "cleaned afterwards" but never asked for.
* Lab dips do not travel.
* `norm_marker_id` does not travel: the stamp points at a marker of the source instance, and a
  norm whose marker cannot be re-sewn degrades honestly (`norm_marker_lost`) instead of pointing
  at a stranger.

### 5.4 `materials/index.json`

Passports of exactly the catalogue articles the card references — BOM lines' `material_id`, recipe
pins, and an auxiliary card's `output_material_id` (the warehouse bucket its run receipts into).
**Without prices**, and without price history.

```json
[
  {
    "ref": 8120,
    "code": "F-WOOL-320",
    "name": "wool melton 320 g",
    "supplier": "Lanificio",
    "supplier_ref": "LM-320-BLK",
    "composition": "80% wool, 20% pa",
    "composition_entries": [{"fiber_code": "WO", "percent": "80"}, {"fiber_code": "PA", "percent": "20"}],
    "spec": "150 cm / 320 gsm",
    "unit": "m",
    "unit_code": "MATERIAL_UNIT_M",
    "class": "MATERIAL_CLASS_FABRIC",
    "color": "black",
    "pantone": "",
    "cutting_coefficient": "1.03",
    "attributes": {
      "fabric": {
        "width_cm": "150",
        "weight_gsm": "320",
        "fabric_direction": "lengthwise",
        "shrinkage_pct": "3",
        "roll_length_m": "40",
        "selvedge_cm": "1.5"
      }
    }
  }
]
```

`ref` is the source `material_id` — the key `card.json` and `colorways.json` point at, and the
reason the passport exists at all. The import **matches, it does not create**: `code` among
non-archived articles first (the code is unique only among live rows and only in the application —
the schema does not enforce it), then `(supplier, supplier_ref)`, with the `unit` checked on a
code match. Ambiguous, unit-mismatched or unmatched articles leave `material_id` empty and produce
a hole — the BOM line itself imports regardless, because it carries its own
`name/supplier/supplier_ref/composition/spec/unit`. The output article resolves the same way and
misses with the same three codes, under the `ref` `output_material`; a miss leaves the card's
`output_material_id` unset, which the operator must fill before the first production run.

### 5.5 `media/index.json`

```json
[
  {
    "ref": 4020,
    "file": "media/9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08.jpg",
    "sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
    "kind": "TECH_CARD_MEDIA_KIND_FRONT",
    "caption": "front flat",
    "w": 2400,
    "h": 3200
  }
]
```

* One entry **per media id**, not per slot: the same photo used by a sketch slot and a callout is
  one entry. Two different media ids whose bytes are identical share one `file` — that is the
  content-addressed naming doing its job.
* `kind` and `caption` are display sugar taken from the first card slot that names the media
  (`TechCardMediaKind` enum name for the sketch lists, `""` for media reached through callouts,
  details or operations). The **usage** is in `card.json`, where the slots reference `ref`; the
  index says only "these bytes are this media".
* The bytes are the **full-size** variant as it lies in the bucket — the same bytes
  `media.content_hash` is computed over, which is what makes import-side dedup possible.
* A media row whose object could not be read is a hole (`media_object_missing`) and gets no entry;
  the slot stays in `card.json` and the import reports it a second time (`media_missing`).

### 5.6 `patterns/index.json`

```json
[
  {
    "line_key": "01J8ZC6T4KQ2RS0B77",
    "file": "patterns/3b7c…d1.dxf",
    "sha256": "3b7c…d1",
    "size_name": null,
    "version": 3,
    "name": "перед",
    "filename": "front_v3.dxf",
    "fabric_purpose": "TECH_CARD_BOM_PURPOSE_MAIN",
    "bom_line_key": ""
  }
]
```

`line_key` is the sheet's stable identity and travels verbatim. `size_name: null` is legal and
common: a sheet filed under no size is graded inside the DXF (0284). `fabric_purpose` is the
binding that matters; `bom_line_key` is the legacy half kept for sheets on cards nobody has
sorted — resolve purpose first, never `bom_line_key` alone.

### 5.7 `markers/index.json`

```json
[
  {
    "file": "markers/m-1.json",
    "size_name": "m",
    "marker_name": "shell 150 cm",
    "bom_line_key": "01J8ZC4Q0FQ8M6R0K2"
  }
]
```

`size_name: null` = the marker has no single size (смешанный настил); its composition lives inside
the blob. Each `markers/<slug>-<n>.json` is protojson `common.TechCardMarker` — summary **and**
layout, geometry self-contained (contours inside the layout, never URL references). Only markers
of the CARD travel; a production run's markers belong to the run.

**Foreign identity inside the blob.** The marker file is the one entry that travels as RAW
protojson, so §6.2 («nothing foreign is written as an id») holds here only because the import
treats the blob as listed below — the file itself carries the source instance's numbers verbatim.
This list is the whole contract; nothing else in a marker is an identity.

* `summary.id` and `summary.tech_card_id` are **ignored and re-minted**. They are the source row's
  own identity: the marker is inserted on the imported card and takes that card's numbers.
* **Every** `size_id` in the blob — legacy `summary.size_id`, `summary.composition[].size_id`,
  `layout.composition[].size_id`, `layout.pieces[].size_id` — is remapped through the same
  `id_maps.sizes` name table as the rest of the archive. A size name the target dictionary does not
  have is a `size_unknown` hole and the WHOLE MARKER is dropped rather than written with a gap in
  its состав: a раскладка that lost a size no longer describes the lay that was measured, and
  the piece-instance formula would silently hand the orphaned contour zero instances.
* `summary.colorway_id` is **zeroed**, and the import writes a report line (`entity=marker`, reason
  `colorways_not_applied`). Colourways are products and an import does not create them (§5.3), so
  there is nothing to remap onto; the marker lands as общекарточная geometry and the hole is what
  says the length was measured on one colourway's article — at its roll width and its кромка.
* `summary.production_run_id` is **0 by construction**: only card markers travel (above). A blob
  carrying a non-zero one is a production run's marker and is not imported.
* `layout.pieces[].source_url` is **blanked**, like every other URL of the exporting instance
  (§4): the contours are inside the blob, and the link points at a host the target cannot read.
* `piece_id` on `layout.pieces` / `layout.placements` is **not an identity and is not touched**. It
  is layout-local — stable within this one blob, which is the only place that references it.

---

## 6. Import-side invariants that the format guarantees

The format cannot enforce behaviour, but it is designed so these hold:

1. **An imported card never looks signed.** `approval_state` → draft, `released_at` /
   `approved_at` cleared, signoffs dropped before the card reaches the write path (the create
   pipeline coerces supplied approved signoffs into fresh ones stamped with a digest — the only
   defence is not to hand it any).
2. **Nothing foreign is written as an id.** Every id in the archive is either remapped through
   `id_maps` / an index, or dropped with a hole.
3. **A missing reference degrades, it does not abort.** Corruption (sha mismatch), a wrong MAJOR
   and a missing `money_policy` are the only whole-archive refusals; everything else is a report
   line.
4. **The report is not empty by default.** `contents` vs the parsed counters is the positive
   control that separates "a clean archive" from "a parser that silently produced nothing".

---

## 7. Reason codes

The closed dictionary lives in `reasons.go` — one code, one line of human explanation, no code
outside that file. Adding a code is a format change and belongs in the same commit as its
explanation and the report action text.

| code | said plainly |
| --- | --- |
| `material_not_found` | no article in the target catalogue matches the passport |
| `material_ambiguous` | several live articles carry that code — none is picked |
| `material_unit_mismatch` | the code matched but the unit differs — not linked |
| `media_missing` | the archive has no file for a media slot the card references |
| `media_object_missing` | EXPORT side: the source bucket would not give up the object — the archive has no bytes for that slot |
| `media_upload_failed` | IMPORT side: the bytes were in the archive and the target bucket refused them — the slot is cleared |
| `pattern_invalid` | the pattern file is unreadable or is not a DXF/PDF |
| `size_unknown` | the size name is not in the target size dictionary |
| `measurement_unknown` | the measurement name is not in the target measurement dictionary — the row is dropped and the chart imports without it |
| `work_token_unknown` | the operation's work token is not in the target work catalogue |
| `category_unknown` | the category path does not resolve — the card lands without a category |
| `assembly_component_not_found` | the assembly component style number is not in the target base |
| `colorways_not_applied` | colourways travelled as reference and were not created |
| `wastage_claim_degraded` | a wastage/consumption claim lost its provenance and reads as manual |
| `norm_marker_lost` | the norm's marker stamp could not be re-sewn — the norm stands, the stamp does not |
| `style_number_taken` | the style number already exists in the target base |
| `unknown_entry` | the archive holds a file this server does not know (newer MINOR) |

`entity` on a hole is the human word for what it happened to — `media`, `material`, `bom_line`,
`pattern`, `marker`, `size`, `measurement`, `operation`, `assembly`, `colorway`, `card`, `archive`
— and `ref` is whatever names the row inside its own file (`bom_line_key=…`, `media_id=…`,
`size_name=…`). `measurement` is the size chart's SECOND named axis and is deliberately not
`size`: a measurement problem reported as a size problem sends the operator to the wrong
dictionary. The list is mirrored by the `Entity*` constants in `report.go`.
