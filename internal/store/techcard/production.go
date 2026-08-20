package techcard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// --- inserts (called within the AddTechCard / UpdateTechCard transaction) ---

// insertTechCardConstruction writes the card's DEFAULTS row.
//
// `pressing` and `overlock_thread_count` are NOT written and are not a forgotten pair: 0306 dropped
// both columns — the prose moved into `notes` under a tag and the thread count became an overlock
// PROFILE, because one thread count on a card could only ever describe one overlock and a card may
// run several. The entity no longer carries either field; what survives them is their frozen
// POSITION in the CONSTRUCTION digest tuple. Naming either column here is an immediate 1054 on
// every save that touches construction.
func insertTechCardConstruction(ctx context.Context, db dependency.DB, tcID int, c *entity.TechCardConstruction) error {
	if c == nil {
		return nil
	}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_construction
			(tech_card_id, default_seam_class, default_stitches_per_cm, hem_finish, notes)
		VALUES (:tech_card_id, :default_seam_class, :default_stitches_per_cm, :hem_finish, :notes)`,
		map[string]any{
			"tech_card_id":            tcID,
			"default_seam_class":      c.DefaultSeamClass,
			"default_stitches_per_cm": c.DefaultStitchesPerCm,
			"hem_finish":              c.HemFinish,
			"notes":                   c.Notes,
		}); err != nil {
		return fmt.Errorf("failed to insert tech card construction: %w", err)
	}
	return nil
}

// equipmentKindMachine / equipmentKindPress say which of the two lists a profile row came from. The
// kind is written from THE LIST, never inferred from a field, and that is what makes «a machine row
// wearing press settings» unrepresentable rather than merely refused: entity.TechCardMachineProfile
// has no press fields to fill and entity.TechCardPressProfile has no machine fields.
const (
	equipmentKindMachine = "machine"
	equipmentKindPress   = "press"
)

// insertTechCardEquipmentProfiles writes the card's equipment park — the machines this style is sewn
// on and the ВТО modes it is pressed in — into the one table both kinds share.
//
// PRESENCE, not emptiness, is the gate, and the same nil is read in two places for two different
// reasons: here it means «this payload has nothing to write», and in UpdateTechCard's full-replace
// loop it means «do not DELETE what is stored». A caller that honoured only one of the two would
// either lose the stored park or write a second copy of it.
func insertTechCardEquipmentProfiles(ctx context.Context, db dependency.DB, tcID int, c *entity.TechCardConstruction) error {
	if c == nil || c.EquipmentDefaults == nil {
		return nil
	}
	d := c.EquipmentDefaults
	if err := validateTechCardEquipmentProfiles(d); err != nil {
		return err
	}
	rows := make([]map[string]any, 0, len(d.Machines)+len(d.Presses))
	for _, m := range d.Machines {
		row := equipmentProfileRow(tcID, equipmentKindMachine, m.ProfileKey, m.MachineType, m.Label, m.Note)
		row["thread_count"] = m.ThreadCount
		row["needle_type"] = m.NeedleType
		row["needle_size_nm"] = m.NeedleSizeNm
		row["bed_type"] = m.BedType
		row["automation"] = m.Automation
		row["thread_tension"] = m.ThreadTension
		row["thread_tension_note"] = m.ThreadTensionNote
		row["attachment_kind"] = m.AttachmentKind
		row["stitches_per_cm"] = m.StitchesPerCm
		row["stitch_width_mm"] = m.StitchWidthMm
		rows = append(rows, row)
	}
	for _, p := range d.Presses {
		row := equipmentProfileRow(tcID, equipmentKindPress, p.ProfileKey, p.PressEquipment, p.Label, p.Note)
		row["press_operation_type"] = p.PressOperationType
		row["press_temperature_c"] = p.PressTemperatureC
		row["press_dwell_sec"] = p.PressDwellSec
		row["press_pressure_n_cm2"] = p.PressPressureNCm2
		row["press_steam"] = p.PressSteam
		row["press_cloth"] = p.PressCloth
		rows = append(rows, row)
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_equipment_profile", rows); err != nil {
		return fmt.Errorf("failed to insert tech card equipment profiles: %w", err)
	}
	return nil
}

// equipmentProfileRow builds the FULL column set of one profile row with both kind-specific blocks
// empty, and both kinds go through it. Not tidiness: BulkInsert takes its column list from the FIRST
// row of the batch, so a machine row and a press row carrying different keys would write whichever
// kind came first and silently drop the other kind's settings on the floor.
func equipmentProfileRow(tcID int, kind, profileKey, equipment string, label, note sql.NullString) map[string]any {
	return map[string]any{
		"tech_card_id": tcID,
		"profile_key":  profileKey,
		"kind":         kind,
		"equipment":    equipment,
		"label":        label,
		"note":         note,
		// The other kind's block stays NULL. NULL here is not «inherit» — a profile inherits from
		// nothing; it is «this setting does not exist on this kind of equipment».
		"thread_count":         nil,
		"needle_type":          nil,
		"needle_size_nm":       nil,
		"bed_type":             nil,
		"automation":           nil,
		"thread_tension":       nil,
		"thread_tension_note":  nil,
		"attachment_kind":      nil,
		"stitches_per_cm":      nil,
		"stitch_width_mm":      nil,
		"press_operation_type": nil,
		"press_temperature_c":  nil,
		"press_dwell_sec":      nil,
		"press_pressure_n_cm2": nil,
		"press_steam":          nil,
		"press_cloth":          nil,
	}
}

// validateTechCardEquipmentProfiles closes the one thing 0306 deliberately leaves open: the PAIR
// (kind, equipment). The schema checks the two vocabularies as a union — `chk_eqp_equipment` accepts
// every machine token and every press token in the one column both kinds share — so
// `INSERT (kind='machine', equipment='iron')` passes the database and lands a press in the machine
// park, where the read path hands it back as a machine type nothing maps and the machine silently
// disappears off the card. A two-column CHECK would have caught it with error 3819: no field name,
// no words, for a value the operator picked from a list — the argument 0289 settled, so the pair is
// checked here instead, in a sentence.
//
// The other half of the invariant needs no check and gets none: «a machine row with press columns
// filled» is unrepresentable — see the comment on equipmentKindMachine.
func validateTechCardEquipmentProfiles(d *entity.TechCardEquipmentDefaults) error {
	for i := range d.Machines {
		field := fmt.Sprintf("construction.equipment_defaults.machines[%d]", i)
		if err := requireEquipmentProfileKey(d.Machines[i].ProfileKey, field); err != nil {
			return err
		}
		if !entity.ValidMachineTypes[entity.TechCardMachineType(d.Machines[i].MachineType)] {
			return entity.NewFieldViolation(field+".machine_type", "not_a_machine_type",
				d.Machines[i].MachineType,
				"a machine profile has to name a sewing machine; iron / press / fusing_press / steam_dummy / steamer belong to a PRESS profile")
		}
	}
	for i := range d.Presses {
		field := fmt.Sprintf("construction.equipment_defaults.presses[%d]", i)
		if err := requireEquipmentProfileKey(d.Presses[i].ProfileKey, field); err != nil {
			return err
		}
		if !entity.ValidPressEquipment[entity.TechCardPressEquipment(d.Presses[i].PressEquipment)] {
			return entity.NewFieldViolation(field+".press_equipment", "not_press_equipment",
				d.Presses[i].PressEquipment,
				"a press profile has to name pressing equipment; a sewing machine belongs to a MACHINE profile")
		}
	}
	return nil
}

// requireEquipmentProfileKey refuses a blank durable key. The column is CHAR(26) NOT NULL and MySQL
// takes ” happily, so the schema cannot catch this; what it does catch is the SECOND blank-keyed
// profile, as a 1062 on uq_equipment_profile_key with nothing in it an operator could act on. A
// wire payload never gets here (dto mints a key for an empty one), which leaves the direct entity
// writers — the seeder, the clone path's fixtures, tests — and a keyless profile is a row no step
// can ever reference.
func requireEquipmentProfileKey(key, field string) error {
	if strings.TrimSpace(key) == "" {
		return entity.NewFieldViolation(field+".profile_key", "required", "",
			"an equipment profile is identified by its durable key — a row without one is one no step can point at")
	}
	return nil
}

func insertTechCardOperations(ctx context.Context, db dependency.DB, tcID int, ops []entity.TechCardOperation, bomRes bomResolver) error {
	if len(ops) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(ops))
	for i, o := range ops {
		rows = append(rows, map[string]any{
			"tech_card_id":     tcID,
			"operation_number": o.OperationNumber,
			"operation_type":   string(o.OperationType),
			"zone":             string(o.Zone),
			"stitches_per_cm":  o.StitchesPerCm,
			"seam_class":       o.SeamClass,
			// Millimetres. NULL here means «inherit the card standard», and 0 means «cut on the line
			// as drawn» — the store writes whichever the payload carried and invents neither.
			"seam_allowance_mm":  o.SeamAllowanceMm,
			"topstitch_mode":     o.TopstitchMode,
			"topstitch_width_mm": o.TopstitchWidthMm,
			"topstitch_rows":     o.TopstitchRows,
			"attachment_kind":    o.AttachmentKind,
			"attachment_size_mm": o.AttachmentSizeMm,
			"smv":                o.SMV,
			"note":               o.Note,
			"callout_number":     o.CalloutNumber,
			"display_order":      i,
			// «На чём» (0306) — the machine block and the ВТО block. Every one of these is an
			// OVERRIDE: NULL means «inherit from the profile the step points at, or from the card»,
			// and the store never materialises an inherited value into the row — the moment it does,
			// «the technologist chose 4 threads» stops being distinguishable from «it defaulted to 4».
			// The two profile keys are SOFT references (no FK): the park is full-replaced on every
			// save, so a key naming nothing means «nothing to inherit», not «this row is invalid».
			"machine_type":         o.MachineType,
			"machine_profile_key":  o.MachineProfileKey,
			"thread_count":         o.ThreadCount,
			"needle_type":          o.NeedleType,
			"needle_size_nm":       o.NeedleSizeNm,
			"thread_tension":       o.ThreadTension,
			"thread_tension_note":  o.ThreadTensionNote,
			"stitch_width_mm":      o.StitchWidthMm,
			"press_equipment":      o.PressEquipment,
			"press_profile_key":    o.PressProfileKey,
			"press_temperature_c":  o.PressTemperatureC,
			"press_dwell_sec":      o.PressDwellSec,
			"press_pressure_n_cm2": o.PressPressureNCm2,
			// Three-valued: NULL inherits, 0 is «без пара» — a real instruction, which is why the
			// column is written from a NullBool and not from a plain bool.
			"press_steam": o.PressSteam,
			"press_cloth": o.PressCloth,
			// Сборка (0307). Пусто = шаг ничего не собирает: это обработка, а не «не заполнено».
			// Колонка ключа _bin — сравнение побайтное, как обещает контракт.
			"output_unit_key":  o.OutputUnitKey,
			"output_unit_name": o.OutputUnitName,

			// --- ВИДЫ ОПЕРАЦИЙ: 32 колонки волны 0324 ------------------------------------------
			//
			// Порядок ключей ниже — канон волны (§1, он же порядок ALTER'а миграции, SELECT'а ниже
			// и полей entity.TechCardOperation). Ключи map'ы позиции не имеют, и именно поэтому
			// порядок здесь держится вручную: это единственное место, где разъезд четырёх списков
			// нечем поймать компилятору.
			//
			// Все 32 — NULLable, и NULL пишется как NULL: «не указано» не ноль и не «нет». Явное
			// «нет» там, где оно есть, приезжает отдельным токеном none (seam_securing, hole_prep,
			// reinforcement, peel_mode), и стор его не изобретает и не сворачивает в NULL.

			// Строчка (S).
			"needle_count":    o.NeedleCount,
			"needle_gauge_mm": o.NeedleGaugeMm,
			"seam_securing":   o.SeamSecuring,
			"row_spacing_mm":  o.RowSpacingMm,
			"fullness_ratio":  o.FullnessRatio,

			// Раскладка повторов (PL).
			"placement_count": o.PlacementCount,
			"pitch_mm":        o.PitchMm,

			// Фурнитура (H).
			"attach_method":      o.AttachMethod,
			"hole_prep":          o.HolePrep,
			"reinforcement":      o.Reinforcement,
			"foldback_mm":        o.FoldbackMm,
			"cycle_stitch_count": o.CycleStitchCount,

			// Печать (P).
			"print_method":     o.PrintMethod,
			"peel_mode":        o.PeelMode,
			"second_press_sec": o.SecondPressSec,
			"pressure_scale":   o.PressureScale,

			// Сварка и проклейка (W).
			"air_temperature_c": o.AirTemperatureC,
			"feed_speed_m_min":  o.FeedSpeedMMin,

			// Подрезка и выправка (T), чистка концов ниток (F).
			"trim_action":           o.TrimAction,
			"residual_allowance_mm": o.ResidualAllowanceMm,
			"residual_tail_max_mm":  o.ResidualTailMaxMm,

			// Дискриминаторы финишных глаголов (C, Q, WP).
			"cleaning_kind":    o.CleaningKind,
			"coverage_mode":    o.CoverageMode,
			"wet_process_kind": o.WetProcessKind,

			// Петли, закрепки, пуговицы, молнии (FA) и два поля строчки из дельты (S14, S17).
			"buttonhole_style":       o.ButtonholeStyle,
			"cut_length_mm":          o.CutLengthMm,
			"buttonhole_orientation": o.ButtonholeOrientation,
			"bartack_length_mm":      o.BartackLengthMm,
			"attach_pattern":         o.AttachPattern,
			"zipper_application":     o.ZipperApplication,
			"binding_style":          o.BindingStyle,
			"label_attach_stitch":    o.LabelAttachStitch,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_operation", rows); err != nil {
		return fmt.Errorf("failed to insert tech card operations: %w", err)
	}
	if err := insertTechCardOperationInputs(ctx, db, tcID, ops); err != nil {
		return err
	}
	if err := insertTechCardOperationMedia(ctx, db, tcID, ops); err != nil {
		return err
	}
	return insertTechCardOperationBoms(ctx, db, tcID, ops, bomRes)
}

// insertTechCardOperationMedia пишет фотографии шагов с выносками (0308).
//
// Выноски едут JSON-колонкой одним значением на картинку: у выноски нет внешних ссылок, она
// читается и пишется только целиком со своим снимком. Форма уже проверена в dto — сюда приходит
// то, что сервер согласился считать выноской, и стор не валидирует повторно.
//
// Ids операций перечитываются по display_order — та же причина, что у входов и BOM-связей выше:
// BulkInsert их не возвращает, а вставка по одной ради LastInsertId стоила бы round-trip на шаг.
func insertTechCardOperationMedia(ctx context.Context, db dependency.DB, tcID int, ops []entity.TechCardOperation) error {
	wanted := false
	for i := range ops {
		if len(ops[i].Media) > 0 {
			wanted = true
			break
		}
	}
	if !wanted {
		return nil
	}
	opRows, err := storeutil.QueryListNamed[struct {
		Id           int `db:"id"`
		DisplayOrder int `db:"display_order"`
	}](ctx, db,
		`SELECT id, display_order FROM tech_card_operation WHERE tech_card_id = :id`,
		map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load operations for media links: %w", err)
	}
	opIDByOrder := make(map[int]int, len(opRows))
	for _, r := range opRows {
		opIDByOrder[r.DisplayOrder] = r.Id
	}

	rows := make([]map[string]any, 0)
	for i, o := range ops {
		if len(o.Media) == 0 {
			continue
		}
		opID, ok := opIDByOrder[i]
		if !ok {
			return fmt.Errorf("operation %d missing after insert", i)
		}
		for j, m := range o.Media {
			anns := m.Annotations
			if anns == nil {
				anns = []entity.TechCardAnnotation{}
			}
			raw, err := json.Marshal(anns)
			if err != nil {
				return fmt.Errorf("marshal annotations of operation %d media %d: %w", i, j, err)
			}
			var caption any
			if m.Caption.Valid && m.Caption.String != "" {
				caption = m.Caption.String
			}
			rows = append(rows, map[string]any{
				"tech_card_operation_id": opID,
				"media_id":               m.MediaId,
				"caption":                caption,
				// Позиция В СПИСКЕ ШАГА, а не сквозная: филмстрип листается внутри своего шага.
				"display_order": j,
				"annotations":   string(raw),
			})
		}
	}
	if len(rows) > 0 {
		if err := storeutil.BulkInsert(ctx, db, "tech_card_operation_media", rows); err != nil {
			return fmt.Errorf("failed to insert operation media: %w", err)
		}
	}
	return nil
}

// insertTechCardOperationBoms writes the operation -> BOM-line links (0200): the off-part materials
// an operation consumes. Many-to-many, mirroring the piece links -- one operation can join several
// materials. Operation ids are re-read by display_order for the same reason as there.
func insertTechCardOperationBoms(ctx context.Context, db dependency.DB, tcID int, ops []entity.TechCardOperation, bomRes bomResolver) error {
	wanted := false
	for i := range ops {
		if len(ops[i].BomLineKeys) > 0 {
			wanted = true
			break
		}
	}
	if !wanted {
		return nil
	}
	opRows, err := storeutil.QueryListNamed[struct {
		Id           int `db:"id"`
		DisplayOrder int `db:"display_order"`
	}](ctx, db,
		`SELECT id, display_order FROM tech_card_operation WHERE tech_card_id = :id`,
		map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load operations for bom links: %w", err)
	}
	opIDByOrder := make(map[int]int, len(opRows))
	for _, r := range opRows {
		opIDByOrder[r.DisplayOrder] = r.Id
	}
	links := make([]map[string]any, 0)
	for i, o := range ops {
		opID, ok := opIDByOrder[i]
		if !ok {
			return fmt.Errorf("operation %d missing after insert", i)
		}
		for j, key := range o.BomLineKeys {
			bomID, err := resolveBomRef(bomRes, key, sql.NullInt32{},
				fmt.Sprintf("operations[%d].bom_line_keys[%d]", i, j))
			if err != nil {
				return err
			}
			if bomID == nil {
				continue
			}
			links = append(links, map[string]any{
				"operation_id":  opID,
				"bom_item_id":   bomID,
				"display_order": j,
			})
		}
	}
	if len(links) == 0 {
		return nil
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_operation_bom", links); err != nil {
		return fmt.Errorf("failed to insert operation bom links: %w", err)
	}
	return nil
}

// insertTechCardOperationInputs writes the ЕДИНЫЙ упорядоченный список входов операции (0307):
// каждая строка — либо деталь, либо узел, и display_order — позиция в ОБЪЕДИНЕНИИ.
//
// Пишет ДВЕ таблицы. Новая tech_card_operation_input — истина; легаси tech_card_operation_piece
// (0199) заполняется зеркально и остаётся источником для отката. Причина домовая: провалившийся
// деплой на DO откатывается сам, а readyz при этом возвращает 200 старым процессом — откатившийся
// код обязан найти свою таблицу наполненной. Двойная запись стоит десяти строк, потому что список
// уже канонический: детали из него фильтруются одним проходом.
//
// Ids операций перечитываются, а не протаскиваются из bulk-вставки: BulkInsert их не возвращает, а
// вставка по одной ради LastInsertId стоила бы round-trip на операцию. display_order уникален
// внутри карточки и только что записан, поэтому это надёжный join назад. Детали апсертятся ДО
// операций в insertTechCardChildren, так что их line_key здесь уже резолвятся.
func insertTechCardOperationInputs(ctx context.Context, db dependency.DB, tcID int, ops []entity.TechCardOperation) error {
	// Гвард считает ОБЪЕДИНЕНИЕ, а не только детали: шаг, у которого входы — одни узлы, при
	// проверке по PieceLineKeys потерял бы их целиком.
	wanted := false
	for i := range ops {
		if len(ops[i].AssemblyInputs) > 0 || len(ops[i].PieceLineKeys) > 0 {
			wanted = true
			break
		}
	}
	if !wanted {
		return nil
	}

	pieceRows, err := storeutil.QueryListNamed[pieceExistingRow](ctx, db,
		`SELECT id, line_key FROM tech_card_piece WHERE tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load cut-pieces for operation links: %w", err)
	}
	pieceByKey := make(map[string]int, len(pieceRows))
	for _, r := range pieceRows {
		pieceByKey[r.LineKey] = r.Id
	}

	opRows, err := storeutil.QueryListNamed[struct {
		Id           int `db:"id"`
		DisplayOrder int `db:"display_order"`
	}](ctx, db,
		`SELECT id, display_order FROM tech_card_operation WHERE tech_card_id = :id`,
		map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load operations for piece links: %w", err)
	}
	opIDByOrder := make(map[int]int, len(opRows))
	for _, r := range opRows {
		opIDByOrder[r.DisplayOrder] = r.Id
	}

	inputs := make([]map[string]any, 0)
	legacy := make([]map[string]any, 0)
	for i, o := range ops {
		opID, ok := opIDByOrder[i]
		if !ok {
			return fmt.Errorf("operation %d missing after insert", i)
		}
		// Канонический список приходит из конвертера. Пустой он только у неосведомлённой записи,
		// которую канонизация не трогала: тогда объединение вырождается в легаси-проекцию.
		union := o.AssemblyInputs
		if len(union) == 0 {
			union = make([]entity.OperationInput, 0, len(o.PieceLineKeys))
			for _, k := range o.PieceLineKeys {
				union = append(union, entity.OperationInput{Kind: entity.AssemblyInputPiece, Key: k})
			}
		}
		legacyOrder := 0
		for j, in := range union {
			if in.Kind == entity.AssemblyInputUnit {
				// Узел — ссылка по ключу, без FK: узел не строка таблицы, а результат шага.
				// Существование ключа уже удостоверено канонизацией (правило 1).
				inputs = append(inputs, map[string]any{
					"operation_id":  opID,
					"piece_id":      nil,
					"unit_key":      in.Key,
					"display_order": j,
				})
				continue
			}
			pieceID, ok := pieceByKey[in.Key]
			if !ok {
				// Field-tagged rather than a bare error, so the admin client can pin it to the exact
				// operation row instead of failing the whole card with an unattributable message.
				return entity.NewFieldViolation(fmt.Sprintf("operations[%d].input_keys[%d]", i, j),
					fmt.Sprintf("no cut-piece %q in this style", in.Key), "",
					"reference an existing cut-piece by its line_key")
			}
			inputs = append(inputs, map[string]any{
				"operation_id":  opID,
				"piece_id":      pieceID,
				"unit_key":      nil,
				"display_order": j,
			})
			// Легаси-зеркало нумеруется по СВОЕЙ шкале (только детали, подряд): у 0199 порядок
			// пер-табличный, и записать в него сквозные позиции объединения значило бы отдать
			// откатившемуся коду дыры в нумерации.
			legacy = append(legacy, map[string]any{
				"operation_id":  opID,
				"piece_id":      pieceID,
				"display_order": legacyOrder,
			})
			legacyOrder++
		}
	}
	if len(inputs) > 0 {
		if err := storeutil.BulkInsert(ctx, db, "tech_card_operation_input", inputs); err != nil {
			return fmt.Errorf("failed to insert operation inputs: %w", err)
		}
	}
	if len(legacy) > 0 {
		if err := storeutil.BulkInsert(ctx, db, "tech_card_operation_piece", legacy); err != nil {
			return fmt.Errorf("failed to insert operation piece links: %w", err)
		}
	}
	return nil
}

