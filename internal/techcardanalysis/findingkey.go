package techcardanalysis

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ── «SEEN» ДЛЯ НАХОДОК (T9) ─────────────────────────────────────────────────────────────────────
//
// Находки не хранятся: аудит пересчитывается на каждом открытии вкладки. Чтобы технолог мог
// пометить находку «просмотрено» (не всякую надо чинить), у неё должна быть ИДЕНТИЧНОСТЬ, которая
// переживает пересчёт, и ОТПЕЧАТОК текста, который он видел:
//
//	key         = code "@" join(",", sort(dedupe(normalize(refs)))) ["#" subject]
//	fingerprint = hex(sha256(mask(title) 0x00 mask(detail) 0x00 fp8_1 fp8_2 …))[:16]   (fp8 — отпечатки шагов из refs, отсортированы)
//
// Отметка совпадает, пока совпадает отпечаток. Поменялся текст или форма шага — находка снова
// «не просмотрена», строка отметки остаётся и перезапишется следующей отметкой.
//
// ЯКОРЬ ШАГА В КЛЮЧЕ. "op:<n>" — номер позиционный, сервер пере-штампует его на каждом сохранении,
// а стабильного id у шага нет (tech_card_operation переписывается целиком, DELETE + INSERT). Поэтому
// ВНУТРИ КЛЮЧА "op:<n>" ВСЕГДА заменяется на "opfp:<fp8>.<k>" — отпечаток формы шага (fingerprint.go)
// и порядковый номер шага среди шагов карточки с той же формой (1, 2, … по номеру операции). Вид
// токена не зависит от ДРУГИХ шагов: дописать в конец шаг той же формы не трогает ключи прежних.
// Производители находок по-прежнему пишут RefOp: нормализация живёт здесь.
//
// НОМЕР ШАГА В ТЕКСТЕ НЕ ВХОДИТ В ОТПЕЧАТОК. Текст говорит «Operation 210»; перенумерация — не
// изменение находки. mask() заменяет на "#" каждое целое слово-число, равное номеру шага из refs
// находки (MaskOpNumbers, общий с клиентом контракт), а сервер вдобавок — номера в собственной прозе
// «Operation N» / «ops N, M» (maskOpProse: шаги, названные в тексте, но не в refs).
//
// Ключ, Отпечаток, маску и сетку коллизий клиент повторяет байт в байт
// (admin: tech-card/components/finding-key.ts); TestFindingKeyGoldenVectors — общие векторы.

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

// serverCodes is every Code* above — the kinds the SERVER computes. A mark whose key carries one of
// them travels inline on its finding (seen / seen_by / seen_at). Any other code is a CLIENT finding:
// computed in the browser (the PDF gaps, pdf_*; the assembly-schema checks), which the server cannot
// recompute, so its mark is handed back bare (ClientSeenMarks) and matched by the client.
// TestServerCodesListEveryCode keeps this set equal to the const table.
var serverCodes = map[string]bool{
	CodeUnitKeyCaseCollision:          true,
	CodeButtonholeUnspecified:         true,
	CodePlacementCountMissing:         true,
	CodeTrimActionMissing:             true,
	CodeAttachMethodMissing:           true,
	CodeFusingParamsMissing:           true,
	CodePressParamsMissing:            true,
	CodeSeamClassMachineMismatch:      true,
	CodeNoteOnlyInstruction:           true,
	CodeWorkAfterPack:                 true,
	CodeFoldAfterPack:                 true,
	CodeFusingOnAssembledUnit:         true,
	CodeWorkVerbMismatch:              true,
	CodeWorkMachineMismatch:           true,
	CodeWorkCatalogMismatch:           true,
	CodeLegacyPieceLinkDrift:          true,
	CodeSensitiveBeforeWet:            true,
	CodeHardwareNotInBom:              true,
	CodeHardwareNotSet:                true,
	CodeThreadLinesMissing:            true,
	CodeFusingTriangleMismatch:        true,
	CodeButtonholeButtonCountMismatch: true,
	CodeHardwareSetExceedsBom:         true,
	CodePlaceholderPrice:              true,
	CodePurposePriceInversion:         true,
	CodeCurrencyWithoutRate:           true,
	CodeCmtNotSet:                     true,
	CodeCmtWithoutSmv:                 true,
	CodeWastageNotStated:              true,
	CodeWastageHigh:                   true,
	CodeNoOperations:                  true,
	CodeNoCutPieces:                   true,
	CodeNoSizeRange:                   true,
	CodePrintPacketGaps:               true,
	CodeLabelNotInBom:                 true,
	CodeLabelLinesUndescribed:         true,
	CodeLabelMockupMissing:            true,
	CodePackagingMockupMissing:        true,
	CodeEquipmentProfilesMissing:      true,
	CodeEquipmentProfileRefBroken:     true,
	CodeEquipmentProfileAmbiguous:     true,
	CodeTechnicalSketchMissing:        true,
	CodeAssemblyNotConverging:         true,
	CodePieceNeverSewn:                true,
	CodeSmvMissing:                    true,
	CodeWorkMissing:                   true,
	CodeFinishingBlockMissing:         true,
	CodeNotReleaseReady:               true,
}

