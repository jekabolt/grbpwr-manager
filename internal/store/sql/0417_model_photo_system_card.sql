-- +migrate Up
-- The hidden system tech card «Model photos». A design run is always booked on a tech card, and the
-- model-photo white-background action (a cutout run on a library photo of a model) has no garment
-- card to book on, so every such run goes onto this one card. No schema change: the card is an
-- ordinary tech_card row marked by its create_request_id (entity.TechCardModelPhotosKey). The
-- "system:" key namespace is refused to clients by CreateTechCard, the row is kept out of
-- ListTechCards and the style pipeline (techCardNotSystemSQL), UpdateTechCard and DeleteTechCard
-- refuse it, and the admin dictionary carries its id (Dictionary.model_photo_tech_card_id).
--
-- Every other column takes its default (stage proto, approval_state draft, purpose sellable,
-- measurement_unit mm), which satisfies every CHECK on the table. Idempotent: the NOT EXISTS guard
-- (and UNIQUE uq_tech_card_create_request_id behind it) makes a re-run a no-op.
INSERT INTO tech_card (name, created_by, updated_by, create_request_id)
SELECT 'Model photos', 'system', 'system', 'system:model-photos' FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM tech_card WHERE create_request_id = 'system:model-photos');

-- +migrate Down
DELETE FROM tech_card WHERE create_request_id = 'system:model-photos';
