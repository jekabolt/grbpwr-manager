package techcardanalysis

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ── ОТПЕЧАТОК ШАГА (design §9) ──────────────────────────────────────────────────────────────────
//
// Якорь находки — `operation_number`, действующая якорная валюта всей системы. Его слабость в том,
// что сервер пере-штампует номера позиционно на КАЖДОМ сохранении: пока человек читает находки,
// чужое сохранение может передвинуть #460 на другой шаг, и переход по якорю приведёт не туда.
//
// Отпечаток закрывает ровно эту дырку и ничего больше: он считается по СЫРЫМ ХРАНИМЫМ КЛЮЧАМ шага
// — тем же, которыми живут все реальные пути данных, — поэтому «шаг изменился с момента прогона»
// становится вопросом сравнения восьми hex-символов, а не догадки.
//
// КАНОНИЧЕСКАЯ СЕРИАЛИЗАЦИЯ, ОДНА НА Go И TS:
//
//	fp(op) = hex(sha256(payload))[0:8]
//	payload = "tcfp1" 0x00 output_unit_key 0x00 k_1 0x00 k_2 … 0x00 k_n   (байты UTF-8)
//
// где k_i — сырой ключ i-го входа В DISPLAY_ORDER: для детали tech_card_piece.line_key (CHAR(26)
// ULID), для узла unit_key байт-в-байт.
//
// ЧЕГО ЗДЕСЬ НЕТ И БЫТЬ НЕ МОЖЕТ: сортировки, тримминга, приведения регистра, префикса вида входа.
// Порядок входов — факт шага (перестановка входов меняет шаг), пробел в ключе — часть ключа, а
// регистр различает узлы («Base» и «base» — два разных узла, на этом стоит проверка A1). Префикс
// вида не нужен: правило 6 движка сборки гарантирует, что ключ узла не совпадает с line_key детали
// — пространство имён едино, и коллизия в сохранённой карточке невозможна.
//
// TS-порт (T21) сверяется с ТЕМИ ЖЕ векторами, что зафиксированы в fingerprint_test.go. Любая
// «нормализация», добавленная здесь из лучших побуждений, разойдётся с клиентом молча и даст
// ложный амбер «эта операция изменилась» на каждой карточке беты.

// fingerprintPrefix is the version tag of the payload. Меняется только вместе с формой payload — и
// тогда старые отпечатки перестают совпадать НАМЕРЕННО, а не по недосмотру.
const fingerprintPrefix = "tcfp1"

// fingerprintSep is the field separator: NUL. Ключи узлов — свободный текст технолога, и NUL
// единственный байт, которого в нём не бывает; любой печатный разделитель («|», ":") дал бы две
// разные пары ключей с одним payload.
const fingerprintSep = "\x00"

// OperationFingerprint computes the fp8 of ONE step from its output unit key and its raw input keys
// in display order. Пустой выход (ОБРАБОТКА) сериализуется пустой строкой.
//
// Это единственное место, где payload собирается; Fingerprints ниже только раскладывает карточку
// на аргументы этой функции.
func OperationFingerprint(outputUnitKey string, inputKeys []string) string {
	parts := make([]string, 0, len(inputKeys)+2)
	parts = append(parts, fingerprintPrefix, outputUnitKey)
	parts = append(parts, inputKeys...)
	sum := sha256.Sum256([]byte(strings.Join(parts, fingerprintSep)))
	return hex.EncodeToString(sum[:])[:8]
}

// Fingerprints maps operation_number → fp8 for every numbered operation of the card (design §9).
//
// Шаг без номера пропускается: у него нет якоря, по которому клиент мог бы этот отпечаток
// спросить, и положить его в карту было бы некуда — ключ карты и есть номер. На сохранённой
// карточке таких шагов не бывает (номера штампует запись), но легаси-строки старше фичи существуют
// как класс, и падать на них нечем.
func Fingerprints(card *entity.TechCard) map[int32]string {
	if card == nil || len(card.Operations) == 0 {
		return map[int32]string{}
	}
	out := make(map[int32]string, len(card.Operations))
	for i := range card.Operations {
		op := &card.Operations[i]
		if !op.OperationNumber.Valid {
			continue
		}
		out[op.OperationNumber.Int32] = OperationFingerprint(op.OutputUnitKey.String, inputKeysOf(op))
	}
	return out
}

// inputKeysOf reads the step's inputs from AssemblyInputs — the CANONICAL form (entity docs): the
// hydration fills it in display order and everything downstream reads it instead of re-guessing a
// key's nature. InputKeys is the same list in raw form and is NOT persisted; falling back to it
// would mean hashing one thing on the write path and another on the read path.
func inputKeysOf(op *entity.TechCardOperation) []string {
	if len(op.AssemblyInputs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(op.AssemblyInputs))
	for _, in := range op.AssemblyInputs {
		keys = append(keys, in.Key)
	}
	return keys
}