// IsServerCode reports whether code is a kind the server audit computes.
func IsServerCode(code string) bool { return serverCodes[code] }

// ClientCodePrefixes are the namespaces of CLIENT findings: pdf_* (the PDF gaps), asm_* (the
// assembly-schema checks). A mark on any other non-server code is refused — a bounded namespace is
// what keeps an arbitrary writer from filling the table with kinds nobody reads.
var ClientCodePrefixes = []string{"pdf_", "asm_"}

// findingCodeRe is the shape of every finding code: snake_case.
var findingCodeRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// IsClientCode reports whether code may name a client finding: snake_case, in a client namespace,
// and never a server code.
func IsClientCode(code string) bool {
	if !findingCodeRe.MatchString(code) || IsServerCode(code) {
		return false
	}
	for _, p := range ClientCodePrefixes {
		if strings.HasPrefix(code, p) {
			return true
		}
	}
	return false
}

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
	keys := make([]string, len(findings))
	idents := make([]string, len(findings))
	for i := range findings {
		f := &findings[i]
		if f.Code == "" {
			f.Key, f.Fingerprint = "", ""
			continue
		}
		// The server's own prose names steps the refs may not (the pack step, a capped list): those
		// numbers leave the text before anything is hashed, like the refs' own.
		title, detail := maskOpProse(f.Title, opFingerprints), maskOpProse(f.Detail, opFingerprints)
		keys[i] = FindingKey(f.Code, f.Refs, f.Subject, tokens)
		f.Fingerprint = FindingFingerprint(title, detail, f.Refs, opFingerprints)
		idents[i] = findingIdentity(f.Code, f.Subject, title, f.Refs, tokens)
	}
	for i, k := range disambiguateKeys(keys, idents) {
		findings[i].Key = k
	}
}

