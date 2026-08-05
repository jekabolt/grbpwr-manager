package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Colour variants of an AUXILIARY card's warehouse output (tech_card_output_variant, migration
// 0252). An aux card sewn from several fabrics produces into several stock buckets — one per colour
// — instead of the single tech_card.output_material_id of 0111. ZERO variant rows is legacy
// single-output mode and behaves exactly as it always did; the first row switches the card over.

// defaults for the auto-created output bucket when the card offers no material to copy from.
const (
	outputVariantDefaultSection  = "packaging"
	outputVariantDefaultClass    = "packaging"
	outputVariantDefaultUnit     = "pcs"
	outputVariantDefaultPurposeM = "production"
)

// ListOutputVariants returns a card's colour variants with the identity a UI needs resolved in the
// same round trip: the colour's name, the bucket's name and unit, and its on-hand balance. Active
// colours lead, then alphabetical by colour, so the list a planner reads is the list of colours they
// can actually plan. An empty result means the card is in legacy single-output mode.
func (s *Store) ListOutputVariants(ctx context.Context, techCardID int) ([]entity.TechCardOutputVariant, error) {
	return listOutputVariants(ctx, s.DB, techCardID)
}

// listOutputVariants runs on the caller's connection so a single-card read inside a snapshot
// transaction sees the same rows as the rest of that read.
func listOutputVariants(ctx context.Context, db dependency.DB, techCardID int) ([]entity.TechCardOutputVariant, error) {
	// LEFT JOIN material_stock: a bucket with no movement yet has no stock row at all, and that must
	// read as "no balance recorded" rather than as a zero balance or an absent material.
	rows, err := storeutil.QueryListNamed[entity.TechCardOutputVariant](ctx, db, `
		SELECT v.id, v.tech_card_id, v.color_code, v.material_id, v.active,
		       v.created_by, v.updated_by, v.created_at, v.updated_at,
		       c.name AS color_name, m.name AS material_name,
		       COALESCE(m.unit, '') AS unit, ms.on_hand AS on_hand,
		       m.archived AS material_archived
		FROM tech_card_output_variant v
		JOIN color c ON c.code = v.color_code
		JOIN material m ON m.id = v.material_id
		LEFT JOIN material_stock ms ON ms.material_id = v.material_id
		WHERE v.tech_card_id = :id
		ORDER BY v.active DESC, c.name, v.id`, map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("can't list tech card output variants: %w", err)
	}
	return rows, nil
}

// ListOutputVariantsByCardIds returns the colour variants of MANY cards at once, keyed by tech card id
// — the read the packing spec needs, where one order can touch a dozen component cards and a per-card
// round trip would be an N+1 inside an already per-style loop (R11).
//
// It returns RETIRED colours too, and that is the point: "the item's colour exists on this card but is
// switched off" is a different answer from "this card has no such colour", and only the first one may
// never be silently substituted. Filtering here would throw away the fact the rule needs most (see
// entity.ResolveAssemblyOutput). material.archived comes along for the same reason — a bucket the
// catalog has withdrawn must not be prescribed.
//
// No on-hand join: a balance is a different question from "which bucket". A card absent from the map
// has no colours at all — legacy single-output mode.
func (s *Store) ListOutputVariantsByCardIds(ctx context.Context, techCardIDs []int) (map[int][]entity.TechCardOutputVariant, error) {
	out := make(map[int][]entity.TechCardOutputVariant, len(techCardIDs))
	if len(techCardIDs) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[entity.TechCardOutputVariant](ctx, s.DB, `
		SELECT v.id, v.tech_card_id, v.color_code, v.material_id, v.active,
		       v.created_by, v.updated_by, v.created_at, v.updated_at,
		       c.name AS color_name, m.name AS material_name, COALESCE(m.unit, '') AS unit,
		       m.archived AS material_archived
		FROM tech_card_output_variant v
		JOIN color c ON c.code = v.color_code
		JOIN material m ON m.id = v.material_id
		WHERE v.tech_card_id IN (:ids)
		ORDER BY v.tech_card_id, v.active DESC, c.name, v.id`, map[string]any{"ids": techCardIDs})
	if err != nil {
		return nil, fmt.Errorf("can't list tech card output variants by ids: %w", err)
	}
	for _, r := range rows {
		out[r.TechCardId] = append(out[r.TechCardId], r)
	}
	return out, nil
}

