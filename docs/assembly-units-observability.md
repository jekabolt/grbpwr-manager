# Узлы сборки: как узнать, что происходит

Фича добавляет три вещи, которые иначе не наблюдаемы: отказы правил, срабатывания щита и сам
факт использования. Без них после выкатки нельзя ответить ни на «пользуются ли», ни на «не
бьётся ли кто-то о щит в проде».

## Логи

Все строки структурные (`slog`), ищутся по сообщению.

| сообщение | что значит | поля |
|---|---|---|
| `assembly canonicalisation refused a payload` | запись отвергнута правилом графа | `rule` (номер правила 0–7), `branch` (машинный код ветки), `step`, `violations` |
| `assembly gate refused an unaware payload that echoes units` | старый бандл прислал сборочные поля | `gate=wire` |
| `assembly gate refused an outdated bundle against a marked-up card` | старая вкладка против размеченной карточки | `gate=stored`, `tech_card_id` |
| `assembly backstop refused an aware but empty save` | осведомлённая, но пустая запись — тихое стирание предотвращено | `gate=backstop`, `tech_card_id` |
| `assembly gate refused assembly_cleared on a card with no markup` | флаг «снял разметку» там, где её не было | `gate=stored`, `tech_card_id` |

Что читать по ним:

- **`branch` важнее, чем `rule`.** «Правило 1, двадцать раз» не говорит ничего; «правило 1,
  ветка `produced-later`, двадцать раз за неделю» говорит, что технолог не понимает порядок
  шагов, и чинить надо интерфейс, а не правило.
- **`gate=stored` в проде — сигнал, что клиент где-то не обновился.** Это единственный способ
  об этом узнать: пользователь видит «обновите вкладку» и обновляет, не сообщая никому.
- **`gate=backstop` — сигнал, что что-то пытается стереть разметку.** Одиночное срабатывание
  это параллельная вкладка; регулярные — баг в клиенте (восстановление черновика, применение
  AI-черновика).
- **У строк канонизации `tech_card_id` нет** — она работает до того, как карточка получила id
  (создание) и не знает его на обновлении. Карточку ищут по соседним строкам того же запроса.

## SQL: пользуются ли фичей

Только чтение. На проде — тоже только чтение.

```sql
-- Сколько карточек размечено, и насколько глубоко.
SELECT
    COUNT(DISTINCT o.tech_card_id)                                  AS marked_cards,
    COUNT(*)                                                        AS producing_steps,
    COUNT(DISTINCT o.output_unit_key)                               AS distinct_units
FROM tech_card_operation o
WHERE o.output_unit_key IS NOT NULL AND o.output_unit_key <> '';

-- Разметка по карточкам: сколько шагов, сколько из них производят узел, сколько входов-узлов.
SELECT
    o.tech_card_id,
    COUNT(*)                                                        AS steps,
    SUM(o.output_unit_key IS NOT NULL AND o.output_unit_key <> '')  AS producing_steps,
    (SELECT COUNT(*) FROM tech_card_operation_input i
      JOIN tech_card_operation o2 ON o2.id = i.operation_id
      WHERE o2.tech_card_id = o.tech_card_id AND i.unit_key IS NOT NULL) AS unit_inputs
FROM tech_card_operation o
GROUP BY o.tech_card_id
HAVING producing_steps > 0
ORDER BY producing_steps DESC;

-- Расхождение двойной записи: строки-детали, которые есть в новой таблице, но не в легаси
-- (или наоборот). Обязано быть ПУСТО, пока expand/contract не закрыт.
SELECT 'только в новой' AS side, i.operation_id, i.piece_id
FROM tech_card_operation_input i
LEFT JOIN tech_card_operation_piece p
       ON p.operation_id = i.operation_id AND p.piece_id = i.piece_id
WHERE i.piece_id IS NOT NULL AND p.id IS NULL
UNION ALL
SELECT 'только в легаси', p.operation_id, p.piece_id
FROM tech_card_operation_piece p
LEFT JOIN tech_card_operation_input i
       ON i.operation_id = p.operation_id AND i.piece_id = p.piece_id
WHERE i.id IS NULL;
```

Последний запрос — гейт для задачи Ф6 «снести 0199»: пока он что-то возвращает, сносить
легаси-таблицу нельзя, потому что фолбэк чтения ещё нужен.

## Аварийный люк

```
go run ./cmd assembly-clear --id <tech_card_id> --dry-run
go run ./cmd assembly-clear --id <tech_card_id>
go run ./cmd assembly-clear --id <tech_card_id> --reopen-to-draft   # если карточка RELEASED
```

Выпущенная карточка заморожена стором, и без `--reopen-to-draft` команда откажет с инструкцией.
Флаг отдельный намеренно: возврат выпущенной карточки в черновик — решение человека, а не
побочный эффект аварийного снятия разметки.

**Подписи команда НЕ пере-заверяет.** После снятия разметки секция CONSTRUCTION честно станет
«изменено после подписи» — ровно как при любой правке содержания. Скрипт не имеет права
утверждать человеческую подпись заново, и это поведение, а не недоделка.

**Огрубление, о котором надо знать:** узлы раскрываются по ФИНАЛЬНОМУ замыканию. Обработка на
шаге k, бравшая узел, который поглотили позже, получит и детали, которых на шаге k ещё не
существовало. Карточка остаётся валидной, но последовательность огрубляется — это аварийный
инструмент, а не редактор.

Снимает разметку с ОДНОЙ карточки, через штатный конвертер (канонизация, релизные гейты, штамп
подписей). Массового режима нет намеренно: снятие разметки со всех карточек — не аварийная
операция, а решение.

Когда он нужен: откат клиентского бандла делает размеченные карточки нередактируемыми для всех
(старый бандл не может послать `assembly_aware` и упирается в щит). До появления клиентской
кнопки «снять разметку» это единственный способ вернуть карточку в работу.