func insertTechCardLabels(ctx context.Context, db dependency.DB, tcID int, labels []entity.TechCardLabel, bomRes bomResolver) error {
	if len(labels) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(labels))
	for i, l := range labels {
		bomItemID := l.BomItemId
		if bomItemID.Valid && bomItemID.Int32 == 0 {
			bomItemID = sql.NullInt32{}
		}
		if bomItemID.Valid && !bomRes.containsID(int(bomItemID.Int32)) {
			return entity.NewFieldViolation(fmt.Sprintf("labels[%d].bom_item_id", i), "not_in_tech_card",
				fmt.Sprintf("BOM line %d", bomItemID.Int32), "select a BOM line from this tech card")
		}
		rows = append(rows, map[string]any{
			"tech_card_id":  tcID,
			"label_type":    string(l.LabelType),
			"content":       l.Content,
			"placement":     l.Placement,
			"attachment":    l.Attachment,
			"size":          l.Size,
			"note":          l.Note,
			"bom_item_id":   bomItemID,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_label", rows); err != nil {
		return fmt.Errorf("failed to insert tech card labels: %w", err)
	}
	return nil
}

func insertTechCardPackaging(ctx context.Context, db dependency.DB, tcID int, p *entity.TechCardPackaging) error {
	if p == nil {
		return nil
	}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_packaging
			(tech_card_id, folding_method, polybag, bag_sticker, inserts, units_per_box,
			 box_marking, box_dimensions, weight_net_grams, weight_gross_grams, notes)
		VALUES (:tech_card_id, :folding_method, :polybag, :bag_sticker, :inserts, :units_per_box,
			 :box_marking, :box_dimensions, :weight_net_grams, :weight_gross_grams, :notes)`,
		map[string]any{
			"tech_card_id":       tcID,
			"folding_method":     p.FoldingMethod,
			"polybag":            p.Polybag,
			"bag_sticker":        p.BagSticker,
			"inserts":            p.Inserts,
			"units_per_box":      p.UnitsPerBox,
			"box_marking":        p.BoxMarking,
			"box_dimensions":     p.BoxDimensions,
			"weight_net_grams":   p.WeightNetGrams,
			"weight_gross_grams": p.WeightGrossGrams,
			"notes":              p.Notes,
		}); err != nil {
		return fmt.Errorf("failed to insert tech card packaging: %w", err)
	}
	return nil
}

func insertTechCardCosting(ctx context.Context, db dependency.DB, tcID int, c *entity.TechCardCosting) error {
	if c == nil {
		return nil
	}
	// hardware_cost / packaging_cost are deliberately absent (Phase 2): the columns still exist —
	// they hold the pre-migration values 0237's exception report points at — but the application
	// writes NULL rows only; hardware/packaging money lives in the BOM.
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_costing
			(tech_card_id, cmt_cost, logistics_cost, overhead_cost,
			 defect_percent, currency, notes, target_margin_pct)
		VALUES (:tech_card_id, :cmt_cost, :logistics_cost, :overhead_cost,
			 :defect_percent, :currency, :notes, :target_margin_pct)`,
		map[string]any{
			"tech_card_id":      tcID,
			"cmt_cost":          c.CmtCost,
			"logistics_cost":    c.LogisticsCost,
			"overhead_cost":     c.OverheadCost,
			"defect_percent":    c.DefectPercent,
			"currency":          c.Currency,
			"notes":             c.Notes,
			"target_margin_pct": c.TargetMarginPct,
		}); err != nil {
		return fmt.Errorf("failed to insert tech card costing: %w", err)
	}
	return nil
}

