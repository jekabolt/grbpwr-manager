package techcardanalysis

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ── «SEEN» ДЛЯ НАХОДОК (T9) ─────────────────────────────────────────────────────────────────────
//
// Находки не хранятся: аудит пересчитывается на каждом открытии вкладки. Чтобы технолог мог
// пометить находку «просмотрено» (не всякую надо чинить), у неё должна быть ИДЕНТИЧНОСТЬ, которая
// переживает пересчёт, и ОТПЕЧАТОК текста, который он видел:
//
//	key         = code "@" join(",", sort(dedupe(normalize(refs)))) ["#" subject]
//	fingerprint = hex(sha256(title 0x00 detail 0x00 fp8_1 fp8_2 …))[:16]   (fp8 — отпечатки шагов из refs, отсортированы)
//
// Отметка совпадает, пока совпадает отпечаток. Поменялся текст или форма шага — находка снова
// «не просмотрена», строка отметки остаётся и перезапишется следующей отметкой.
//
// ЯКОРЬ ШАГА В КЛЮЧЕ. "op:<n>" — номер позиционный, сервер пере-штампует его на каждом сохранении,
// а стабильного id у шага нет (tech_card_operation переписывается целиком, DELETE + INSERT). Поэтому
// ВНУТРИ КЛЮЧА "op:<n>" заменяется на "opfp:<fp8>" — отпечаток формы шага (fingerprint.go), если на
// карточке он у этого шага единственный; иначе остаётся "op:<n>" (две одинаковые формы различимы
// только номером). Производители находок по-прежнему пишут RefOp: нормализация живёт здесь.

// Codes — стабильный вид находки, ОДИН на правило. Пер-шаговая и агрегированная находка одного
// правила делят код. Значения — часть ключа отметки «seen» в БД: переименование кода молча
// «разотмечает» все находки этого вида, поэтому коды не переименовываются.
//
// Зарезервированы за assembly-schema (T8b) и здесь НЕ используются: edge_missing, partner_missing,
// step_no_pieces, unit_unused, length_mismatch, piece_no_code, inner_target_missing.
const (
	// route.go
	CodeUnitKeyCaseCollision     = "unit_key_case_collision"     // A1
	CodeButtonholeUnspecified    = "buttonhole_unspecified"      // A2.1
	CodePlacementCountMissing    = "placement_count_missing"     // A2.2
	CodeTrimActionMissing        = "trim_action_missing"         // A2.3
	CodeAttachMethodMissing      = "attach_method_missing"       // A2.4
	CodeFusingParamsMissing      = "fusing_params_missing"       // A3 fusing
	CodePressParamsMissing       = "press_params_missing"        // A3 pressing
	CodeSeamClassMachineMismatch = "seam_class_machine_mismatch" // A4
	CodeNoteOnlyInstruction      = "note_only_instruction"       // A5 (Subject = field)
	CodeWorkAfterPack            = "work_after_pack"             // A6 assembly or wet work
	CodeFoldAfterPack            = "fold_after_pack"             // A6 folding
	CodeFusingOnAssembledUnit    = "fusing_on_assembled_unit"    // A7
	CodeWorkVerbMismatch         = "work_verb_mismatch"          // A8 per step
	CodeWorkMachineMismatch      = "work_machine_mismatch"       // A8 per step
	CodeWorkCatalogMismatch      = "work_catalog_mismatch"       // A8 aggregate of both
	CodeLegacyPieceLinkDrift     = "legacy_piece_link_drift"     // A9
	CodeSensitiveBeforeWet       = "sensitive_before_wet"        // A10

	// bom.go
	CodeHardwareNotInBom              = "hardware_not_in_bom"              // B1
	CodeHardwareNotSet                = "hardware_not_set"                 // B1
	CodeThreadLinesMissing            = "thread_lines_missing"             // B2
	CodeFusingTriangleMismatch        = "fusing_triangle_mismatch"         // B3
	CodeButtonholeButtonCountMismatch = "buttonhole_button_count_mismatch" // B4
	CodeHardwareSetExceedsBom         = "hardware_set_exceeds_bom"         // B4
	CodePlaceholderPrice              = "placeholder_price"                // B5a
	CodePurposePriceInversion         = "purpose_price_inversion"          // B5b
	CodeCurrencyWithoutRate           = "currency_without_rate"            // B5c
	CodeCmtNotSet                     = "cmt_not_set"                      // B6
	CodeCmtWithoutSmv                 = "cmt_without_smv"                  // B7
	CodeWastageNotStated              = "wastage_not_stated"               // B8
	CodeWastageHigh                   = "wastage_high"                     // B8

	// readiness.go
	CodeNoOperations              = "no_operations"                // C1
	CodeNoCutPieces               = "no_cut_pieces"                // C1
	CodeNoSizeRange               = "no_size_range"                // C1
	CodePrintPacketGaps           = "print_packet_gaps"            // C2
	CodeLabelNotInBom             = "label_not_in_bom"             // C3 (Subject = label key)
	CodeLabelLinesUndescribed     = "label_lines_undescribed"      // C3
	CodeLabelMockupMissing        = "label_mockup_missing"         // C3b
	CodePackagingMockupMissing    = "packaging_mockup_missing"     // C3b
	CodeEquipmentProfilesMissing  = "equipment_profiles_missing"   // C4
	CodeEquipmentProfileRefBroken = "equipment_profile_ref_broken" // C4 (Subject = column)
	CodeEquipmentProfileAmbiguous = "equipment_profile_ambiguous"  // C4
	CodeTechnicalSketchMissing    = "technical_sketch_missing"     // C5
	CodeAssemblyNotConverging     = "assembly_not_converging"      // C6
	CodePieceNeverSewn            = "piece_never_sewn"             // C6
	CodeSmvMissing                = "smv_missing"                  // C7
	CodeWorkMissing               = "work_missing"                 // C8
	CodeFinishingBlockMissing     = "finishing_block_missing"      // C9

	// findings.go — the draft's collapsed readiness finding. One finding for seen; its fingerprint
	// moves with its list of clauses, which is intended.
	CodeNotReleaseReady = "not_release_ready"
)

