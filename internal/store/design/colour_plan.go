package design

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ⚠ ЧИТАТЕЛЯ-ГЛАГОЛА ЗДЕСЬ НЕТ, И ЭТО РЕШЕНИЕ. План приезжает ОДНИМ чтением полосы (GetBand), в том
// же снимке, что верстак и полки, — потому что студия рисует их одним кадром. Отдельный
// GetColourPlan был бы вторым моментом времени и вторым мнением о том, что значит пустая колонка;
// внутренний colourPlanByCard остаётся единственным читателем строки.

// ЦВЕТОВОЙ ПЛАН КАРТОЧКИ (0364) — ОДНА СТРОКА, ДВА JSON-ДОКУМЕНТА, CAS ПО `rev`.
//
// ФОРМА ВЗЯТА У СЛОЯ ПРАВКИ И ВЗЯТА НАМЕРЕННО. SaveEditLayer заменяет документ ЦЕЛИКОМ под
// `rev = :expected`, и здесь тот же довод, только сильнее: перекраска вида убирает цвета из палитры
// ВМЕСТЕ с их назначениями, поэтому «поправить одну строку» — это операция, которой у экрана нет.
// Патч по строкам заставил бы клиента собирать удаление из нескольких вызовов, каждый из которых
// умеет упасть отдельно, и половина плана осталась бы утверждать про цвет, которого больше нет.
//
// ⚠ CAS НЕ СТАНОВИТСЯ ЛИШНИМ ОТ SERIALIZABLE, и это ровно то, что говорит шапка SaveEditLayer:
// уровень изоляции упорядочивает двух писателей, но не знает, что второй смотрел на r3, когда r4
// уже существовал. Двадцать минут покраски — это ровно та работа, которую нельзя потерять молча.

// planRow — строка таблицы. JSON-колонки читаются сырыми и разбираются здесь: entity держит форму
// документа, а стор — строку.
type planRow struct {
	TechCardId int            `db:"tech_card_id"`
	Rev        int            `db:"rev"`
	Maps       entity.RawJSON `db:"maps"`
	Cloths     entity.RawJSON `db:"cloths"`
	UpdatedBy  string         `db:"updated_by"`
	UpdatedAt  time.Time      `db:"updated_at"`
}

// SetColourPlan заменяет план целиком под compare-and-set по `rev`.
//
// ⚠ ГРАНИЦА КАРТОЧКИ ДЛЯ КАРТИНОК ПРОВЕРЯЕТСЯ ЗДЕСЬ, В ТОЙ ЖЕ ТРАНЗАКЦИИ, ЧТО И ЗАПИСЬ. Карта и её
// подложка отдаются клиенту ссылками и рисуются им на экране, а сама карта потом уезжает
// ПОСТАВЩИКУ как вход прогона — то есть непроверенное поле означает, что план карточки A показывает
// и отправляет картинку карточки B. Правило ОТРИЦАТЕЛЬНОЕ («не принадлежит чужой карточке»),
// поэтому свежезагруженный ничейный PNG — обычный случай — проходит одним чтением; см. шапку
// refuseForeignMedia.
//
// ФОРМУ ДОКУМЕНТА ПРОВЕРЯЕТ entity, А НЕ CHECK В СХЕМЕ. Довод записан в 0364: ADD CONSTRAINT …
// CHECK копирует таблицу целиком и однажды уже остановил старт прода. Проверка зовётся ДО
// транзакции: отказ по форме не должен стоить открытия SERIALIZABLE.
func (s *Store) SetColourPlan(ctx context.Context, req entity.DesignColourPlanSave) (*entity.DesignColourPlan, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if req.ExpectedRev < 0 {
		return nil, fmt.Errorf("%w: expected_rev %d", entity.ErrDesignInvalidArgument, req.ExpectedRev)
	}
	mapsJSON, err := json.Marshal(colourPlanMapsOrEmpty(req.Maps))
	if err != nil {
		return nil, fmt.Errorf("failed to encode the colour maps: %w", err)
	}
	clothsJSON, err := json.Marshal(colourPlanClothsOrEmpty(req.Cloths))
	if err != nil {
		return nil, fmt.Errorf("failed to encode the colour plan's cloths: %w", err)
	}

	var out entity.DesignColourPlan
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if err := refuseForeignMedia(ctx, db, req.TechCardId, colourPlanMediaIDs(req.Maps)...); err != nil {
			return err
		}

		before, err := colourPlanByCard(ctx, db, req.TechCardId)
		if err != nil {
			return err
		}
		if before == nil {
			// ⚠ РОЖДЕНИЕ ТРЕБУЕТ expected_rev == 0, И ЭТО НЕ ПРИДИРКА. Клиент, echo'нувший rev 3
			// у карточки без плана, смотрит на план, которого здесь нет: либо его удалили, либо он
			// перепутал карточку. Молча завести первый rev под этим намерением значило бы принять
			// просьбу, которой никто не делал.
			if req.ExpectedRev != 0 {
				return fmt.Errorf("%w: tech card %d has no colour plan yet, that is rev 0, %d was echoed",
					entity.ErrDesignColourPlanRevMismatch, req.TechCardId, req.ExpectedRev)
			}
			err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO design_colour_plan (tech_card_id, rev, maps, cloths, updated_by)
				VALUES (:card, 1, :maps, :cloths, :who)`,
				map[string]any{
					"card": req.TechCardId, "maps": []byte(mapsJSON),
					"cloths": []byte(clothsJSON), "who": req.Actor,
				})
			if err != nil {
				// 1062 здесь означает «пока мы шли, план завёл сосед». Это НЕ дубликат, который
				// можно проглотить: вызывающий полагал, что рождает план, поэтому честный ответ —
				// тот же CAS-отказ, который заставит его перечитать полосу и продолжить чужой план.
				if isDupKey(err) {
					return fmt.Errorf("%w: a colour plan for tech card %d already exists",
						entity.ErrDesignColourPlanRevMismatch, req.TechCardId)
				}
				return fmt.Errorf("failed to create the design colour plan: %w", err)
			}
			p, err := colourPlanByCard(ctx, db, req.TechCardId)
			if err != nil {
				return err
			}
			if p == nil {
				return fmt.Errorf("failed to read back the design colour plan of tech card %d", req.TechCardId)
			}
			out = *p
			return nil
		}

		n, err := storeutil.ExecNamedRows(ctx, db, `
			UPDATE design_colour_plan
			SET rev = rev + 1, maps = :maps, cloths = :cloths, updated_by = :who
			WHERE tech_card_id = :card AND rev = :expected`,
			map[string]any{
				"card": req.TechCardId, "expected": req.ExpectedRev,
				"maps": []byte(mapsJSON), "cloths": []byte(clothsJSON), "who": req.Actor,
			})
		if err != nil {
			return fmt.Errorf("failed to save the design colour plan of tech card %d: %w", req.TechCardId, err)
		}
		if n == 0 {
			// НОЛЬ СТРОК ЗДЕСЬ ЗНАЧИТ РОВНО ОДНО: строка есть (мы её только что прочли в этой же
			// транзакции), значит не сошёлся rev. Текущее значение называется в отказе, чтобы
			// клиенту было чем перечитать.
			return fmt.Errorf("%w: the colour plan is at rev %d, %d was echoed",
				entity.ErrDesignColourPlanRevMismatch, before.Rev, req.ExpectedRev)
		}
		p, err := colourPlanByCard(ctx, db, req.TechCardId)
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("failed to read back the design colour plan of tech card %d", req.TechCardId)
		}
		out = *p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteColourPlan removes the card's plan.
//
// УДАЛЕНИЕ ОТСУТСТВУЮЩЕГО — УСПЕХ. Намерение вызывающего «у этой карточки нет плана», и оно уже
// исполнено; отказ здесь заставил бы клиента различать два состояния, между которыми для него нет
// разницы.
//
// ⚠ PNG КАРТ НЕ ТРОГАЮТСЯ. Они могли уже замёрзнуть в рецепте прогона, а история обязана
// показывать те картинки, с которыми прогон был запущен.
func (s *Store) DeleteColourPlan(ctx context.Context, cardID int) error {
	if err := requireCard(cardID); err != nil {
		return err
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM design_colour_plan WHERE tech_card_id = :card`,
			map[string]any{"card": cardID}); err != nil {
			return fmt.Errorf("failed to delete the design colour plan of tech card %d: %w", cardID, err)
		}
		return nil
	})
}

