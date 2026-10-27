package dto

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ИКОННЫЕ ПОЛЯ ШАГА (0436, SPEC-B1): seam_kind и work_variant. Третье поле волны —
// fastening.button_holes — живёт в блоке FA (techcard_operation_kinds.go), рядом с attach_pattern,
// с которым у него общее правило.
//
// ВСЕ ТРИ РАЗБИРАЮТСЯ ВСЕГДА, независимо от operation_seam_kind_aware (129): флаг объявляет
// способность бандла, а не выключает разбор (тот же довод, что у work и 116) — иначе серверный
// round-trip (клон сезона строит payload сам) молча вернул бы карточку без значений. Отказ
// неосведомлённой записи — забота щита в apisrv/admin, а не разбора.
//
// СЕРВЕР НИЧЕГО НЕ ВЫВОДИТ И НЕ ДОПИСЫВАЕТ: пусто = не указано = NULL, и это законное состояние
// каждой сегодняшней строки. Правила ниже стоят ноль для шага без новых полей.

// parseOperationSeamKind — правила 1 и 2 конструкции шва. Правило 3 (окантовка ↔ binding_style)
// проверяет checkSeamKindBinding ПОСЛЕ разбора блока строчки: binding_style разбирается там.
//
// Идёт ДО полей видов, чтобы противоречие «вид окантовки, класс другой» назвалось по своему полю
// (`seam_class`, needs_seam_class), а не превратилось в F22 на соседнем binding_style.
func parseOperationSeamKind(o *pb_common.TechCardOperation, seamClass sql.NullString, step string) (sql.NullString, error) {
	kind := strings.TrimSpace(o.GetSeamKind())
	if kind == "" {
		return sql.NullString{}, nil
	}
	// Правило 1. Незнакомый токен — отказ. Отдельного гейта по глаголу нет: у seam_class его нет
	// тоже, а вид законен ровно там, где законен его класс (правило 2 этого и требует).
	if !entity.ValidSeamKinds[kind] {
		return sql.NullString{}, entity.NewFieldViolation(step+".seam_kind", "unknown_value", kind,
			"pick a seam construction from the list — this server does not know that one, so the admin panel is newer than the server")
	}
	// Правило 2. Класс ЯВНЫЙ и равный классу вида. Наследование умолчания карточки не засчитывается:
	// умолчание может виду противоречить, и шаг сказал бы два разных шва одновременно.
	want := string(entity.SeamKindClass[kind])
	if !seamClass.Valid || seamClass.String != want {
		said := "not set (inherits the card default)"
		if seamClass.Valid {
			said = fmt.Sprintf("%q", seamClass.String)
		}
		return sql.NullString{}, entity.NewFieldViolation(step+".seam_class", "needs_seam_class", seamClass.String,
			fmt.Sprintf("the seam construction %q belongs to the seam class %q, and this step's seam class is %s — set the class to %q, or clear the construction",
				kind, want, said, want))
	}
	return sql.NullString{String: kind, Valid: true}, nil
}

// checkSeamKindBinding — правило 3: вид окантовки задаёт сложение бейки целиком. bound_tape /
// bound_open / bound_closed требуют binding_style raw / single_fold / double_fold, bound_frame —
// пустой binding_style. Вне класса bs_bound правила нет: binding_style там уже отвергает F22.
func checkSeamKindBinding(kind, bindingStyle sql.NullString, step string) error {
	if !kind.Valid {
		return nil
	}
	want, bound := entity.SeamKindBindingStyle[kind.String]
	if !bound {
		return nil
	}
	got := ""
	if bindingStyle.Valid {
		got = bindingStyle.String
	}
	if got == string(want) {
		return nil
	}
	if want == "" {
		return entity.NewFieldViolation(step+".binding_style", "contradicts_seam_kind", got,
			fmt.Sprintf("the seam construction %q is a frame binding, which no fold value describes — clear the binding style, or pick the construction that matches the fold", kind.String))
	}
	return entity.NewFieldViolation(step+".binding_style", "contradicts_seam_kind", got,
		fmt.Sprintf("the seam construction %q means the binding is folded %q — set the binding style to %q (it lives on a machine step), or pick the construction that matches the fold",
			kind.String, string(want), string(want)))
}

// parseOperationWorkVariant — вариант исполнения внутри работы. Вариант без работы — отказ (его не
// к чему отнести); вариант, которого у этой работы нет, — отказ not_for_work. Вариант никогда не
// меняет глагол: другое действие оператора — это другая работа, поэтому проверки глагола здесь нет —
// её уже сделал parseOperationWork для самой работы.
func parseOperationWorkVariant(o *pb_common.TechCardOperation, work sql.NullString, step string) (sql.NullString, error) {
	variant := strings.TrimSpace(o.GetWorkVariant())
	if variant == "" {
		return sql.NullString{}, nil
	}
	if !work.Valid {
		return sql.NullString{}, entity.NewFieldViolation(step+".work_variant", "needs_work", variant,
			"a variant says HOW a work is done — pick the work first, or clear the variant")
	}
	if !entity.IsWorkVariant(work.String, variant) {
		allowed := append([]string(nil), entity.WorkVariants[work.String]...)
		sort.Strings(allowed)
		hint := "this work has no variants — clear the variant"
		if len(allowed) > 0 {
			hint = "pick one of: " + strings.Join(allowed, ", ")
		}
		return sql.NullString{}, entity.NewFieldViolation(step+".work_variant", "not_for_work", variant,
			fmt.Sprintf("the work %q has no variant %q — %s", work.String, variant, hint))
	}
	return sql.NullString{String: variant, Valid: true}, nil
}
