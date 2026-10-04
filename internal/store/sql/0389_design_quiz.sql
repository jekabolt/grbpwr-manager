-- Moodboard quiz (2026-10-04) — the answers table and the route of the new AI purpose.
--
-- OWNER: «при создании техкарты когда ты уже накидал мудборд … нажать кнопку пораспрашивай меня об
-- этой вещи и оно тебе давало бы квиз». The quiz is one sync vision+JSON call (chat.design_quiz);
-- its answers live HERE, one row per answered question, and feed every later generation.
--
-- WHY A TABLE OF ITS OWN. UpdateTechCard full-replaces tech_card_detail on every 2 s autosave; a row
-- outside its DELETE loop survives the autosave by construction (like tech_card_revision). The row
-- carries the whole question (options, contradiction flags, the clarify follow-up) so a re-run and an
-- edit of a folded answer need nothing else. Writes are a full-list replace by the one client door
-- (SaveDesignQuizAnswers): DELETE by card + INSERT, one transaction.
--
-- VOCABULARIES ARE VARCHAR, CLOSED IN GO (entity.IsDesignQuiz*): no ENUM, no CHECK.
--
-- ROUTE: openrouter with an explicit Claude slug (the direct `anthropic` provider is seeded disabled).
-- INSERT IGNORE never overwrites what the owner saved later. THE OLD BINARY SURVIVES THE ROW: the
-- store orders an unknown purpose after the known ones on read and refuses it only on a panel write.
--
-- IDEMPOTENT: IF NOT EXISTS + INSERT IGNORE; a half-applied file re-runs from the top.

-- +migrate Up

CREATE TABLE IF NOT EXISTS tech_card_design_quiz_answer (
    id INT NOT NULL PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    question_id VARCHAR(64) NOT NULL COMMENT 'snake_case, unique per card',
    category VARCHAR(16) NOT NULL COMMENT 'design|details|materials|use|finish (closed in Go)',
    part VARCHAR(32) NOT NULL DEFAULT 'whole' COMMENT 'pictogram part key of the family',
    family VARCHAR(16) NOT NULL DEFAULT '' COMMENT 'garment pictogram family, "" = none',
    part_view VARCHAR(8) NOT NULL DEFAULT 'front' COMMENT 'pictogram view of the part: front|back|side_l',
    kind VARCHAR(8) NOT NULL DEFAULT 'single' COMMENT 'single|multi',
    question TEXT NOT NULL,
    options_json JSON NOT NULL COMMENT '["…"] as offered',
    contradicts_json JSON NOT NULL COMMENT '[bool] parallel to options_json',
    visual_evidence TEXT NOT NULL COMMENT 'what the pictures showed; never shown in UI',
    clarify_question TEXT NOT NULL COMMENT '"" = no follow-up',
    clarify_options_json JSON NOT NULL,
    selected_json JSON NOT NULL COMMENT '["…"] chosen option texts, [] when none',
    free_text TEXT NOT NULL,
    skipped TINYINT(1) NOT NULL DEFAULT 0,
    display_order INT NOT NULL DEFAULT 0,
    answered_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT uniq_tc_design_quiz_question UNIQUE (tech_card_id, question_id),
    CONSTRAINT fk_tc_design_quiz_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Moodboard quiz answers, one row per answered question; full-list replace';

INSERT IGNORE INTO ai_route (purpose, position, provider_key, model) VALUES
    ('chat.design_quiz', 1, 'openrouter', 'anthropic/claude-sonnet-5');

-- +migrate Down

DELETE FROM ai_route
WHERE purpose = 'chat.design_quiz' AND position = 1
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-sonnet-5';

DROP TABLE IF EXISTS tech_card_design_quiz_answer;