// colourPlanByCard — ЕДИНСТВЕННЫЙ читатель строки на весь пакет, и разбор JSON живёт в нём.
// Второй читатель означал бы второе мнение о том, что значит пустая колонка.
func colourPlanByCard(ctx context.Context, db dependency.DB, cardID int) (*entity.DesignColourPlan, error) {
	row, err := storeutil.QueryNamedOne[planRow](ctx, db, `
		SELECT tech_card_id, rev, maps, cloths, updated_by, updated_at
		FROM design_colour_plan WHERE tech_card_id = :card`,
		map[string]any{"card": cardID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read the design colour plan of tech card %d: %w", cardID, err)
	}
	out := entity.DesignColourPlan{
		TechCardId: row.TechCardId,
		Rev:        row.Rev,
		UpdatedBy:  row.UpdatedBy,
		UpdatedAt:  row.UpdatedAt,
	}
	if len(row.Maps) > 0 {
		if err := json.Unmarshal(row.Maps, &out.Maps); err != nil {
			return nil, fmt.Errorf("the colour maps of tech card %d do not parse: %w", cardID, err)
		}
	}
	if len(row.Cloths) > 0 {
		if err := json.Unmarshal(row.Cloths, &out.Cloths); err != nil {
			return nil, fmt.Errorf("the colour plan cloths of tech card %d do not parse: %w", cardID, err)
		}
	}
	return &out, nil
}

// colourPlanMapsOrEmpty / colourPlanClothsOrEmpty — nil едет в колонку как `[]`, а не как `null`.
// Колонка объявлена NOT NULL, а `null` внутри JSON заставил бы каждого читателя различать два
// написания пустоты.
func colourPlanMapsOrEmpty(in []entity.DesignColourMap) []entity.DesignColourMap {
	if in == nil {
		return []entity.DesignColourMap{}
	}
	// Палитра внутри карты — та же история: карта без ярлыков законна (её только что положили и
	// ещё не красили), и `null` в ней читался бы клиентом иначе, чем пустой список.
	out := make([]entity.DesignColourMap, len(in))
	copy(out, in)
	for i := range out {
		if out[i].Palette == nil {
			out[i].Palette = []entity.DesignColourSwatch{}
		}
	}
	return out
}

func colourPlanClothsOrEmpty(in []entity.DesignColourCloth) []entity.DesignColourCloth {
	if in == nil {
		return []entity.DesignColourCloth{}
	}
	return in
}

// colourPlanMediaIDs — обе картинки каждой карты: сама карта и флэт, по которому её рисовали. Обе
// проверяются границей карточки, потому что обе едут клиенту ссылкой, и обе приезжают с провода.
func colourPlanMediaIDs(maps []entity.DesignColourMap) []int {
	out := make([]int, 0, len(maps)*2)
	for _, m := range maps {
		if m.MediaId > 0 {
			out = append(out, m.MediaId)
		}
		if m.BaseMediaId > 0 {
			out = append(out, m.BaseMediaId)
		}
	}
	return out
}
