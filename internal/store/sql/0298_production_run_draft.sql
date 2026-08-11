-- +migrate Up

-- ЧЕРНОВАЯ ПАРТИЯ (Ф2): статус `draft` — расчёт, а не производство.
--
-- ЗАЧЕМ. Владелец просит считать себестоимость по КОНКРЕТНОЙ партии: выбрать колорвеи, размеры и
-- количества, и увидеть деньги именно этого производства. Объект для этого в системе уже есть —
-- production_run с его строками (колорвей × размер × план) и взвешенной по ним плановой ценой.
--
-- Но взять его как есть нельзя, и это выяснилось проверкой, а не рассуждением: прогон РЕЗЕРВИРУЕТ
-- ТКАНЬ С МОМЕНТА РОЖДЕНИЯ (Ф5б.4, reconcileRunReservations в CreateProductionRun) и проходит гейт
-- готовности ещё до сохранения. То есть каждая прикидка «а сколько будет стоить, если сшить сто
-- чёрных и полсотни белых» немедленно занимала бы ткань на складе, и соседний прогон видел бы
-- нехватку из-за чужого черновика. Считать деньги — не значит занимать сырьё.
--
-- ЧТО ДАЁТ ЭТОТ СТАТУС: партию можно набрать, посчитать и выбросить. Резерв, гейт готовности и
-- наряд появляются на переходе draft → planned, то есть тогда, когда партию действительно решили
-- шить. Ровно один переход, и он же — единственное место, где черновик становится обязательством.
--
-- ПОЧЕМУ НЕ ОТДЕЛЬНЫЙ ОБЪЕКТ «сценарий». Состав партии, написанный дважды, разойдётся при первой же
-- правке («в сценарии 100, в прогоне 120»), и стоить это будет дороже любой экономии на схеме. У
-- прогона уже есть и микс, и цена по миксу, и потребность, и настилы — черновику нужно ровно одно:
-- чтобы его существование ничего не стоило.
--
-- 0233 расширила колонку и назвала constraint стабильным именем ровно ради таких правок, 0245 этим
-- уже воспользовалась — здесь тот же приём, третий раз.

SET @have := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run' AND CONSTRAINT_NAME = 'chk_production_run_status');
SET @sql := IF(@have > 0, 'ALTER TABLE production_run DROP CHECK chk_production_run_status', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run' AND CONSTRAINT_NAME = 'chk_production_run_status');
SET @sql := IF(@have > 0, 'SELECT 1',
    'ALTER TABLE production_run ADD CONSTRAINT chk_production_run_status CHECK (status REGEXP ''^(draft|planned|in_progress|partially_received|received|closed|cancelled)$'')');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- Сузить набор обратно нельзя: на любом сохранённом черновике ADD CONSTRAINT проверит ВСЮ историю
-- таблицы и остановит старт. Осознанный no-op, как в 0245.
SELECT 1;