const (
	// refOpFpPrefix stands for a step INSIDE a finding key only — never in Refs, never on screen.
	refOpFpPrefix = "opfp:"
	// MaxFindingKeyBytes is the width of tech_card_finding_seen.finding_key. A longer key folds to a
	// hash (findingKeyBody) so the column never truncates one key into another.
	MaxFindingKeyBytes = 512
	// findingKeyBodyBytes leaves room for the collision suffix of disambiguateKeys.
	findingKeyBodyBytes = MaxFindingKeyBytes - 32
	// FindingFingerprintLen is the length of Finding.Fingerprint (hex).
	FindingFingerprintLen = 16
)

// AssignKeys fills Key and Fingerprint on every finding that carries a Code, from the card's step
// fingerprints (operation_number → fp8, Fingerprints). Findings without a Code (model findings) are
// left without a key: they cannot be marked seen on the server.
func AssignKeys(findings []Finding, opFingerprints map[int32]string) {
	tokens := OpKeyTokens(opFingerprints)
	for i := range findings {
		f := &findings[i]
		if f.Code == "" {
			f.Key, f.Fingerprint = "", ""
			continue
		}
		f.Key = FindingKey(f.Code, f.Refs, f.Subject, tokens)
		f.Fingerprint = FindingFingerprint(f.Title, f.Detail, f.Refs, opFingerprints)
	}
	disambiguateKeys(findings)
}

// OpKeyTokens maps operation_number → the token that stands for that step inside a finding key:
// "opfp:<fp8>" when no other step of the card has the same fp8. A step missing from the map keeps
// its "op:<n>".
func OpKeyTokens(opFingerprints map[int32]string) map[int32]string {
	count := make(map[string]int, len(opFingerprints))
	for _, fp := range opFingerprints {
		count[fp]++
	}
	out := make(map[int32]string, len(opFingerprints))
	for n, fp := range opFingerprints {
		if fp != "" && count[fp] == 1 {
			out[n] = refOpFpPrefix + fp
		}
	}
	return out
}

