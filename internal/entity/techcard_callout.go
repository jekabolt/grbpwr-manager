package entity

import "strings"

// КАК АДРЕСУЕТСЯ УКАЗАНИЕ НА ЭСКИЗЕ — ОДНО ПРАВИЛО НА ВСЕХ, КТО СПРАШИВАЕТ.
//
// НОМЕР ВЫНОСКИ НЕ УНИКАЛЕН ПО КАРТОЧКЕ, И ЭТО НЕ ПОРЧА ДАННЫХ. Эскиз и мудборд — два разных
// документа, и клиент нумерует их НЕЗАВИСИМО, намеренно: «The sketch and the moodboard number
// INDEPENDENTLY … a moodboard reference note has no business pushing the next sketch callout to 7»
// (admin-client, sketch-tab.tsx, isMine/nextNumber). Схема дубли тоже не запрещает: у
// tech_card_callout нет ни одного UNIQUE, а сам номер — `callout_number INT NOT NULL DEFAULT 0`
// (0067_add_tech_card_core.sql:113). Значит «выноска номер 3» на карточке с мудбордом — вопрос без
// ответа, пока не назван ЭСКИЗ, на котором она стоит.
//
// ПОЧЕМУ ПРАВИЛО ЖИВЁТ ЗДЕСЬ, А НЕ У КАЖДОГО ПОТРЕБИТЕЛЯ. Потому что записанное дважды оно
// разошлось — и разошлось В РАЗНЫЕ СТОРОНЫ. Индекс детали кроя писался в карту без условия, то
// есть ПОСЛЕДНИЙ выигрывал; перенос геометрии брал ПЕРВОГО. На одной и той же карточке мудбордная
// выноска с номером технической молча помечала живую деталь кроя detached (у неё пропадал
// технический источник имени), а перенос геометрии в том же сохранении смотрел при этом на ДРУГУЮ
// выноску. Ни один из двух ответов не был неправильным сам по себе — неправильным было то, что
// ответов два.
//
// ГРАНИЦА ОДНА: поиск по номеру НИКОГДА не пересекает эскизы. Деталь кроя спрашивает про
// ТЕХНИЧЕСКИЙ лист (S7: мудбордное указание не несёт смысла детали), перенос геометрии — про тот
// самый эскиз, на котором стоит присланное указание. Остаточная неоднозначность — два указания с
// ОДНОЙ идентичностью — решается ОДИНАКОВО у всех: первый выигрывает.

// TechCardCalloutKey — идентичность указания на карточке: эскиз, на котором оно стоит, и его
// номер. Номера самого недостаточно (см. выше), поэтому пары нет и быть не может отдельно от
// картинки.
type TechCardCalloutKey struct {
	MediaId int // 0 — указание ни на чём не запинено
	Number  int
}

// CalloutKey — идентичность ЭТОГО указания.
func (c TechCardCallout) CalloutKey() TechCardCalloutKey {
	id := 0
	if c.MediaId.Valid {
		id = int(c.MediaId.Int32)
	}
	return TechCardCalloutKey{MediaId: id, Number: c.Number}
}

// TechCardCalloutsByKey — указания по их идентичности.
//
// ПЕРВЫЙ ВЫИГРЫВАЕТ. Два указания с одной идентичностью — уже испорченные данные: клиент такого не
// минтит (nextNumber берёт максимум и по живым выноскам своего листа, и по всем ещё ссылающимся
// номерам — «A number that is still referenced is not free»). На испорченных данных выбор обязан
// быть детерминированным и ОДИНАКОВЫМ у всех, кто спрашивает; какой именно из двойников победит —
// значения не имеет, важно, что один и тот же.
func TechCardCalloutsByKey(callouts []TechCardCallout) map[TechCardCalloutKey]TechCardCallout {
	out := make(map[TechCardCalloutKey]TechCardCallout, len(callouts))
	for _, c := range callouts {
		k := c.CalloutKey()
		if _, seen := out[k]; seen {
			continue
		}
		out[k] = c
	}
	return out
}

// TechCardCalloutIndex — разрешение ссылки «деталь кроя → выноска» (S6/S7/S8). Держит ТОЛЬКО
// указания, запиненные на ТЕХНИЧЕСКИЕ эскизы этой карточки: только они несут смысл детали, и
// только их номера деталь и имеет в виду.
type TechCardCalloutIndex struct {
	byNumber map[int]TechCardCallout
}

// NewTechCardCalloutIndex индексирует технические указания карточки по номеру. Медиа и указания
// приезжают ОДНИМ payload'ом вместе с деталями, поэтому чтения из базы здесь не нужно.
//
// В индекс не попадает ничто, кроме указаний на технических эскизах, — ровно то, что ApplyToPiece
// и так требовало. Пока в него писались все подряд, мудбордный двойник ВЫТЕСНЯЛ техническую
// выноску из карты, и деталь, у которой источник имени никуда не девался, объявлялась оторванной.
func NewTechCardCalloutIndex(media []TechCardMediaItem, callouts []TechCardCallout) TechCardCalloutIndex {
	technical := make(map[int]bool, len(media))
	for _, m := range media {
		if m.Category == TechCardMediaCategoryTechnical {
			technical[m.MediaId] = true
		}
	}
	ix := TechCardCalloutIndex{byNumber: make(map[int]TechCardCallout, len(callouts))}
	for _, c := range callouts {
		// Незапиненное указание в индекс не идёт: по нумерации оно принадлежит техническому листу
		// (клиентское isMine), но стоит НЕ НА ЭСКИЗЕ, а значит источником имени детали быть не
		// может (S7). Незнакомая картинка — тоже мимо: technical[…] по ней ложно.
		if !c.MediaId.Valid || !technical[int(c.MediaId.Int32)] {
			continue
		}
		if _, seen := ix.byNumber[c.Number]; seen {
			continue // первый выигрывает — та же развязка, что в TechCardCalloutsByKey
		}
		ix.byNumber[c.Number] = c
	}
	return ix
}

// TechnicalCallout — техническое указание с этим номером, если оно есть.
func (ix TechCardCalloutIndex) TechnicalCallout(number int) (TechCardCallout, bool) {
	c, ok := ix.byNumber[number]
	return c, ok
}

// ApplyToPiece сводит имя детали кроя с её технической выноски и выставляет detached (S7/S8):
// деталь, привязанная к техническому указанию, берёт его `part` своим каноническим именем (имя
// детали живёт ОДИН раз, на выноске); деталь, чьё указание удалили или чьё указание мудбордное
// либо незапиненное, СВОЁ имя сохраняет, но помечается detached — живого технического источника у
// неё нет. Оторванную деталь orphan-control сохраняет, а не удаляет.
func (ix TechCardCalloutIndex) ApplyToPiece(p *TechCardPiece) {
	if !p.CalloutNumber.Valid {
		p.Detached = false
		return
	}
	c, ok := ix.TechnicalCallout(int(p.CalloutNumber.Int32))
	if !ok {
		p.Detached = true
		return
	}
	if part := strings.TrimSpace(c.Part.String); part != "" {
		p.Name = part
	}
	p.Detached = false
}