// OpKeyTokens maps operation_number → the token that stands for that step inside a finding key:
// ALWAYS "opfp:<fp8>.<k>", k the 1-based ordinal of the step among the card's steps with the same
// fp8, in operation-number order. A unique shape is ".1" from the start, so adding a later step of
// the same shape never moves an earlier step's key. A step missing from the map keeps its "op:<n>".
func OpKeyTokens(opFingerprints map[int32]string) map[int32]string {
	nums := make([]int32, 0, len(opFingerprints))
	for n, fp := range opFingerprints {
		if fp != "" {
			nums = append(nums, n)
		}
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
	ordinal := make(map[string]int, len(nums))
	out := make(map[int32]string, len(nums))
	for _, n := range nums {
		fp := opFingerprints[n]
		ordinal[fp]++
		out[n] = refOpFpPrefix + fp + "." + strconv.Itoa(ordinal[fp])
	}
	return out
}

// normalizeRefs is the ref list a key is built from: steps replaced by their token, deduplicated
// (a finding that names a step twice is still one finding about that step), sorted by bytes.
func normalizeRefs(refs []string, opTokens map[int32]string) []string {
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
	return norm
}

// FindingKey builds code "@" sorted(normalized refs) ["#" subject].
func FindingKey(code string, refs []string, subject string, opTokens map[int32]string) string {
	key := code + "@" + strings.Join(normalizeRefs(refs, opTokens), ",")
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

// FindingFingerprint is hex(sha256(mask(title) 0x00 mask(detail) 0x00 fp8…))[:16], fp8 of every step
// the refs name that the card still has, sorted; mask = MaskOpNumbers with the same refs. fp8 is
// fixed-width hex, so the concatenation is unambiguous without a separator.
func FindingFingerprint(title, detail string, refs []string, opFingerprints map[int32]string) string {
	title, detail = MaskOpNumbers(title, refs), MaskOpNumbers(detail, refs)
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

// opNumberMask is what a step number reads as in the fingerprinted text.
const opNumberMask = "#"

// MaskOpNumbers replaces with "#" every whole-word number of text that equals the number of a step
// the refs name ("op:<n>", written canonically: "op:010" names 10). A number is a maximal run of
// ASCII digits, with "." or "," joining further digit runs — "12.5" and "1,200" are one number and
// never match a step; whole-word means no ASCII letter, digit or "_" right before or after it.
// "Operation 210 runs…" with refs [op:210] → "Operation # runs…": renumbering is not a change.
func MaskOpNumbers(text string, refs []string) string {
	nums := map[string]bool{}
	for _, r := range refs {
		if n, ok := parseOpRef(r); ok {
			nums[strconv.Itoa(int(n))] = true
		}
	}
	if len(nums) == 0 {
		return text
	}
	return maskNumbers(text, func(num string) bool { return nums[num] })
}

// opProseRe is how the server's own prose names steps: opLabel «Operation N», opList «op N» /
// «ops N, M», in any case.
var opProseRe = regexp.MustCompile(`(?i)\b(?:operations?|ops?) \d+(?:, \d+)*`)

// maskOpProse masks the step numbers the server's prose renders even when the refs do not name the
// step (the pack step of work_after_pack, a list capped to three refs). SERVER ONLY: the client
// never recomputes a server finding, and its own findings name their steps in refs.
func maskOpProse(text string, cardOps map[int32]string) string {
	if len(cardOps) == 0 {
		return text
	}
	return opProseRe.ReplaceAllStringFunc(text, func(m string) string {
		return maskNumbers(m, func(num string) bool {
			n, err := strconv.ParseInt(num, 10, 32)
			if err != nil || strconv.Itoa(int(n)) != num {
				return false
			}
			_, ok := cardOps[int32(n)]
			return ok
		})
	})
}

// maskNumbers replaces each whole-word number of text (see MaskOpNumbers) for which hit is true.
// Byte-wise: only ASCII is ever a digit or a word byte, so a UTF-8 letter is a boundary, as it is
// for the client's UTF-16 scan.
func maskNumbers(text string, hit func(string) bool) string {
	var b strings.Builder
	last, i := 0, 0
	for i < len(text) {
		if !isASCIIDigit(text[i]) {
			i++
			continue
		}
		start := i
		for i < len(text) && isASCIIDigit(text[i]) {
			i++
		}
		for i+1 < len(text) && (text[i] == '.' || text[i] == ',') && isASCIIDigit(text[i+1]) {
			i++
			for i < len(text) && isASCIIDigit(text[i]) {
				i++
			}
		}
		if start > 0 && isASCIIWord(text[start-1]) || i < len(text) && isASCIIWord(text[i]) {
			continue
		}
		if hit(text[start:i]) {
			b.WriteString(text[last:start])
			b.WriteString(opNumberMask)
			last = i
		}
	}
	if last == 0 {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

func isASCIIWord(c byte) bool {
	return isASCIIDigit(c) || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

// findingIdentity is what the collision net hashes: hex(sha256("fk1" 0x00 ‖ code ‖ subject ‖
// masked title ‖ each normalized ref))[:16], every field written as <byte length>":"<bytes>. The
// length prefixes make it injective — two different tuples never share a payload, whatever bytes a
// ref carries — and the masked title keeps it still under renumbering.
func findingIdentity(code, subject, title string, refs []string, opTokens map[int32]string) string {
	h := sha256.New()
	h.Write([]byte("fk1\x00"))
	field := func(s string) {
		h.Write([]byte(strconv.Itoa(len(s)) + ":"))
		h.Write([]byte(s))
	}
	field(code)
	field(subject)
	field(MaskOpNumbers(title, refs))
	for _, r := range normalizeRefs(refs, opTokens) {
		field(r)
	}
	return hex.EncodeToString(h.Sum(nil))[:FindingFingerprintLen]
}

// disambiguateKeys is the safety net under Subject. Keys held by one finding stay as they are and
// are reserved first; each finding of a shared key gets "~<identity16>", and if that is taken — two
// findings identical in everything, or a ref that happens to spell an earlier key's suffix — a
// running ".<n>" from 2, in list order, until it is free. The result is unique over the whole
// list. A check that hits this routinely should set Subject — TestFindingKeysAreUniqueOnGoldenCard.
func disambiguateKeys(keys, idents []string) []string {
	count := make(map[string]int, len(keys))
	for _, k := range keys {
		if k != "" {
			count[k]++
		}
	}
	taken := make(map[string]bool, len(keys))
	for _, k := range keys {
		if count[k] == 1 {
			taken[k] = true
		}
	}
	out := make([]string, len(keys))
	copy(out, keys)
	for i, k := range keys {
		if k == "" || count[k] == 1 {
			continue
		}
		base := k + "~" + idents[i]
		final := base
		for n := 2; taken[final]; n++ {
			final = base + "." + strconv.Itoa(n)
		}
		taken[final] = true
		out[i] = final
	}
	return out
}

// findingKeyCodeRe is the head every finding key starts with: a snake_case code and "@".
var findingKeyCodeRe = regexp.MustCompile(`^([a-z][a-z0-9_]*)@`)

// KeyCode returns the code a finding key starts with, "" when the key has no code@ head.
func KeyCode(key string) string {
	if m := findingKeyCodeRe.FindStringSubmatch(key); m != nil {
		return m[1]
	}
	return ""
}

// ValidFindingKey is the SHAPE check SetTechCardFindingSeen makes on a key: a code@ head, valid
// UTF-8, at most MaxFindingKeyBytes. It does NOT ask whether the server can produce that finding — a
// client finding (pdf_*, asm_*) is marked through the same call, and the match against the current
// text is the client's, by key + fingerprint. KnownFindingCode is the namespace half.
func ValidFindingKey(key string) bool {
	return len(key) <= MaxFindingKeyBytes && utf8.ValidString(key) && KeyCode(key) != ""
}

// KnownFindingCode reports whether a key's code may carry a mark: a server code, or a client one
// (IsClientCode). A server-code key the server would never produce is accepted and harmless: it
// matches no finding (ApplySeen) and is never handed back (ClientSeenMarks).
func KnownFindingCode(code string) bool { return IsServerCode(code) || IsClientCode(code) }

// ClientSeenMarks keeps the marks the server does not resolve itself: those of a client code, at
// most entity.TechCardFindingSeenMaxPerCard, in the store's (key) order. A server-code mark either
// rides on its finding already or names a finding this run no longer produces — and for a reader
// without costing access it may name a withheld money finding, so it is never handed out bare.
func ClientSeenMarks(marks []entity.TechCardFindingSeen) []entity.TechCardFindingSeen {
	out := make([]entity.TechCardFindingSeen, 0, min(len(marks), entity.TechCardFindingSeenMaxPerCard))
	for _, m := range marks {
		if len(out) == entity.TechCardFindingSeenMaxPerCard {
			break
		}
		if IsClientCode(KeyCode(m.FindingKey)) {
			out = append(out, m)
		}
	}
	return out
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
