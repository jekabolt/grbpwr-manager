-- 101-MOODBOARD-ROLES, Ф4 — THE MOODBOARD BECOMES THE ONLY SOURCE OF A FLAT'S PICTURES (data only).
--
-- The flat's separate input («INPUT — REFERENCES») was the tech_card_media rows of kind 'reference'
-- with their roles in design_reference. The client no longer draws it (Ф3) and the server reads a
-- person's label for a FLAT run only on a moodboard picture with a matching purpose
-- (designBoardIsTheSource, designRunRefsFor). This file moves the stored input onto the board, so
-- nothing that travelled before stops travelling — except the retired three-quarter views (step 4):
--
--   1. a moodboard row (board or input) WITHOUT a purpose whose picture carries a PERSON's view label
--      gets the purpose that label implies — 'detail' for a detail, 'target' for any view;
--   2. an input row whose picture is already on the board goes — the board row stays, with step 1's
--      purpose and, when it has no caption, the input row's caption (the old client never wrote one;
--      nothing written is dropped silently); callouts are keyed by media_id and stay with the picture;
--   3. every other input row becomes a board row (kind 'moodboard'), keeping its place
--      (display_order) and the purpose step 1 gave it; a row with no label stays without a purpose and
--      the model proposes one when the card is next opened (a cheap call, under the usual fences);
--   4. a person's three-quarter label (retired views, D-18) is marked 'unsure': it no longer rides,
--      and the moodboard asks «which view is this?» instead of sending a view nobody can draw.
--
-- A RELEASED CARD IS SKIPPED: it is frozen, and rewriting its media would make its signed DESIGN
-- digest read as changed. Its old input rows keep riding by the old rule (designRefsBy grandfathers a
-- legacy input row). Any other card whose DESIGN section is signed off WILL read «changed since
-- sign-off» after this file when it had input rows — the section's content (media kind/role) did
-- change; the signer re-approves.
--
-- A stale admin tab that still sends input rows is folded the same way on save
-- (designFoldReferenceRows). The word 'reference' leaves the kind dictionary (CHECK 0346) only in a
-- SECOND deploy, once `SELECT COUNT(*) FROM tech_card_media WHERE kind = 'reference'` is 0 on both
-- databases outside released cards (README-pending-drops.md).
--
-- NO DDL, NO CHECK. Every statement is idempotent: a re-run finds no row without a purpose that a
-- label would fill, no input row left to delete or convert, and no three-quarter label still 'ok'.
-- One statement per line group (prod runs without multiStatements).

-- +migrate Up
UPDATE tech_card_media tcm
JOIN tech_card tc ON tc.id = tcm.tech_card_id
JOIN design_reference dr ON dr.tech_card_id = tcm.tech_card_id AND dr.media_id = tcm.media_id
SET tcm.role = IF(dr.role = 'detail', 'detail', 'target')
WHERE tcm.category = 'moodboard'
  AND tcm.role = ''
  AND dr.role <> ''
  AND dr.label_source IN ('', 'human', 'quiz')
  AND tc.approval_state <> 'released';

UPDATE tech_card_media b
JOIN tech_card_media r
  ON r.tech_card_id = b.tech_card_id AND r.media_id = b.media_id
 AND r.category = 'moodboard' AND r.kind = 'reference'
JOIN tech_card tc ON tc.id = b.tech_card_id
SET b.caption = r.caption
WHERE b.category = 'moodboard'
  AND b.kind <> 'reference'
  AND (b.caption IS NULL OR b.caption = '')
  AND r.caption IS NOT NULL AND r.caption <> ''
  AND tc.approval_state <> 'released';

DELETE r FROM tech_card_media r
JOIN tech_card_media b
  ON b.tech_card_id = r.tech_card_id AND b.media_id = r.media_id
 AND b.category = 'moodboard' AND b.kind <> 'reference'
JOIN tech_card tc ON tc.id = r.tech_card_id
WHERE r.category = 'moodboard'
  AND r.kind = 'reference'
  AND tc.approval_state <> 'released';

UPDATE tech_card_media tcm
JOIN tech_card tc ON tc.id = tcm.tech_card_id
SET tcm.kind = 'moodboard'
WHERE tcm.category = 'moodboard'
  AND tcm.kind = 'reference'
  AND tc.approval_state <> 'released';

UPDATE design_reference dr
JOIN tech_card tc ON tc.id = dr.tech_card_id
SET dr.label_state = 'unsure'
WHERE dr.role IN ('three_quarter_l', 'three_quarter_r')
  AND dr.label_state IN ('', 'ok')
  AND dr.label_source IN ('', 'human', 'quiz')
  AND tc.approval_state <> 'released';

-- +migrate Down
-- Nothing to undo by SQL: which board rows were input rows before is not recorded, and turning board
-- rows back into input rows would hide pictures the person has since placed. The previous binary reads
-- board rows and labels as before (it simply shows no input grid entries for them).
SELECT 1;
