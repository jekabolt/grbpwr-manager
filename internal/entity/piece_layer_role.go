package entity

import "strings"

// РОЛЬ СЛОЯ ДЕТАЛИ КРОЯ (T4) — ПРОЕКЦИЯ, НЕ СУЩНОСТЬ.
//
// Решение владельца: одна деталь кроя может ссылаться на несколько материалов в одном колорвее,
// ЕСЛИ ЭТО СЛОИ (основной / подклад / дублерин / кант); две ОСНОВНЫЕ ткани на одной цельной
// детали — ошибка данных («это были бы две детали, и их соединение мы бы описали в конструкции»).
//
// Роль слоя НИГДЕ НЕ ХРАНИТСЯ. Каждая связь «деталь ↔ материал» — это детальная строка рецепта
// (tech_card_colorway_usage.piece_id), держащая FK на строку BOM, а строка BOM уже несёт обе оси:
// section (семейство) и purpose (НАЗНАЧЕНИЕ, 0265). Словарь владельца ложится на существующий без
// остатка: основной = purpose 'main', подклад = 'lining', дублерин = 'interfacing', и существующий
// словарь БОГАЧЕ владельческого (карманка, контраст, сетка, утеплитель) — проекция даёт эти роли
// бесплатно, с уже готовой локализацией и порядком показа (BomPurposeOrder).
//
// ПОЧЕМУ НЕ КОЛОНКА РОЛИ НА РЕБРЕ. (1) Ребро без строки BOM не существует — роль ребра,
// противоречащая назначению строки («ребро говорит подклад, строка говорит main»), была бы вторым
// именем того же факта с гарантированным рассинхроном. (2) Система УЖЕ выбрала purpose осью слоя:
// скоуп выкроек и раскладок с 0267 — «назначение, если разложено, иначе line_key»
// (ResolveFabricScope); хранить на ребре вторую копию значит расщепить ось. (3) Контрпример «один
// слот main — основная у полочки и кант у манжеты» разрешается моделью самого владельца: обтачка из
// основной ткани — это ОТДЕЛЬНАЯ деталь кроя со своим единственным слоем, а не вторая роль ребра.
//
// Клиентское зеркало (ручное, как весь словарь bom-purpose): piece-layer-role.ts рядом с
// bom-purpose.ts в админке.

// PieceLayerRoleUnsorted — «не разложено»: детальная строка ссылается на fabric-строку БЕЗ
// назначения. Единственная секция, где фолбэка нет намеренно: под section='fabric' прячутся и
// карманка, и контраст, и сетка, и любая догадка назвала бы их «основной» уверенно и неверно —
// ровно аргумент 0265 против бэкфилла. Пустая строка, а не именованное значение: роль — это
// TechCardBomPurpose, и девятое значение-призрак в словаре назначений заведёт то самое второе имя.
const PieceLayerRoleUnsorted TechCardBomPurpose = ""

// rollSectionFallbackRole — фолбэк роли по секции, когда назначение не задано. У трёх из четырёх
// рулонных секций роль однозначна из самого семейства; fabric в карте отсутствует сознательно
// (см. PieceLayerRoleUnsorted).
var rollSectionFallbackRole = map[TechCardBomSection]TechCardBomPurpose{
	BomSectionLining:      BomPurposeLining,
	BomSectionInterlining: BomPurposeInterfacing,
	BomSectionInsulation:  BomPurposeInsulation,
}

// DerivePieceLayerRole выводит роль слоя из строки BOM, на которую ссылается детальная строка
// рецепта. ЕДИНСТВЕННОЕ правило вывода — все потребители (валидация рецепта, кат-план, гейт
// готовности, кат-лист стиля) обязаны звать сюда, а не переизобретать пару (section, purpose).
//
//	rollGoods=false — секция не рулонная: у связи нет роли слоя, правила целостности к ней не
//	                  применяются (этикетку и нитку к детали привязывают законно, но кроя они не
//	                  описывают);
//	rollGoods=true  — роль = purpose, если задан и известен словарю; иначе фолбэк по секции;
//	                  для fabric без назначения — PieceLayerRoleUnsorted («не разложено»).
//
// Неизвестное словарю значение purpose читается как «не задано», а не как роль: защитная ветка для
// данных, пришедших мимо DB CHECK (снапшоты релизов, прямые вызовы стора).
func DerivePieceLayerRole(section TechCardBomSection, purpose string) (role TechCardBomPurpose, rollGoods bool) {
	if !IsRollGoodsSection(section) {
		return "", false
	}
	p := TechCardBomPurpose(strings.TrimSpace(purpose))
	if p != "" && IsValidTechCardBomPurpose(p) {
		return p, true
	}
	if fb, ok := rollSectionFallbackRole[section]; ok {
		return fb, true
	}
	return PieceLayerRoleUnsorted, true
}

// PieceLayerRole — та же деривация от загруженной строки BOM, для потребителей, держащих карту.
func (b *TechCardBomItem) PieceLayerRole() (TechCardBomPurpose, bool) {
	if b == nil {
		return "", false
	}
	return DerivePieceLayerRole(b.Section, b.Purpose.String)
}

// pieceLayerRoleLabels — подписи ролей для серверных сообщений (отказ рецепта, блокер
// кат-плана, находки гейта). Клиентские подписи живут своим зеркалом (bom-purpose-labels.ts);
// здесь — только то, что сервер печатает сам.
var pieceLayerRoleLabels = map[TechCardBomPurpose]string{
	BomPurposeMain:        "main fabric",
	BomPurposeLining:      "lining",
	BomPurposePocketing:   "pocketing",
	BomPurposeInterfacing: "interfacing",
	BomPurposeInsulation:  "insulation",
	BomPurposeContrast:    "contrast",
	BomPurposeMesh:        "mesh",
	BomPurposeOther:       "other",
}

// PieceLayerRoleLabel — имя роли; у «не разложено» своё, потому что это не роль, а её
// отсутствие, и печатать его шрифтом роли значило бы выдать пробел за факт.
func PieceLayerRoleLabel(r TechCardBomPurpose) string {
	if r == PieceLayerRoleUnsorted {
		return "unsorted"
	}
	if l, ok := pieceLayerRoleLabels[r]; ok {
		return l
	}
	return string(r)
}
