-- Полоса DESIGN, кусок 4 из 7: слой правки — векторная калька, живущая между двумя сохранениями.
--
-- ЭКРАННЫЙ ФАКТ. Лёгкую правку флэта доводят у нас, тяжёлую — в Illustrator. Слой держит лёгкую:
-- штрихи поверх картинки (или поверх пустоты), которые человек копит несколькими сеансами, а
-- потом «флэттит» — растеризует в обычную картинку полосы (`design_picture` с `derived_from`,
-- `source_class='drawn'` и `layer_rev`).
--
-- `base_media_id` NULLABLE — ЭТО НЕ ОСТОРОЖНОСТЬ, А ДВЕРЬ. `draw it` из пустой студии рождает слой
-- БЕЗ базы: чистую векторную основу. Модель «слой = калька поверх картинки» этой двери не
-- выражает вовсе. Уникальность `(tech_card_id, base_media_id)` остаётся — одна калька на картинку,
-- — но пустых баз на карточке может быть несколько: несколько NULL в UNIQUE в MySQL законны.
-- Именно поэтому слой адресуется СВОИМ `id`, а не базой.
--
-- ПЕРВАЯ РЕДАКЦИЯ ЭТОГО ФАЙЛА ОСТАВЛЯЛА `base_media_id` БЕЗ FK, И ЭТО БЫЛО ОШИБКОЙ.
-- Довод звучал так: «база слоя не владеет — слой это калька, а не выпуск; RESTRICT сделал бы
-- картинку неудаляемой из-за черновой кальки». Он опирался на неверную посылку, будто стёртая
-- база оставляет работоспособный слой, который «Go честно нарисует без подложки». Не нарисует:
-- штрихи это координаты ПОВЕРХ конкретного изображения, и без него сохранённый слой нечем
-- открыть и нечем сплющить. Медиатека при голом KEY показала бы файл свободным, человек стёр бы
-- его и вернулся к своей правке к штрихам над пустотой.
--
-- Поэтому решение обратное (2026-08-30): `base_media_id → media(id) ON DELETE RESTRICT`, и
-- колонка ОБЯЗАНА быть в `mediaRefRegistry`. Владеющих ссылок в волне ЧЕТЫРЕ, и эта — одна из
-- них. Отказ в удалении здесь не побочный эффект, а сообщение: файл держит незавершённая правка.
--
-- Разница с `design_picture.derived_from` и `design_reference.media_id`, у которых FK нет:
-- `derived_from` указывает на сиблинга, чьё исчезновение обрывает только родословную, а роль
-- референса это подсказка модели, и её замок на удаление был бы вредом. Подложка — несущая.
--
-- RESTRICT здесь НЕ ломает `DeleteTechCard`, и это проверено исполнением на стенде, а не выведено:
-- ограничение стоит на стороне `media`, которая от `tech_card` не каскадится, поэтому удаление
-- карточки сносит слой каскадом по `tech_card_id`, слой отпускает медиа, и 1451 не возникает.
--
-- ИСТОРИИ РЕВИЗИЙ СЛОЯ НЕТ НАМЕРЕННО. Версия листа пиннит `content_hash` уже растеризованного
-- файла, поэтому «что было на бумаге» восстанавливается из выпуска, а не из истории штрихов.
-- `rev` здесь — CAS, а не журнал: и сохранение, и флэттен требуют `expected_rev`, иначе флэттен
-- материализует чужой r4 под намерением того, кто видел r3.

-- +migrate Up

CREATE TABLE IF NOT EXISTS design_edit_layer (
    id INT UNSIGNED PRIMARY KEY AUTO_INCREMENT COMMENT 'слой адресуется своим id: чистых баз на карточке может быть несколько',
    tech_card_id INT NOT NULL,
    base_media_id INT NULL COMMENT 'подложка кальки; NULL = чистая векторная база (дверь draw it из пустой студии). На проводе NULL едет нулём. FK RESTRICT: слой без подложки неработоспособен — см. шапку',
    rev INT NOT NULL DEFAULT 0 COMMENT 'CAS-ревизия: и SaveEditLayer, и FlattenEditLayer требуют expected_rev',
    strokes JSON NULL COMMENT 'штрихи слоя; колонка ВАЛИДИРУЕТ JSON, поэтому непрозрачный или сжатый payload она отвергнет — размер режет Go отказом strokes_too_large',
    updated_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'username из JWT последнего писателя',
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    UNIQUE KEY uq_design_edit_layer_base (tech_card_id, base_media_id),
    KEY idx_design_edit_layer_base_media (base_media_id),
    CONSTRAINT fk_design_edit_layer_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_design_edit_layer_base_media FOREIGN KEY (base_media_id) REFERENCES media(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Слой правки: векторная калька поверх картинки либо поверх пустоты';

-- +migrate Down

DROP TABLE IF EXISTS design_edit_layer;