// FindingKey builds code "@" sorted(normalized refs) ["#" subject]. Refs are deduplicated: a
// finding that names a step twice is still one finding about that step.
func FindingKey(code string, refs []string, subject string, opTokens map[int32]string) string {
	norm := make([]string, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	for _, r := range refs {
		if n, ok := parseOpRef(r); ok {
			if tok, ok := opTokens[n]; ok {
				r = tok
			}
		}
		if seen[r] {
			continue
		}
		seen[r] = true
		norm = append(norm, r)
	}
	sort.Strings(norm)
	key := code + "@" + strings.Join(norm, ",")
	if subject != "" {
		key += "#" + subject
	}
	return findingKeyBody(code, key)
}

// findingKeyBody folds an over-long key into code + a hash of the whole key: still deterministic,
// still unique in practice, and never cut mid-character by the column.
func findingKeyBody(code, key string) string {
	if len(key) <= findingKeyBodyBytes {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return code + "@sha256:" + hex.EncodeToString(sum[:])[:32]
}

// FindingFingerprint is hex(sha256(title 0x00 detail 0x00 fp8…))[:16], fp8 of every step the refs
// name that the card still has, sorted. fp8 is fixed-width hex, so the concatenation is
// unambiguous without a separator.
func FindingFingerprint(title, detail string, refs []string, opFingerprints map[int32]string) string {
	fps := make([]string, 0, len(refs))
	seen := make(map[int32]bool, len(refs))
	for _, r := range refs {
		n, ok := parseOpRef(r)
		if !ok || seen[n] {
			continue
		}
		seen[n] = true
		if fp, ok := opFingerprints[n]; ok {
			fps = append(fps, fp)
		}
	}
	sort.Strings(fps)
	h := sha256.New()
	h.Write([]byte(title))
	h.Write([]byte{0})
	h.Write([]byte(detail))
	h.Write([]byte{0})
	for _, fp := range fps {
		h.Write([]byte(fp))
	}
	return hex.EncodeToString(h.Sum(nil))[:FindingFingerprintLen]
}

// disambiguateKeys is the safety net under Subject: two findings that still share a key get
// "~<hash8(title)>", and if even the titles agree, a running "~<hash8>.<n>" in list order. A check
// that hits this routinely should set Subject instead — TestFindingKeysAreUniqueOnGoldenCard pins it.
func disambiguateKeys(findings []Finding) {
	groups := map[string][]int{}
	for i := range findings {
		if findings[i].Key != "" {
			groups[findings[i].Key] = append(groups[findings[i].Key], i)
		}
	}
	for _, idx := range groups {
		if len(idx) < 2 {
			continue
		}
		used := map[string]int{}
		for _, i := range idx {
			sum := sha256.Sum256([]byte(findings[i].Title))
			k := findings[i].Key + "~" + hex.EncodeToString(sum[:])[:8]
			used[k]++
			if n := used[k]; n > 1 {
				k += "." + strconv.Itoa(n)
			}
			findings[i].Key = k
		}
	}
}

// parseOpRef reads the number out of an "op:<n>" sigil.
func parseOpRef(ref string) (int32, bool) {
	if !strings.HasPrefix(ref, refOpPrefix) {
		return 0, false
	}
	n, err := strconv.ParseInt(ref[len(refOpPrefix):], 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(n), true
}

// ApplySeen marks the findings whose stored mark still matches their fingerprint and returns how
// many stay unseen. A mark whose fingerprint no longer matches leaves the finding unseen — the text
// the person saw is not the text on screen now.
func ApplySeen(findings []Finding, marks []entity.TechCardFindingSeen) int {
	byKey := make(map[string]*entity.TechCardFindingSeen, len(marks))
	for i := range marks {
		byKey[marks[i].FindingKey] = &marks[i]
	}
	unseen := 0
	for i := range findings {
		f := &findings[i]
		f.Seen, f.SeenBy, f.SeenAt = false, "", time.Time{}
		if m := byKey[f.Key]; f.Key != "" && m != nil && m.Fingerprint == f.Fingerprint {
			f.Seen, f.SeenBy, f.SeenAt = true, m.SeenBy, m.SeenAt
			continue
		}
		unseen++
	}
	return unseen
}