func insertTechCardIssues(ctx context.Context, db dependency.DB, tcID int, issues []entity.TechCardIssue) error {
	if len(issues) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(issues))
	for i, is := range issues {
		rows = append(rows, map[string]any{
			"tech_card_id":     tcID,
			"operation_number": is.OperationNumber,
			"callout_number":   is.CalloutNumber,
			"raised_by":        is.RaisedBy,
			"severity":         string(is.Severity),
			"status":           string(is.Status),
			"description":      is.Description,
			"resolution_note":  is.ResolutionNote,
			"display_order":    i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_issue", rows); err != nil {
		return fmt.Errorf("failed to insert tech card issues: %w", err)
	}
	return nil
}

func insertTechCardSignoffs(ctx context.Context, db dependency.DB, tcID int, signoffs []entity.TechCardSignoff) error {
	if len(signoffs) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(signoffs))
	for i, s := range signoffs {
		rows = append(rows, map[string]any{
			"tech_card_id":  tcID,
			"section":       string(s.Section),
			"state":         string(s.State),
			"signed_by":     s.SignedBy,
			"signed_at":     s.SignedAt,
			"note":          s.Note,
			"signed_digest": s.SignedDigest,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_signoff", rows); err != nil {
		return fmt.Errorf("failed to insert tech card signoffs: %w", err)
	}
	return nil
}

// --- enrich (load production sections for read paths) ---

type techCardConstructionRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardConstruction
}

type techCardIssueRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardIssue
}

type techCardSignoffRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardSignoff
}

type techCardOperationRow struct {
	TechCardID int `db:"tech_card_id"`
	// Id is the operation's primary key. It is read purely so the link passes below can join on the
	// real row identity instead of guessing it from display_order; it is deliberately NOT part of
	// entity.TechCardOperation and never reaches the wire (the wire identity of an operation is its
	// operation_number).
	Id int `db:"id"`
	entity.TechCardOperation
}

// techCardOperationsQuery reads the steps of every requested card.
//
// Operations are returned sorted ascending by operation_number (the addressable «оп. 10, 20, …»);
// unnumbered operations sort last, with display_order as a stable tiebreaker within each group.
//
// The LEFT JOIN onto tech_card_bom_item is gone with the singular bom_item_id it resolved: the
// materials a step consumes are the many-to-many links (0200) read separately, and the single column
// was a second answer that the printed sheet had to subtract from the first.
//
// Explicit column list, not SELECT *, for the reason spelled out at the construction read above.
//
// Хвост списка — 32 колонки видов операций (0324), ТЕМ ЖЕ порядком, что в ALTER'е миграции, в
// named-map insertTechCardOperations и в полях entity.TechCardOperation: S -> PL -> H -> P -> W -> T
// -> F -> C -> Q -> WP, затем дельта FA -> S14 -> S17. Старая строка отдаёт по ним NULL, и NULL
// обязан доехать до Valid=false, а не до нуля: «технолог молчит» и «технолог сказал ноль» — разные
// инструкции цеху.
//
// КОММЕНТАРИИ К КОЛОНКАМ ЖИВУТ ЗДЕСЬ, СНАРУЖИ СТРОКИ ЗАПРОСА, А НЕ ВНУТРИ НЕЁ. Двоеточие внутри
// SQL-комментария «--» sqlx разбирает как именованный параметр и роняет bind этого запроса (у него
// есть :ids) — уже стоило проекту одного деплоя. Запрос вынесен в константу, чтобы bind можно было
// пинить тестом без базы; см. techCardOperationsQueryBinds.
const techCardOperationsQuery = `
		SELECT o.id, o.tech_card_id, o.operation_number, o.operation_type, o.zone,
		       o.stitches_per_cm, o.seam_class, o.seam_allowance_mm,
		       o.topstitch_mode, o.topstitch_width_mm, o.topstitch_rows,
		       o.attachment_kind, o.attachment_size_mm,
		       o.machine_type, o.machine_profile_key, o.thread_count, o.needle_type,
		       o.needle_size_nm, o.thread_tension, o.thread_tension_note, o.stitch_width_mm,
		       o.press_equipment, o.press_profile_key, o.press_temperature_c, o.press_dwell_sec,
		       o.press_pressure_n_cm2, o.press_steam, o.press_cloth,
		       o.smv, o.note, o.callout_number,
		       o.output_unit_key, o.output_unit_name,
		       o.needle_count, o.needle_gauge_mm, o.seam_securing, o.row_spacing_mm,
		       o.fullness_ratio,
		       o.placement_count, o.pitch_mm,
		       o.attach_method, o.hole_prep, o.reinforcement, o.foldback_mm,
		       o.cycle_stitch_count,
		       o.print_method, o.peel_mode, o.second_press_sec, o.pressure_scale,
		       o.air_temperature_c, o.feed_speed_m_min,
		       o.trim_action, o.residual_allowance_mm,
		       o.residual_tail_max_mm,
		       o.cleaning_kind, o.coverage_mode, o.wet_process_kind,
		       o.buttonhole_style, o.cut_length_mm, o.buttonhole_orientation,
		       o.bartack_length_mm, o.attach_pattern, o.zipper_application,
		       o.binding_style, o.label_attach_stitch
		FROM tech_card_operation o
		WHERE o.tech_card_id IN (:ids)
		ORDER BY o.tech_card_id, o.operation_number IS NULL, o.operation_number, o.display_order`