// outputVariantRow is the stored shape of one variant plus its bucket's unit, the two facts every
// guard below needs.
type outputVariantRow struct {
	Id         int            `db:"id"`
	TechCardId int            `db:"tech_card_id"`
	ColorCode  string         `db:"color_code"`
	MaterialId int            `db:"material_id"`
	Active     bool           `db:"active"`
	Unit       sql.NullString `db:"unit"`
}

// UpsertOutputVariant creates or updates ONE colour variant of an auxiliary card and returns its id.
// It is deliberately a single-row upsert rather than the full-replace shape the assembly bill uses:
// a variant is a warehouse bucket, and phase 3 makes it the FK target of a production-run line, so
// re-minting the rows on every save would strand history.
//
// ins.Id == 0 creates; a non-zero id must already belong to techCardID. ins.MaterialId == 0 on
// create auto-creates the bucket from the card and the colour; on update it means "keep the bucket
// where it is" — a bucket move is a deliberate act that must name its target.
//
// Everything runs in one SERIALIZABLE transaction, because each guard reads a range the write then
// commits against: the card's purpose and approval state, the colour's uniqueness on the card, the
// bucket's exclusivity, and the card's shared unit of measure.
func (s *Store) UpsertOutputVariant(ctx context.Context, techCardID int, ins entity.TechCardOutputVariantInsert, username string) (int, error) {
	colorCode := strings.ToUpper(strings.TrimSpace(ins.ColorCode))
	if colorCode == "" {
		return 0, entity.NewFieldViolation("color_code", "required", "", "pick a colour from the dictionary")
	}
	if ins.MaterialId < 0 {
		return 0, entity.NewFieldViolation("material_id", "must_not_be_negative", "",
			"leave it 0 to create the warehouse bucket automatically")
	}
	if ins.Id < 0 {
		return 0, entity.NewFieldViolation("id", "must_not_be_negative", "", "leave it 0 to add a new colour")
	}

	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		card, err := storeutil.QueryNamedOne[struct {
			Name             string        `db:"name"`
			Purpose          string        `db:"purpose"`
			OutputMaterialId sql.NullInt64 `db:"output_material_id"`
		}](ctx, db, `SELECT name, purpose, output_material_id FROM tech_card WHERE id = :id`,
			map[string]any{"id": techCardID})
		if err != nil {
			return fmt.Errorf("load tech card %d for colour variant: %w", techCardID, err)
		}
		// A colour variant produces into the MATERIAL warehouse, which is the one destination a
		// sellable style must never have: its colours are colourways — products, SKUs, product stock.
		if entity.TechCardPurpose(card.Purpose) != entity.TechCardPurposeAuxiliary {
			return fmt.Errorf("%w: tech card %d is %q", entity.ErrTechCardNotAuxiliary, techCardID, card.Purpose)
		}
		// Inside the tx, like every other content write: the SERIALIZABLE read keeps the approval
		// state stable until this mutation commits, so a concurrent release cannot slip past it.
		if err := storeutil.RequireMutableTechCard(ctx, db, techCardID); err != nil {
			return err
		}

		// Resolve the colour here rather than from the dictionary cache: the cache serves archived
		// colours as live, and the name is needed anyway to label an auto-created bucket.
		color, err := storeutil.QueryNamedOne[struct {
			Name string `db:"name"`
		}](ctx, db, `SELECT name FROM color WHERE code = :code`, map[string]any{"code": colorCode})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return entity.NewFieldViolation("color_code", "not_found", colorCode,
					"pick a colour code from the colour dictionary")
			}
			return fmt.Errorf("load colour %q for variant: %w", colorCode, err)
		}

		siblings, err := storeutil.QueryListNamed[outputVariantRow](ctx, db, `
			SELECT v.id, v.tech_card_id, v.color_code, v.material_id, v.active, m.unit AS unit
			FROM tech_card_output_variant v
			JOIN material m ON m.id = v.material_id
			WHERE v.tech_card_id = :id
			ORDER BY v.id`, map[string]any{"id": techCardID})
		if err != nil {
			return fmt.Errorf("load colour variants of tech card %d: %w", techCardID, err)
		}
		// Resolve the addressed row BEFORE any other row-level complaint. An id that names no row of
		// THIS card is either gone or someone else's, and that is what the caller has to be told —
		// reporting "duplicate colour" for a foreign id would send them to edit a row they cannot see.
		var current *outputVariantRow
		if ins.Id > 0 {
			for i := range siblings {
				if siblings[i].Id == ins.Id {
					current = &siblings[i]
					break
				}
			}
			if current == nil {
				return fmt.Errorf("%w: variant %d is not a colour of tech card %d",
					entity.ErrOutputVariantNotFound, ins.Id, techCardID)
			}
		}
		// One colour, one row. Retirement is `active = FALSE`, never a second row for the colour.
		for i := range siblings {
			if siblings[i].ColorCode == colorCode && siblings[i].Id != ins.Id {
				return entity.NewFieldViolation("color_code", "duplicate",
					fmt.Sprintf("%s (%s)", color.Name, colorCode),
					"edit the existing colour variant instead, or deactivate it")
			}
		}

		materialID := ins.MaterialId
		if materialID == 0 && current != nil {
			// An update that names no bucket keeps the one it has. Naming a DIFFERENT bucket is
			// allowed and deliberate: the old bucket keeps whatever stock and moving average it
			// accumulated and stays in the material catalog: this card simply stops producing into
			// it. Nothing is moved or written off, because only the operator knows whether that
			// stock is still the same physical thing.
			materialID = current.MaterialId
		}
		var unit string
		switch {
		case materialID > 0:
			m, err := storeutil.QueryNamedOne[struct {
				Unit     sql.NullString `db:"unit"`
				Archived bool           `db:"archived"`
			}](ctx, db, `SELECT unit, archived FROM material WHERE id = :id`, map[string]any{"id": materialID})
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return entity.NewFieldViolation("material_id", "not_found",
						fmt.Sprintf("material %d", materialID), "pick an existing material or leave it 0 to create one")
				}
				return fmt.Errorf("load material %d for colour variant: %w", materialID, err)
			}
			// An archived article is retired nomenclature. Pointing a live colour at it would make a
			// card produce into something the catalog has already withdrawn.
			if m.Archived {
				return entity.NewFieldViolation("material_id", "archived",
					fmt.Sprintf("material %d", materialID),
					"material is archived; un-archive it or pick another")
			}
			unit = strings.TrimSpace(m.Unit.String)
			// A unitless bucket cannot take part in the one-unit-per-card rule, and its received
			// quantities would mean nothing on the shelf. Refuse it at the door rather than let the
			// empty unit propagate into the card and weaken the rule for every later colour.
			if unit == "" {
				return entity.NewFieldViolation("material_id", "no_unit",
					fmt.Sprintf("material %d", materialID),
					"material has no unit of measure; set it in the materials admin first")
			}
		default:
			materialID, unit, err = createOutputVariantMaterial(ctx, db, card.Name, color.Name,
				card.OutputMaterialId, siblings, username)
			if err != nil {
				return err
			}
		}

		// uniq_tcov_material as a readable refusal instead of a driver error. Nothing has ever stopped
		// N cards from pointing at one tech_card.output_material_id, so "adopt this material as a
		// colour" WILL land on a bucket some other card already claimed — and if it went through, that
		// bucket's moving average would blend two physically different articles.
		for i := range siblings {
			if siblings[i].MaterialId == materialID && siblings[i].Id != ins.Id {
				return fmt.Errorf("%w: material %d is already the %s variant of this card",
					entity.ErrOutputVariantMaterialClaimed, materialID, siblings[i].ColorCode)
			}
		}
		claim, err := storeutil.QueryNamedOne[struct {
			TechCardId int    `db:"tech_card_id"`
			ColorCode  string `db:"color_code"`
			CardName   string `db:"card_name"`
		}](ctx, db, `
			SELECT v.tech_card_id, v.color_code, t.name AS card_name
			FROM tech_card_output_variant v
			JOIN tech_card t ON t.id = v.tech_card_id
			WHERE v.material_id = :mid AND v.tech_card_id <> :card`,
			map[string]any{"mid": materialID, "card": techCardID})
		switch {
		case err == nil:
			return fmt.Errorf("%w: material %d is the %s variant of %q (tech card %d)",
				entity.ErrOutputVariantMaterialClaimed, materialID, claim.ColorCode, claim.CardName, claim.TechCardId)
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("check colour variant material claim: %w", err)
		}
		// The LEGACY claim, which the table's UNIQUE cannot see: another card may still hold this
		// material as its single tech_card.output_material_id (no UNIQUE has ever guarded that
		// column). Adopting it here would point two cards at one bucket by two different mechanisms —
		// exactly the blended moving average uniq_tcov_material exists to prevent, just spelled
		// differently. This card's OWN output material is deliberately not matched: adopting it is
		// the intended migration path out of single-output mode.
		legacy, err := storeutil.QueryNamedOne[struct {
			Id   int    `db:"id"`
			Name string `db:"name"`
		}](ctx, db, `SELECT id, name FROM tech_card WHERE output_material_id = :mid AND id <> :card`,
			map[string]any{"mid": materialID, "card": techCardID})
		switch {
		case err == nil:
			return fmt.Errorf("%w: material %d is the single output of tech card %q (%d)",
				entity.ErrOutputVariantMaterialClaimed, materialID, legacy.Name, legacy.Id)
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("check colour variant legacy material claim: %w", err)
		}

		// One card, one unit. A run's received quantity is booked per colour but counted once, so a
		// card whose buckets are half in pcs and half in metres has a meaningless run total — and
		// material.unit freezes on the first movement, so the repair may be impossible later.
		for i := range siblings {
			if siblings[i].Id == ins.Id {
				continue
			}
			// A sibling with no unit asserts nothing — it cannot be the reason a legitimate choice is
			// refused. New buckets can no longer reach that state (unitless materials are refused
			// above), so this only covers rows adopted before that rule or blanked in the catalog.
			sibUnit := strings.TrimSpace(siblings[i].Unit.String)
			if sibUnit == "" {
				continue
			}
			if !strings.EqualFold(sibUnit, unit) {
				return fmt.Errorf("%w: this card's colours are measured in %q, the chosen material in %q",
					entity.ErrOutputVariantUnitMismatch, sibUnit, unit)
			}
		}

		// A NEW colour is always born active. proto3 bools carry no presence, so an omitted `active`
		// arrives as false and would create a colour that is invisible to every ACTIVE-only consumer
		// (the list badge, the receipt guard) while still pinning the card's purpose, which counts
		// every row — three subsystems disagreeing about whether the card is in variant mode. The
		// caller who genuinely wants a retired colour deactivates it with a follow-up update.
		active := ins.Active
		if current == nil {
			active = true
		}
		params := map[string]any{
			"id":           ins.Id,
			"tech_card_id": techCardID,
			"color_code":   colorCode,
			"material_id":  materialID,
			"active":       active,
			"username":     username,
		}
		if current != nil {
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE tech_card_output_variant
				SET color_code = :color_code, material_id = :material_id, active = :active,
				    updated_by = :username
				WHERE id = :id AND tech_card_id = :tech_card_id`, params); err != nil {
				return fmt.Errorf("update colour variant %d: %w", ins.Id, err)
			}
			id = ins.Id
			return nil
		}
		newID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO tech_card_output_variant
				(tech_card_id, color_code, material_id, active, created_by, updated_by)
			VALUES (:tech_card_id, :color_code, :material_id, :active, :username, :username)`, params)
		if err != nil {
			return fmt.Errorf("create colour variant of tech card %d: %w", techCardID, err)
		}
		id = newID
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// createOutputVariantMaterial mints the warehouse bucket a new colour produces into, named
// "<card> — <colour>". The operator asked for a colour, not for a catalog entry, so the attributes
// are copied from a TEMPLATE the card already vouches for rather than invented: the card's existing
// single output material when it has one, else the bucket of its first colour variant, else the
// packaging defaults (a dust bag / shopper / box is packaging measured in pieces).
//
// The template chain deliberately puts the sibling variant AFTER output_material_id but BEFORE the
// defaults: a card that entered variant mode without ever having a single output would otherwise get
// a 'pcs' bucket next to metre-measured siblings and be refused by the unit rule it cannot fix.
//
// min_stock stays NULL on purpose — the low-stock alert fires on any material with a threshold and
// no stock, so a freshly minted empty bucket would raise an alarm about a colour nobody has produced
// yet. Supplier and lead time stay NULL: we make this, we do not buy it. Everything is editable in
// the material catalog afterwards.
func createOutputVariantMaterial(
	ctx context.Context,
	db dependency.DB,
	cardName, colorName string,
	cardOutputMaterialID sql.NullInt64,
	siblings []outputVariantRow,
	username string,
) (int, string, error) {
	templateID := 0
	if cardOutputMaterialID.Valid && cardOutputMaterialID.Int64 > 0 {
		templateID = int(cardOutputMaterialID.Int64)
	} else if len(siblings) > 0 {
		templateID = siblings[0].MaterialId
	}

	section, class, unit, purpose := outputVariantDefaultSection, outputVariantDefaultClass,
		outputVariantDefaultUnit, outputVariantDefaultPurposeM
	if templateID > 0 {
		tmpl, err := storeutil.QueryNamedOne[struct {
			Section       sql.NullString `db:"section"`
			MaterialClass sql.NullString `db:"material_class"`
			Unit          sql.NullString `db:"unit"`
			Purpose       sql.NullString `db:"purpose"`
		}](ctx, db, `SELECT section, material_class, unit, purpose FROM material WHERE id = :id`,
			map[string]any{"id": templateID})
		// A template that vanished is not a reason to refuse the colour — fall back to the defaults.
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, "", fmt.Errorf("load template material %d for colour variant: %w", templateID, err)
		}
		if err == nil {
			if v := strings.TrimSpace(tmpl.Section.String); v != "" {
				section = v
			}
			if v := strings.TrimSpace(tmpl.MaterialClass.String); v != "" {
				class = v
			}
			if v := strings.TrimSpace(tmpl.Unit.String); v != "" {
				unit = v
			}
			if v := strings.TrimSpace(tmpl.Purpose.String); v != "" {
				purpose = v
			}
		}
	}
	// The unit answers to the card's existing colours, not to the template, whenever the two could
	// disagree: a card whose single output material is measured differently from the variants it
	// already has would otherwise mint a bucket the unit rule immediately refuses — an auto-create
	// that cannot succeed. Section, class and purpose stay with the template; only the unit is an
	// invariant across the card.
	for i := range siblings {
		if v := strings.TrimSpace(siblings[i].Unit.String); v != "" {
			unit = v
			break
		}
	}

	name := strings.TrimSpace(cardName) + " — " + strings.TrimSpace(colorName)
	m := &entity.MaterialInsert{
		Name:          name,
		Section:       section,
		MaterialClass: class,
		Unit:          sql.NullString{String: unit, Valid: true},
		Purpose:       purpose,
		Color:         sql.NullString{String: colorName, Valid: colorName != ""},
		CreatedBy:     username,
		UpdatedBy:     username,
	}
	// createMaterialInTx, not CreateMaterial: the exported one opens its OWN transaction (sub-stores
	// keep the outer tx opener), so its material would outlive a rollback of the variant that asked
	// for it. The code is auto-generated from these attributes, as for any code-less create.
	id, err := createMaterialInTx(ctx, db, m)
	if err != nil {
		return 0, "", fmt.Errorf("create output material for colour %q: %w", colorName, err)
	}
	return id, unit, nil
}

