package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// КАТАЛОГ РАБОТ НА ПРОВОДЕ (R3) — ПЕРЕВОД, И ТОЛЬКО ПЕРЕВОД.
//
// НИ ОДНОГО ENUM НИ В ОДНОМ ПОЛЕ, И ЭТО ТО ЖЕ РЕШЕНИЕ, ЧТО У ОСИ РАБОТЫ НА ШАГЕ (0330): словарь
// работ — ДАННЫЕ (таблицы operation_work*), а не контракт. Незнакомый член proto-enum protojson
// выбрасывает МОЛЧА (инцидент Ж7), поэтому каждая новая работа делала бы не-обновлённый бандл
// тихим стирателем; строку не теряет никакой маршалер, и незнакомый токен доезжает до экрана
// текстом. По той же причине здесь нет ни отбора «только незалёгшие», ни сортировки, ни фильтров:
// каталог отдаётся ЦЕЛИКОМ и в порядке хранилища, а что показывать в пикере — решает пикер.
//
// ЧТО СЮДА НЕ ПОПАДАЕТ: `updated_at` дефолта. Он есть в таблице (чтобы человек однажды мог узнать,
// когда это в последний раз подтверждали), но клиенту сегодня отвечать нечем — метка «подставлено»
// не датируется. Поле, которое ехало бы и никем не читалось, — это обещание, которого никто не
// давал; появится экран истории дефолтов — появится и оно.

// ConvertOperationWorkCatalogToPb converts the stored catalog rows to the wire.
func ConvertOperationWorkCatalogToPb(works []entity.OperationWork) []*pb_admin.OperationWorkCatalogItem {
	out := make([]*pb_admin.OperationWorkCatalogItem, 0, len(works))
	for _, w := range works {
		out = append(out, &pb_admin.OperationWorkCatalogItem{
			Token:       w.Token,
			Verb:        w.Verb,
			Stage:       w.Stage,
			Label:       w.Label,
			MachineMode: w.MachineMode,
			// NULL default_machine — законное состояние (machine_mode = none), и оно едет пустой
			// строкой: у работы, которая не спрашивает «на чём», ответа нет.
			DefaultMachine: w.DefaultMachine.String,
			Machines:       append([]string(nil), w.Machines...),
			Syn:            append([]string(nil), w.Syn...),
			Sort:           int32(w.Sort),
			// Снятие — ФЛАГ, а не отсутствие строки: retired-работу обязано открывать чтение старых
			// шагов, и имя ей даёт этот же ответ.
			Retired: w.Retired(),
		})
	}
	return out
}

// ConvertOperationWorkDefaultsToPb converts the stored global defaults to the wire.
func ConvertOperationWorkDefaultsToPb(defaults []entity.OperationWorkDefault) []*pb_admin.OperationWorkDefaultItem {
	out := make([]*pb_admin.OperationWorkDefaultItem, 0, len(defaults))
	for _, d := range defaults {
		out = append(out, &pb_admin.OperationWorkDefaultItem{
			WorkToken: d.WorkToken,
			Field:     d.Field,
			Value:     d.Value,
		})
	}
	return out
}

// ConvertOperationWorkSmvHintsToPb converts the «last time it was N» ghosts to the wire.
func ConvertOperationWorkSmvHintsToPb(hints []entity.OperationWorkSmvHint) []*pb_admin.OperationWorkSmvHint {
	out := make([]*pb_admin.OperationWorkSmvHint, 0, len(hints))
	for _, h := range hints {
		out = append(out, &pb_admin.OperationWorkSmvHint{
			WorkToken: h.WorkToken,
			LastSmv:   &pb_decimal.Decimal{Value: h.LastSMV.String()},
			CardName:  h.CardName,
		})
	}
	return out
}