// operationPos locates one operation inside the per-card slice enrichProduction builds, so a link row
// carrying only operation_id can be attached to the right element.
type operationPos struct {
	cardID int
	index  int
}

type techCardLabelRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardLabel
}

type techCardPackagingRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardPackaging
}

type techCardCostingRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardCosting
}

// enrichProduction loads the construction, operations, labels, packaging and
// costing sections for each card and attaches them.
func (s *Store) enrichProduction(ctx context.Context, cards []entity.TechCard) error {
	if len(cards) == 0 {
		return nil
	}
	ids := make([]int, 0, len(cards))
	for i := range cards {
		ids = append(ids, cards[i].Id)
	}

	// Explicit column list, not SELECT * (the packaging precedent below). 0306 dropped `pressing` and
	// `overlock_thread_count`, and the entity fields went with them. A star makes every read depend
	// on those two moving in exact lockstep — and on a Down they do not: the columns come back while
	// the struct has nowhere to put them, and a strict StructScan refuses an unmapped column. Naming
	// the columns turns «the migration and the binary ship together» from a hidden requirement into
	// one that shows up in this list.
	consRows, err := storeutil.QueryListNamed[techCardConstructionRow](ctx, s.DB,
		`SELECT tech_card_id, default_seam_class, default_stitches_per_cm, hem_finish, notes
		 FROM tech_card_construction WHERE tech_card_id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card construction: %w", err)
	}
	consByCard := make(map[int]*entity.TechCardConstruction, len(consRows))
	for i := range consRows {
		c := consRows[i].TechCardConstruction
		consByCard[consRows[i].TechCardID] = &c
	}

	// The card's equipment park (0306). Read as two passes rather than one flat row split by kind:
	// the two entity structs already carry the db tags for their own halves, and a third restatement
	// of twenty-odd column names is a third place for them to drift. Ordered by profile_key so a read
	// is stable; the digest sorts by key again in its own projection and does not rely on this.
	machineRows, err := storeutil.QueryListNamed[entity.TechCardMachineProfile](ctx, s.DB, `
		SELECT id, tech_card_id, profile_key, label, equipment, thread_count, needle_type,
		       needle_size_nm, bed_type, automation, thread_tension, thread_tension_note,
		       attachment_kind, stitches_per_cm, stitch_width_mm, note
		FROM tech_card_equipment_profile
		WHERE tech_card_id IN (:ids) AND kind = 'machine'
		ORDER BY tech_card_id, profile_key`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card machine profiles: %w", err)
	}
	pressRows, err := storeutil.QueryListNamed[entity.TechCardPressProfile](ctx, s.DB, `
		SELECT id, tech_card_id, profile_key, label, equipment, press_operation_type,
		       press_temperature_c, press_dwell_sec, press_pressure_n_cm2, press_steam,
		       press_cloth, note
		FROM tech_card_equipment_profile
		WHERE tech_card_id IN (:ids) AND kind = 'press'
		ORDER BY tech_card_id, profile_key`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card press profiles: %w", err)
	}
	equipmentByCard := make(map[int]*entity.TechCardEquipmentDefaults)
	equipmentOf := func(cardID int) *entity.TechCardEquipmentDefaults {
		d, ok := equipmentByCard[cardID]
		if !ok {
			d = &entity.TechCardEquipmentDefaults{}
			equipmentByCard[cardID] = d
		}
		return d
	}
	for i := range machineRows {
		d := equipmentOf(machineRows[i].TechCardId)
		d.Machines = append(d.Machines, machineRows[i])
	}
	for i := range pressRows {
		d := equipmentOf(pressRows[i].TechCardId)
		d.Presses = append(d.Presses, pressRows[i])
	}

	opRows, err := storeutil.QueryListNamed[techCardOperationRow](ctx, s.DB, techCardOperationsQuery,
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card operations: %w", err)
	}
	opsByCard := make(map[int][]entity.TechCardOperation, len(ids))
	// posByOpID is the only thing that may be used to attach a link row to an operation. The rows are
	// ordered by operation_number first, so an operation's POSITION in the slice below is not its
	// display_order whenever the card holds legacy rows with a NULL or non-canonical operation_number
	// (those sort last). Keying the link passes on display_order-as-index therefore silently attached
	// pieces/materials to the wrong operation on exactly those cards.
	posByOpID := make(map[int]operationPos, len(opRows))
	for _, r := range opRows {
		op := r.TechCardOperation
		opsByCard[r.TechCardID] = append(opsByCard[r.TechCardID], op)
		posByOpID[r.Id] = operationPos{cardID: r.TechCardID, index: len(opsByCard[r.TechCardID]) - 1}
	}

	// Входы операции — ЕДИНЫЙ упорядоченный список (0307). Читается своим проходом и цепляется
	// назад по operation_id (идентичность строки, а не позиционный суррогат); line_key едет рядом с
	// id, чтобы клиент получил ту же durable-ссылку, которой пишет.
	//
	// Порядок — display_order объединения: он и есть авторский порядок «деталь между узлами».
	inputRows, err := storeutil.QueryListNamed[struct {
		OpID     int            `db:"op_id"`
		PieceID  sql.NullInt64  `db:"piece_id"`
		PieceKey sql.NullString `db:"line_key"`
		UnitKey  sql.NullString `db:"unit_key"`
	}](ctx, s.DB, `
		SELECT o.id AS op_id, l.piece_id AS piece_id, p.line_key AS line_key, l.unit_key AS unit_key
		FROM tech_card_operation_input l
		JOIN tech_card_operation o ON o.id = l.operation_id
		LEFT JOIN tech_card_piece p ON p.id = l.piece_id
		WHERE o.tech_card_id IN (:ids)
		ORDER BY o.tech_card_id, o.display_order, l.display_order, l.id`,
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card operation inputs: %w", err)
	}
	// Операции, у которых строки в новой таблице ЕСТЬ. Всё остальное поедет фолбэком ниже.
	haveInputs := make(map[int]bool, len(inputRows))
	for _, l := range inputRows {
		pos, ok := posByOpID[l.OpID]
		if !ok {
			continue
		}
		haveInputs[l.OpID] = true
		list := opsByCard[pos.cardID]
		op := &list[pos.index]
		switch {
		case l.UnitKey.Valid && l.UnitKey.String != "":
			op.AssemblyInputs = append(op.AssemblyInputs, entity.OperationInput{
				Kind: entity.AssemblyInputUnit, Key: l.UnitKey.String,
			})
			op.InputKeys = append(op.InputKeys, l.UnitKey.String)
		case l.PieceID.Valid && l.PieceKey.Valid:
			op.AssemblyInputs = append(op.AssemblyInputs, entity.OperationInput{
				Kind: entity.AssemblyInputPiece, Key: l.PieceKey.String,
			})
			op.InputKeys = append(op.InputKeys, l.PieceKey.String)
			op.PieceIds = append(op.PieceIds, int(l.PieceID.Int64))
			op.PieceLineKeys = append(op.PieceLineKeys, l.PieceKey.String)
		}
	}

	// ПЕРЕХОДНЫЙ ФОЛБЭК на 0199 — пер-операционный, и он не компромисс, а точная семантика.
	//
	// Он существует ради окна отката: откатившийся код пишет только 0199, полная замена операций
	// каскадом сносит строки новой таблицы и не восстанавливает их, а миграция-копия второй раз не
	// выполняется. Без фолбэка карточки, отредактированные в этом окне, вернулись бы с пустыми
	// входами.
	//
	// Точность обеспечивает щит совместимости: карточка со сборочными фактами НЕ МОЖЕТ быть
	// записана старым кодом (FailedPrecondition). Значит операция, у которой есть строки в 0199 и
	// ноль строк в новой таблице, гарантированно piece-only — прочитать её из 0199 не догадка, а
	// факт. Снимается вместе с самой таблицей отдельной задачей Ф6, когда прод отстоит без отката.
	legacyRows, err := storeutil.QueryListNamed[struct {
		OpID     int    `db:"op_id"`
		PieceID  int    `db:"piece_id"`
		PieceKey string `db:"line_key"`
	}](ctx, s.DB, `
		SELECT o.id AS op_id, l.piece_id AS piece_id, p.line_key AS line_key
		FROM tech_card_operation_piece l
		JOIN tech_card_operation o ON o.id = l.operation_id
		JOIN tech_card_piece p ON p.id = l.piece_id
		WHERE o.tech_card_id IN (:ids)
		ORDER BY o.tech_card_id, o.display_order, l.display_order, l.id`,
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card operation piece links: %w", err)
	}
	for _, l := range legacyRows {
		if haveInputs[l.OpID] {
			continue
		}
		pos, ok := posByOpID[l.OpID]
		if !ok {
			continue
		}
		list := opsByCard[pos.cardID]
		op := &list[pos.index]
		op.AssemblyInputs = append(op.AssemblyInputs, entity.OperationInput{
			Kind: entity.AssemblyInputPiece, Key: l.PieceKey,
		})
		op.InputKeys = append(op.InputKeys, l.PieceKey)
		op.PieceIds = append(op.PieceIds, l.PieceID)
		op.PieceLineKeys = append(op.PieceLineKeys, l.PieceKey)
	}

	// Operation -> BOM-line links (0200), same keying as the piece links above.
	bomLinkRows, err := storeutil.QueryListNamed[struct {
		OpID      int    `db:"op_id"`
		BomItemID int    `db:"bom_item_id"`
		BomKey    string `db:"line_key"`
	}](ctx, s.DB, `
		SELECT o.id AS op_id, l.bom_item_id AS bom_item_id, b.line_key AS line_key
		FROM tech_card_operation_bom l
		JOIN tech_card_operation o ON o.id = l.operation_id
		JOIN tech_card_bom_item b ON b.id = l.bom_item_id
		WHERE o.tech_card_id IN (:ids)
		ORDER BY o.tech_card_id, o.display_order, l.display_order, l.id`,
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card operation bom links: %w", err)
	}
	for _, l := range bomLinkRows {
		pos, ok := posByOpID[l.OpID]
		if !ok {
			continue
		}
		list := opsByCard[pos.cardID]
		list[pos.index].BomIds = append(list[pos.index].BomIds, l.BomItemID)
		list[pos.index].BomLineKeys = append(list[pos.index].BomLineKeys, l.BomKey)
	}

	// Фотографии шагов с выносками (0308). Цепляются по operation_id — идентичности строки, а не
	// позиционному суррогату: причина та же, что у входов выше.
	mediaRows, err := storeutil.QueryListNamed[struct {
		OpID         int            `db:"op_id"`
		MediaID      int            `db:"media_id"`
		Caption      sql.NullString `db:"caption"`
		DisplayOrder int            `db:"display_order"`
		Annotations  []byte         `db:"annotations"`
	}](ctx, s.DB, `
		SELECT o.id AS op_id, m.media_id, m.caption, m.display_order, m.annotations
		FROM tech_card_operation_media m
		JOIN tech_card_operation o ON o.id = m.tech_card_operation_id
		WHERE o.tech_card_id IN (:ids)
		ORDER BY o.tech_card_id, o.display_order, m.display_order, m.id`,
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card operation media: %w", err)
	}
	for _, m := range mediaRows {
		pos, ok := posByOpID[m.OpID]
		if !ok {
			continue
		}
		var anns []entity.TechCardAnnotation
		if len(m.Annotations) > 0 {
			// Битый JSON в колонке — это испорченная строка, а не повод уронить чтение всей
			// карточки: картинка вернётся без выносок, и это видно, в отличие от пятисотки.
			if err := json.Unmarshal(m.Annotations, &anns); err != nil {
				slog.Default().Error("tech card operation media: broken annotations json",
					slog.Int("operation_id", m.OpID), slog.Int("media_id", m.MediaID),
					slog.String("err", err.Error()))
				anns = nil
			}
		}
		list := opsByCard[pos.cardID]
		list[pos.index].Media = append(list[pos.index].Media, entity.TechCardOperationMedia{
			MediaId:      m.MediaID,
			Caption:      m.Caption,
			DisplayOrder: m.DisplayOrder,
			Annotations:  anns,
		})
	}

	labelRows, err := storeutil.QueryListNamed[techCardLabelRow](ctx, s.DB, `
		SELECT tech_card_id, label_type, content, placement, attachment, size, note, bom_item_id
		FROM tech_card_label
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card labels: %w", err)
	}
	labelsByCard := make(map[int][]entity.TechCardLabel, len(ids))
	for _, r := range labelRows {
		labelsByCard[r.TechCardID] = append(labelsByCard[r.TechCardID], r.TechCardLabel)
	}

	// Explicit column list (not SELECT *): the deprecated kg columns weight_net/weight_gross may
	// still exist (dropped by 0129) but are no longer mapped, and a strict StructScan rejects
	// unmapped columns.
	pkgRows, err := storeutil.QueryListNamed[techCardPackagingRow](ctx, s.DB,
		`SELECT tech_card_id, folding_method, polybag, bag_sticker, inserts, units_per_box,
		        box_marking, box_dimensions, weight_net_grams, weight_gross_grams, notes
		 FROM tech_card_packaging WHERE tech_card_id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card packaging: %w", err)
	}
	pkgByCard := make(map[int]*entity.TechCardPackaging, len(pkgRows))
	for i := range pkgRows {
		p := pkgRows[i].TechCardPackaging
		pkgByCard[pkgRows[i].TechCardID] = &p
	}

	costRows, err := storeutil.QueryListNamed[techCardCostingRow](ctx, s.DB,
		`SELECT tech_card_id, cmt_cost, logistics_cost, overhead_cost, defect_percent, currency, notes,
		        target_margin_pct
		 FROM tech_card_costing WHERE tech_card_id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card costing: %w", err)
	}
	costByCard := make(map[int]*entity.TechCardCosting, len(costRows))
	for i := range costRows {
		c := costRows[i].TechCardCosting
		costByCard[costRows[i].TechCardID] = &c
	}

	issueRows, err := storeutil.QueryListNamed[techCardIssueRow](ctx, s.DB, `
		SELECT tech_card_id, operation_number, callout_number, raised_by, severity, status, description, resolution_note
		FROM tech_card_issue
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card issues: %w", err)
	}
	issuesByCard := make(map[int][]entity.TechCardIssue, len(ids))
	for _, r := range issueRows {
		issuesByCard[r.TechCardID] = append(issuesByCard[r.TechCardID], r.TechCardIssue)
	}

	signoffRows, err := storeutil.QueryListNamed[techCardSignoffRow](ctx, s.DB, `
		SELECT tech_card_id, section, state, signed_by, signed_at, note, signed_digest
		FROM tech_card_signoff
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card signoffs: %w", err)
	}
	signoffsByCard := make(map[int][]entity.TechCardSignoff, len(ids))
	for _, r := range signoffRows {
		signoffsByCard[r.TechCardID] = append(signoffsByCard[r.TechCardID], r.TechCardSignoff)
	}

	for i := range cards {
		id := cards[i].Id
		cards[i].Construction = consByCard[id]
		// The park hangs off the CARD, not off the construction row (its FK is on tech_card), so a
		// card can hold profiles with no construction row at all — 0306's migrated overlock profile
		// arrives on cards that never filled anything else in the section. An empty construction is
		// created for them rather than dropping the profiles, which is what «no row» would do.
		//
		// Left nil when there are no profiles: nil is «this card has no park» on the read side, and
		// only on the WRITE side does nil mean «the payload did not speak». The mapper turns either
		// into the same empty wrapper on the wire.
		if d := equipmentByCard[id]; d != nil {
			if cards[i].Construction == nil {
				cards[i].Construction = &entity.TechCardConstruction{}
			}
			cards[i].Construction.EquipmentDefaults = d
		}
		cards[i].Operations = opsByCard[id]
		cards[i].Labels = labelsByCard[id]
		cards[i].Packaging = pkgByCard[id]
		cards[i].Costing = costByCard[id]
		cards[i].Issues = issuesByCard[id]
		cards[i].Signoffs = signoffsByCard[id]
	}
	return nil
}