// DeleteOutputVariant removes a colour variant outright. Deactivation (active=false) is the normal
// retirement — it keeps the bucket, its stock and its history — so a delete is the deliberate "this
// colour was a mistake" action, and it is also the ESCAPE from the purpose lock: any variant row,
// active or not, pins a card as auxiliary.
//
// The bucket itself is never deleted. It keeps whatever stock and moving average it accumulated and
// stays in the material catalog to be archived by hand; unhooking it here only means this card no
// longer produces into it.
//
// That holds even when the bucket currently HOLDS stock, and deliberately so. Refusing the delete
// would make the purpose lock inescapable — a card that received one batch of a colour could never
// be re-classified, however wrong the classification. The stock is not orphaned by the delete: it
// stays on the same material, with the same history and the same valuation, and remains issuable,
// adjustable and write-offable from the materials admin. Only the card's claim on it goes.
func (s *Store) DeleteOutputVariant(ctx context.Context, id int) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		row, err := storeutil.QueryNamedOne[struct {
			TechCardId int `db:"tech_card_id"`
		}](ctx, db, `SELECT tech_card_id FROM tech_card_output_variant WHERE id = :id`,
			map[string]any{"id": id})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: colour variant %d", entity.ErrOutputVariantNotFound, id)
			}
			return fmt.Errorf("load colour variant %d: %w", id, err)
		}
		// A variant is card content like a BOM line, so a released card's variants are frozen too.
		// This is not a trap: the purpose flip that a variant pins is itself refused on a released
		// card, so both roads out of "released" start with the same re-open to draft.
		if err := storeutil.RequireMutableTechCard(ctx, db, row.TechCardId); err != nil {
			return err
		}
		// A colour a production run has planned into is history, not a mistake. Deleting it would take
		// the run's grid with it (or, since 0253 RESTRICTs, fail as a driver 1451 that names a
		// constraint instead of a way forward) — and the colour's warehouse bucket, its stock and its
		// moving average would stay behind with nothing left to explain where they came from.
		// Deactivation is the retirement that keeps all of it. The FK is the backstop for writers that
		// bypass this store; this pre-check is what makes the refusal readable.
		// The refusal NAMES the runs, because a bare count is a dead end: the commonest way to meet this
		// message is a cancelled or abandoned run nobody remembers, and "3 lines reference this colour"
		// gives an operator nothing to go and look at. A cancelled run pins a colour exactly as a live
		// one does — its grid is still a row in the table — so the message says that too, or the card
		// reads as permanently stuck.
		// line_count, not `lines` — LINES is a reserved word in MySQL (LOAD DATA ... LINES TERMINATED
		// BY) and an unquoted alias fails with a 1064 that names the wrong thing entirely.
		refs, err := storeutil.QueryListNamed[struct {
			RunId     int `db:"run_id"`
			LineCount int `db:"line_count"`
		}](ctx, db, `
			SELECT run_id, COUNT(*) AS line_count FROM production_run_line
			WHERE output_variant_id = :id GROUP BY run_id ORDER BY run_id`,
			map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("count production run lines of colour variant %d: %w", id, err)
		}
		if len(refs) > 0 {
			totalLines := 0
			named := make([]string, 0, 3)
			for i, r := range refs {
				totalLines += r.LineCount
				if i < 3 {
					named = append(named, strconv.Itoa(r.RunId))
				}
			}
			runList := strings.Join(named, ", ")
			if len(refs) > len(named) {
				runList += fmt.Sprintf(" and %d more", len(refs)-len(named))
			}
			return fmt.Errorf("%w: %d production run line(s) in run(s) %s still produce this colour (a cancelled run pins it too, until the run itself is deleted)",
				entity.ErrOutputVariantReferencedByRun, totalLines, runList)
		}
		rows, err := storeutil.ExecNamedRows(ctx, db,
			`DELETE FROM tech_card_output_variant WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("delete colour variant %d: %w", id, err)
		}
		if rows == 0 {
			return fmt.Errorf("%w: colour variant %d", entity.ErrOutputVariantNotFound, id)
		}
		return nil
	})
}
