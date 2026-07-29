package campaign

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/segment"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type recipientRow struct {
	ID                     uint64         `db:"id"`
	CampaignID             int            `db:"campaign_id"`
	AccountID              sql.NullInt64  `db:"account_id"`
	Email                  string         `db:"email"`
	LanguageID             int            `db:"language_id"`
	VariantID              sql.NullInt64  `db:"variant_id"`
	Cohort                 string         `db:"cohort"`
	Status                 string         `db:"status"`
	UnsubscribeURL         sql.NullString `db:"unsubscribe_url"`
	DispatchBatchID        sql.NullString `db:"dispatch_batch_id"`
	BatchOrdinal           sql.NullInt64  `db:"batch_ordinal"`
	ResendIdempotencyKey   sql.NullString `db:"resend_idempotency_key"`
	ClaimToken             sql.NullString `db:"claim_token"`
	ClaimExpiresAt         sql.NullTime   `db:"claim_expires_at"`
	PayloadSHA256          []byte         `db:"payload_sha256"`
	ResendEmailID          sql.NullString `db:"resend_email_id"`
	AttemptCount           int            `db:"attempt_count"`
	NextAttemptAt          sql.NullTime   `db:"next_attempt_at"`
	FirstProviderAttemptAt sql.NullTime   `db:"first_provider_attempt_at"`
	LastAttemptAt          sql.NullTime   `db:"last_attempt_at"`
	ErrorCode              sql.NullString `db:"error_code"`
	LastError              sql.NullString `db:"last_error"`
	SentAt                 sql.NullTime   `db:"sent_at"`
	CompletedAt            sql.NullTime   `db:"completed_at"`
	DeliveredAt            sql.NullTime   `db:"delivered_at"`
	FirstOpenedAt          sql.NullTime   `db:"first_opened_at"`
	LastOpenedAt           sql.NullTime   `db:"last_opened_at"`
	OpenCount              int            `db:"open_count"`
	FirstClickedAt         sql.NullTime   `db:"first_clicked_at"`
	LastClickedAt          sql.NullTime   `db:"last_clicked_at"`
	ClickCount             int            `db:"click_count"`
	BouncedAt              sql.NullTime   `db:"bounced_at"`
	ComplainedAt           sql.NullTime   `db:"complained_at"`
	UnsubscribedAt         sql.NullTime   `db:"unsubscribed_at"`
	HardFailedAt           sql.NullTime   `db:"hard_failed_at"`
	CreatedAt              sql.NullTime   `db:"created_at"`
	UpdatedAt              sql.NullTime   `db:"updated_at"`
}

const recipientColumns = `
	id, campaign_id, account_id, email, language_id, variant_id, cohort, status,
	unsubscribe_url, dispatch_batch_id, batch_ordinal, resend_idempotency_key,
	claim_token, claim_expires_at, payload_sha256, resend_email_id, attempt_count,
	next_attempt_at, first_provider_attempt_at, last_attempt_at, error_code,
	last_error, sent_at, completed_at, delivered_at, first_opened_at,
	last_opened_at, open_count, first_clicked_at, last_clicked_at, click_count,
	bounced_at, complained_at, unsubscribed_at, hard_failed_at, created_at, updated_at`

const campaignRecipientFreshClaimableSQL = `
	ecr.variant_id IS NOT NULL
	AND ecr.dispatch_batch_id IS NULL
	AND ecr.next_attempt_at <= UTC_TIMESTAMP(6)`

const campaignRecipientReclaimableSQL = `
	ecr.dispatch_batch_id IS NOT NULL
	AND ecr.next_attempt_at <= UTC_TIMESTAMP(6)
	AND (ecr.claim_token IS NULL OR ecr.claim_expires_at < UTC_TIMESTAMP(6))`

const dispatchCampaignPickerSQL = `
	SELECT ec.id, ec.topic
	FROM email_campaign ec
	WHERE ec.status = 'sending'
	  AND ec.audience_materialized_at IS NOT NULL
	  AND EXISTS (
	      SELECT 1
	      FROM email_campaign_recipient ecr
	      WHERE ecr.campaign_id = ec.id
	        AND ecr.status = 'pending'
	        AND (
	            (` + campaignRecipientFreshClaimableSQL + `)
	            OR (` + campaignRecipientReclaimableSQL + `)
	        )
	  )
	ORDER BY ec.id
	LIMIT 1
	FOR UPDATE SKIP LOCKED`

func recipientRowToEntity(row recipientRow) entity.EmailCampaignRecipient {
	out := entity.EmailCampaignRecipient{
		ID:            row.ID,
		CampaignID:    row.CampaignID,
		Email:         row.Email,
		LanguageID:    row.LanguageID,
		Cohort:        entity.EmailCampaignCohort(row.Cohort),
		Status:        entity.EmailCampaignRecipientStatus(row.Status),
		PayloadSHA256: append([]byte(nil), row.PayloadSHA256...),
		AttemptCount:  row.AttemptCount,
		OpenCount:     row.OpenCount,
		ClickCount:    row.ClickCount,
		CreatedAt:     row.CreatedAt.Time,
		UpdatedAt:     row.UpdatedAt.Time,
	}
	if row.AccountID.Valid {
		value := int(row.AccountID.Int64)
		out.AccountID = &value
	}
	if row.VariantID.Valid {
		value := int(row.VariantID.Int64)
		out.VariantID = &value
	}
	if row.UnsubscribeURL.Valid {
		value := row.UnsubscribeURL.String
		out.UnsubscribeURL = &value
	}
	if row.DispatchBatchID.Valid {
		value := row.DispatchBatchID.String
		out.DispatchBatchID = &value
	}
	if row.BatchOrdinal.Valid {
		value := int(row.BatchOrdinal.Int64)
		out.BatchOrdinal = &value
	}
	if row.ResendIdempotencyKey.Valid {
		value := row.ResendIdempotencyKey.String
		out.ResendIdempotencyKey = &value
	}
	if row.ClaimToken.Valid {
		value := row.ClaimToken.String
		out.ClaimToken = &value
	}
	if row.ClaimExpiresAt.Valid {
		value := row.ClaimExpiresAt.Time
		out.ClaimExpiresAt = &value
	}
	if row.ResendEmailID.Valid {
		value := row.ResendEmailID.String
		out.ResendEmailID = &value
	}
	if row.NextAttemptAt.Valid {
		value := row.NextAttemptAt.Time
		out.NextAttemptAt = &value
	}
	if row.FirstProviderAttemptAt.Valid {
		value := row.FirstProviderAttemptAt.Time
		out.FirstProviderAttemptAt = &value
	}
	if row.LastAttemptAt.Valid {
		value := row.LastAttemptAt.Time
		out.LastAttemptAt = &value
	}
	if row.ErrorCode.Valid {
		value := row.ErrorCode.String
		out.ErrorCode = &value
	}
	if row.LastError.Valid {
		value := row.LastError.String
		out.LastError = &value
	}
	if row.SentAt.Valid {
		value := row.SentAt.Time
		out.SentAt = &value
	}
	if row.CompletedAt.Valid {
		value := row.CompletedAt.Time
		out.CompletedAt = &value
	}
	if row.DeliveredAt.Valid {
		value := row.DeliveredAt.Time
		out.DeliveredAt = &value
	}
	if row.FirstOpenedAt.Valid {
		value := row.FirstOpenedAt.Time
		out.FirstOpenedAt = &value
	}
	if row.LastOpenedAt.Valid {
		value := row.LastOpenedAt.Time
		out.LastOpenedAt = &value
	}
	if row.FirstClickedAt.Valid {
		value := row.FirstClickedAt.Time
		out.FirstClickedAt = &value
	}
	if row.LastClickedAt.Valid {
		value := row.LastClickedAt.Time
		out.LastClickedAt = &value
	}
	if row.BouncedAt.Valid {
		value := row.BouncedAt.Time
		out.BouncedAt = &value
	}
	if row.ComplainedAt.Valid {
		value := row.ComplainedAt.Time
		out.ComplainedAt = &value
	}
	if row.UnsubscribedAt.Valid {
		value := row.UnsubscribedAt.Time
		out.UnsubscribedAt = &value
	}
	if row.HardFailedAt.Valid {
		value := row.HardFailedAt.Time
		out.HardFailedAt = &value
	}
	return out
}

// ClaimEmailCampaignBatch first recovers a persisted batch identity and only
// then creates a fresh one. Incident guarantee (b): SKIP LOCKED, an immutable
// batch key, and the status='pending' record guard ensure two workers/ticks
// never process the same recipient rows.
func (s *Store) ClaimEmailCampaignBatch(
	ctx context.Context,
	batchSize int,
	lease time.Duration,
) (*entity.EmailCampaignBatch, error) {
	if batchSize <= 0 || batchSize > 100 {
		return nil, errors.New("campaign batch size must be in 1..100")
	}
	if lease <= 0 {
		return nil, errors.New("campaign claim lease must be positive")
	}

	var claimed *entity.EmailCampaignBatch
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Re-evaluate the empty user predicate through the one compliance choke
		// point. Rows whose first POST may already have happened are intentionally
		// immutable: replay must reproduce the exact batch for Resend deduplication.
		empty, err := segment.Compile(entity.SegmentPredicate{})
		if err != nil {
			return err
		}

		recoveryCampaign, err := storeutil.QueryNamedOne[struct {
			ID    int    `db:"id"`
			Topic string `db:"topic"`
		}](ctx, rep.DB(), `
			SELECT ec.id, ec.topic
			FROM email_campaign ec
			WHERE EXISTS (
			    SELECT 1 FROM email_campaign_recipient ecr
			    WHERE ecr.campaign_id = ec.id
			      AND ecr.status = 'pending'
			      AND `+campaignRecipientReclaimableSQL+`
			      AND (
			          ec.status = 'sending'
			          OR ec.status = 'cancelled'
			          OR ecr.first_provider_attempt_at IS NOT NULL
			      )
			)
			ORDER BY ec.id
			LIMIT 1
			FOR UPDATE SKIP LOCKED`, map[string]any{})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("lock campaign with recoverable batch: %w", err)
		}
		if err == nil {
			var batchID string
			if err := rep.DB().GetContext(ctx, &batchID, `
				SELECT dispatch_batch_id
				FROM email_campaign_recipient
				WHERE campaign_id = ?
				  AND status = 'pending'
				  AND dispatch_batch_id IS NOT NULL
				  AND next_attempt_at <= UTC_TIMESTAMP(6)
				  AND (claim_token IS NULL OR claim_expires_at < UTC_TIMESTAMP(6))
				ORDER BY (claim_expires_at IS NULL), claim_expires_at, id
				LIMIT 1
				FOR UPDATE`, recoveryCampaign.ID); err != nil {
				return fmt.Errorf("lock recoverable campaign batch identity: %w", err)
			}
			topic := entity.EmailCampaignTopic(recoveryCampaign.Topic)
			where, _, err := segment.BuildAudiencePredicate(
				empty,
				segment.ComplianceOpts{Topic: &topic},
			)
			if err != nil {
				return err
			}
			if err := skipComplianceChanged(
				ctx,
				rep.DB(),
				recoveryCampaign.ID,
				batchID,
				where,
			); err != nil {
				return err
			}
			claimed, err = reclaimBatch(
				ctx,
				rep.DB(),
				recoveryCampaign.ID,
				topic,
				batchID,
				lease,
			)
			return err
		}

		campaign, err := storeutil.QueryNamedOne[struct {
			ID    int    `db:"id"`
			Topic string `db:"topic"`
		}](ctx, rep.DB(), dispatchCampaignPickerSQL, map[string]any{})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("find campaign ready to dispatch: %w", err)
		}
		topic := entity.EmailCampaignTopic(campaign.Topic)
		where, params, err := segment.BuildAudiencePredicate(
			empty,
			segment.ComplianceOpts{Topic: &topic},
		)
		if err != nil {
			return err
		}
		if err := skipComplianceChanged(ctx, rep.DB(), campaign.ID, "", where); err != nil {
			return err
		}
		params["campaign_id"] = campaign.ID
		params["limit"] = batchSize
		rows, err := storeutil.QueryListNamed[recipientRow](ctx, rep.DB(), `
			SELECT `+recipientColumns+`
			FROM email_campaign_recipient ecr
			JOIN storefront_account sa
			  ON sa.id = ecr.account_id AND sa.email = ecr.email
			LEFT JOIN marketing_account_aggregate maa ON maa.account_id = sa.id
			LEFT JOIN email_suppression es ON es.email = sa.email
			WHERE ecr.campaign_id = :campaign_id
			  AND ecr.status = 'pending'
			  AND `+campaignRecipientFreshClaimableSQL+`
			  AND `+where+`
			ORDER BY ecr.id
			LIMIT :limit
			FOR UPDATE SKIP LOCKED`, params)
		if err != nil {
			return fmt.Errorf("claim fresh campaign recipients: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}

		bid := uuid.NewString()
		token := uuid.NewString()
		key := fmt.Sprintf("grbpwr-campaign-%d-%s", campaign.ID, bid)
		for ordinal := range rows {
			result, err := rep.DB().NamedExecContext(ctx, `
				UPDATE email_campaign_recipient
				SET dispatch_batch_id = :batch_id,
				    batch_ordinal = :ordinal,
				    resend_idempotency_key = :idempotency_key,
				    claim_token = :claim_token,
				    claim_expires_at = DATE_ADD(UTC_TIMESTAMP(6), INTERVAL :lease_micros MICROSECOND)
				WHERE id = :id AND status = 'pending'
				  AND dispatch_batch_id IS NULL`,
				map[string]any{
					"batch_id":        bid,
					"ordinal":         ordinal,
					"idempotency_key": key,
					"claim_token":     token,
					"lease_micros":    lease.Microseconds(),
					"id":              rows[ordinal].ID,
				})
			if err := transitionRowResult("claim fresh campaign recipient", rows[ordinal].ID, result, err); err != nil {
				return err
			}
			rows[ordinal].DispatchBatchID = sql.NullString{String: bid, Valid: true}
			rows[ordinal].BatchOrdinal = sql.NullInt64{Int64: int64(ordinal), Valid: true}
			rows[ordinal].ResendIdempotencyKey = sql.NullString{String: key, Valid: true}
			rows[ordinal].ClaimToken = sql.NullString{String: token, Valid: true}
		}
		claimed = batchFromRows(campaign.ID, topic, bid, key, token, rows)
		return nil
	})
	return claimed, err
}

func skipComplianceChanged(
	ctx context.Context,
	db dependency.DB,
	campaignID int,
	batchID string,
	where string,
) error {
	params := map[string]any{"campaign_id": campaignID, "batch_id": batchID}
	batchClause := "ecr.dispatch_batch_id IS NULL"
	if batchID != "" {
		batchClause = "ecr.dispatch_batch_id = :batch_id"
	}
	_, err := db.NamedExecContext(ctx, `
		UPDATE email_campaign_recipient ecr
		LEFT JOIN storefront_account sa
		  ON sa.id = ecr.account_id AND sa.email = ecr.email
		LEFT JOIN marketing_account_aggregate maa ON maa.account_id = sa.id
		LEFT JOIN email_suppression es ON es.email = sa.email
		SET ecr.status = 'skipped',
		    ecr.error_code = 'compliance_changed',
		    ecr.last_error = NULL,
		    ecr.completed_at = UTC_TIMESTAMP(6),
		    ecr.claim_token = NULL,
		    ecr.claim_expires_at = NULL
		WHERE ecr.campaign_id = :campaign_id
		  AND ecr.status = 'pending'
		  AND ecr.first_provider_attempt_at IS NULL
		  AND `+batchClause+`
		  AND COALESCE((`+where+`), 0) = 0`, params)
	if err != nil {
		return fmt.Errorf("skip campaign recipients whose compliance changed: %w", err)
	}
	return nil
}

func reclaimBatch(
	ctx context.Context,
	db dependency.DB,
	campaignID int,
	topic entity.EmailCampaignTopic,
	batchID string,
	lease time.Duration,
) (*entity.EmailCampaignBatch, error) {
	var alreadySent int
	if err := db.GetContext(ctx, &alreadySent, `
		SELECT COUNT(*) FROM email_campaign_recipient
		WHERE dispatch_batch_id = ? AND status = 'sent'`, batchID); err != nil {
		return nil, fmt.Errorf("check recoverable campaign batch sent rows: %w", err)
	}
	if alreadySent > 0 {
		return nil, fmt.Errorf("fatal campaign batch %s invariant: %d sent rows coexist with pending recovery rows",
			batchID, alreadySent)
	}
	rows, err := storeutil.QueryListNamed[recipientRow](ctx, db, `
		SELECT `+recipientColumns+`
		FROM email_campaign_recipient
		WHERE dispatch_batch_id = :batch_id
		  AND status = 'pending'
		  AND next_attempt_at <= UTC_TIMESTAMP(6)
		  AND (claim_token IS NULL OR claim_expires_at < UTC_TIMESTAMP(6))
		ORDER BY batch_ordinal
		FOR UPDATE SKIP LOCKED`, map[string]any{"batch_id": batchID})
	if err != nil {
		return nil, fmt.Errorf("lock recoverable campaign batch: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	key := rows[0].ResendIdempotencyKey.String
	if key == "" {
		return nil, fmt.Errorf("campaign batch %s has no idempotency key", batchID)
	}
	token := uuid.NewString()
	result, err := db.NamedExecContext(ctx, `
		UPDATE email_campaign_recipient
		SET claim_token = :claim_token,
		    claim_expires_at = DATE_ADD(UTC_TIMESTAMP(6), INTERVAL :lease_micros MICROSECOND)
		WHERE dispatch_batch_id = :batch_id
		  AND status = 'pending'
		  AND (claim_token IS NULL OR claim_expires_at < UTC_TIMESTAMP(6))`,
		map[string]any{
			"claim_token":  token,
			"lease_micros": lease.Microseconds(),
			"batch_id":     batchID,
		})
	if err != nil {
		return nil, fmt.Errorf("renew campaign batch claim: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("renew campaign batch claim rows: %w", err)
	}
	if affected != int64(len(rows)) {
		return nil, fmt.Errorf("campaign batch %s claim invariant: updated %d of %d rows",
			batchID, affected, len(rows))
	}
	for i := range rows {
		if rows[i].ResendIdempotencyKey.String != key {
			return nil, fmt.Errorf("campaign batch %s carries mixed idempotency keys", batchID)
		}
		rows[i].ClaimToken = sql.NullString{String: token, Valid: true}
	}
	return batchFromRows(campaignID, topic, batchID, key, token, rows), nil
}

func batchFromRows(
	campaignID int,
	topic entity.EmailCampaignTopic,
	batchID, key, token string,
	rows []recipientRow,
) *entity.EmailCampaignBatch {
	recipients := make([]entity.EmailCampaignRecipient, len(rows))
	for i := range rows {
		recipients[i] = recipientRowToEntity(rows[i])
	}
	return &entity.EmailCampaignBatch{
		CampaignID:     campaignID,
		Topic:          topic,
		BatchID:        batchID,
		IdempotencyKey: key,
		ClaimToken:     token,
		Recipients:     recipients,
	}
}

func transitionRowResult(action string, id uint64, result sql.Result, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", action, err)
	}
	if n != 1 {
		return fmt.Errorf("%s invariant for recipient %d: affected %d rows", action, id, n)
	}
	return nil
}

func (s *Store) SaveEmailCampaignRecipientPayload(
	ctx context.Context,
	recipientID uint64,
	batchID, claimToken, unsubscribeURL string,
	payloadSHA256 []byte,
) error {
	if len(payloadSHA256) != 32 {
		return errors.New("campaign payload sha256 must be 32 bytes")
	}
	result, err := s.DB.NamedExecContext(ctx, `
		UPDATE email_campaign_recipient
		SET unsubscribe_url = CASE
		        WHEN unsubscribe_url IS NULL THEN :unsubscribe_url
		        ELSE unsubscribe_url
		    END,
		    payload_sha256 = CASE
		        WHEN payload_sha256 IS NULL THEN :payload_sha256
		        ELSE payload_sha256
		    END
		WHERE id = :id AND status = 'pending'
		  AND dispatch_batch_id = :batch_id AND claim_token = :claim_token
		  AND (unsubscribe_url IS NULL OR unsubscribe_url = :unsubscribe_url)
		  AND (payload_sha256 IS NULL OR payload_sha256 = :payload_sha256)`,
		map[string]any{
			"id":              recipientID,
			"batch_id":        batchID,
			"claim_token":     claimToken,
			"unsubscribe_url": unsubscribeURL,
			"payload_sha256":  payloadSHA256,
		})
	return transitionRowResult("save campaign recipient payload", recipientID, result, err)
}

func (s *Store) MarkEmailCampaignBatchProviderAttempt(
	ctx context.Context,
	batchID, claimToken string,
) error {
	result, err := s.DB.NamedExecContext(ctx, `
		UPDATE email_campaign_recipient
		SET attempt_count = attempt_count + 1,
		    first_provider_attempt_at = COALESCE(first_provider_attempt_at, UTC_TIMESTAMP(6)),
		    last_attempt_at = UTC_TIMESTAMP(6)
		WHERE dispatch_batch_id = :batch_id
		  AND claim_token = :claim_token AND status = 'pending'`,
		map[string]any{"batch_id": batchID, "claim_token": claimToken})
	if err != nil {
		return fmt.Errorf("mark campaign batch provider attempt: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark campaign batch provider attempt rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("mark campaign batch provider attempt invariant: batch %s is not owned", batchID)
	}
	return nil
}

func (s *Store) ReleaseEmailCampaignBatch(
	ctx context.Context,
	batchID, claimToken string,
	nextAttemptAt *time.Time,
	errorCode, lastError *string,
) error {
	result, err := s.DB.NamedExecContext(ctx, `
		UPDATE email_campaign_recipient
		SET claim_token = NULL, claim_expires_at = NULL,
		    next_attempt_at = COALESCE(:next_attempt_at, next_attempt_at),
		    error_code = :error_code, last_error = :last_error
		WHERE dispatch_batch_id = :batch_id
		  AND claim_token = :claim_token AND status = 'pending'`,
		map[string]any{
			"batch_id":        batchID,
			"claim_token":     claimToken,
			"next_attempt_at": nextAttemptAt,
			"error_code":      errorCode,
			"last_error":      lastError,
		})
	if err != nil {
		return fmt.Errorf("release campaign batch: %w", err)
	}
	if _, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("release campaign batch rows: %w", err)
	}
	return nil
}

func (s *Store) CompleteEmailCampaignBatch(
	ctx context.Context,
	batchID, claimToken string,
	status entity.EmailCampaignRecipientStatus,
	errorCode string,
	lastError *string,
) error {
	if status != entity.EmailCampaignRecipientStatusFailed &&
		status != entity.EmailCampaignRecipientStatusSkipped {
		return errors.New("campaign batch terminal status must be failed or skipped")
	}
	result, err := s.DB.NamedExecContext(ctx, `
		UPDATE email_campaign_recipient
		SET status = :status, error_code = :error_code, last_error = :last_error,
		    completed_at = UTC_TIMESTAMP(6), claim_token = NULL, claim_expires_at = NULL
		WHERE dispatch_batch_id = :batch_id
		  AND claim_token = :claim_token AND status = 'pending'`,
		map[string]any{
			"status":      string(status),
			"error_code":  errorCode,
			"last_error":  lastError,
			"batch_id":    batchID,
			"claim_token": claimToken,
		})
	if err != nil {
		return fmt.Errorf("complete campaign batch: %w", err)
	}
	if _, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("complete campaign batch rows: %w", err)
	}
	return nil
}

// RecordEmailCampaignBatchAccepted atomically flips the complete acknowledged
// batch. Incident guarantee (a): a crash after POST and before this commit
// replays the identical payload with the identical batch key, Resend dedupes it,
// and these pending-only guards prevent a second state transition/send.
func (s *Store) RecordEmailCampaignBatchAccepted(
	ctx context.Context,
	batchID, claimToken string,
	providerIDs []string,
) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		rows, err := storeutil.QueryListNamed[recipientRow](ctx, rep.DB(), `
			SELECT `+recipientColumns+`
			FROM email_campaign_recipient
			WHERE dispatch_batch_id = :batch_id
			ORDER BY batch_ordinal
			FOR UPDATE`, map[string]any{"batch_id": batchID})
		if err != nil {
			return fmt.Errorf("lock accepted campaign batch: %w", err)
		}
		active := make([]recipientRow, 0, len(rows))
		for _, row := range rows {
			if row.Status == string(entity.EmailCampaignRecipientStatusPending) ||
				row.Status == string(entity.EmailCampaignRecipientStatusSent) {
				active = append(active, row)
			}
		}
		if len(providerIDs) != len(active) {
			return fmt.Errorf("campaign batch %s provider id length mismatch: got %d want %d",
				batchID, len(providerIDs), len(active))
		}
		for i := range active {
			row := active[i]
			providerID := providerIDs[i]
			if row.Status == string(entity.EmailCampaignRecipientStatusSent) {
				if row.ResendEmailID.Valid && row.ResendEmailID.String == providerID {
					continue
				}
				return fmt.Errorf("fatal campaign recipient %d ack invariant: sent provider_id=%q want=%q",
					row.ID, row.ResendEmailID.String, providerID)
			}
			result, err := rep.DB().NamedExecContext(ctx, `
				UPDATE email_campaign_recipient
				SET status = 'sent', resend_email_id = :provider_id,
				    sent_at = UTC_TIMESTAMP(6), completed_at = UTC_TIMESTAMP(6),
				    claim_token = NULL, claim_expires_at = NULL,
				    error_code = NULL, last_error = NULL
				WHERE id = :id AND status = 'pending'
				  AND dispatch_batch_id = :batch_id`,
				map[string]any{
					"id":          row.ID,
					"provider_id": providerID,
					"batch_id":    batchID,
				})
			if err != nil {
				return fmt.Errorf("record accepted campaign recipient %d: %w", row.ID, err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("record accepted campaign recipient %d rows: %w", row.ID, err)
			}
			if affected == 1 {
				continue
			}
			current, getErr := storeutil.QueryNamedOne[recipientRow](ctx, rep.DB(), `
				SELECT `+recipientColumns+`
				FROM email_campaign_recipient WHERE id = :id`,
				map[string]any{"id": row.ID})
			if getErr != nil {
				return fmt.Errorf("verify accepted campaign recipient %d: %w", row.ID, getErr)
			}
			if current.Status == string(entity.EmailCampaignRecipientStatusSent) &&
				current.ResendEmailID.Valid &&
				current.ResendEmailID.String == providerID {
				continue
			}
			return fmt.Errorf("fatal campaign recipient %d ack invariant: status=%s provider_id=%q want=%q",
				row.ID, current.Status, current.ResendEmailID.String, providerID)
		}
		return nil
	})
}

func (s *Store) QuarantineEmailCampaignRecipient(
	ctx context.Context,
	recipientID uint64,
	batchID, claimToken, errorCode, message string,
) error {
	result, err := s.DB.NamedExecContext(ctx, `
		UPDATE email_campaign_recipient
		SET status = 'failed', error_code = :error_code, last_error = :last_error,
		    completed_at = UTC_TIMESTAMP(6), claim_token = NULL, claim_expires_at = NULL
		WHERE id = :id AND status = 'pending'
		  AND dispatch_batch_id = :batch_id AND claim_token = :claim_token`,
		map[string]any{
			"id":          recipientID,
			"batch_id":    batchID,
			"claim_token": claimToken,
			"error_code":  errorCode,
			"last_error":  message,
		})
	return transitionRowResult("quarantine campaign recipient", recipientID, result, err)
}

func (s *Store) VerifyEmailCampaignRecipientPayload(
	ctx context.Context,
	recipientID uint64,
	payloadSHA256 []byte,
) (bool, error) {
	var stored []byte
	if err := s.DB.GetContext(ctx, &stored,
		`SELECT payload_sha256 FROM email_campaign_recipient WHERE id = ?`, recipientID); err != nil {
		return false, fmt.Errorf("load campaign recipient payload hash: %w", err)
	}
	return len(stored) == 32 && bytes.Equal(stored, payloadSHA256), nil
}

func (s *Store) FinalizeEmailCampaigns(ctx context.Context) (int64, error) {
	result, err := s.DB.ExecContext(ctx, `
		UPDATE email_campaign ec
		SET ec.status = 'sent', ec.sent_at = UTC_TIMESTAMP(6), ec.dispatch_error = NULL
		WHERE ec.status = 'sending'
		  AND ec.audience_materialized_at IS NOT NULL
		  AND NOT EXISTS (
		      SELECT 1 FROM email_campaign_recipient ecr
		      WHERE ecr.campaign_id = ec.id AND ecr.status = 'pending'
		  )`)
	if err != nil {
		return 0, fmt.Errorf("finalize email campaigns: %w", err)
	}
	return result.RowsAffected()
}

func (s *Store) GetEmailCampaignDispatchStatus(
	ctx context.Context,
	campaignID int,
) (*entity.EmailCampaignDispatchStatus, error) {
	row, err := storeutil.QueryNamedOne[struct {
		CampaignID             int            `db:"campaign_id"`
		Status                 string         `db:"status"`
		AudienceMaterializedAt sql.NullTime   `db:"audience_materialized_at"`
		DispatchError          sql.NullString `db:"dispatch_error"`
		RecipientCount         int            `db:"recipient_count"`
		Pending                int            `db:"pending"`
		Accepted               int            `db:"accepted"`
		Failed                 int            `db:"failed"`
		Skipped                int            `db:"skipped"`
	}](ctx, s.DB, `
		SELECT ec.id AS campaign_id, ec.status, ec.audience_materialized_at,
		       ec.dispatch_error, COALESCE(ec.recipient_count, 0) AS recipient_count,
		       COALESCE(SUM(ecr.status = 'pending'), 0) AS pending,
		       COALESCE(SUM(ecr.status = 'sent'), 0) AS accepted,
		       COALESCE(SUM(ecr.status = 'failed'), 0) AS failed,
		       COALESCE(SUM(ecr.status = 'skipped'), 0) AS skipped
		FROM email_campaign ec
		LEFT JOIN email_campaign_recipient ecr ON ecr.campaign_id = ec.id
		WHERE ec.id = :campaign_id
		GROUP BY ec.id, ec.status, ec.audience_materialized_at,
		         ec.dispatch_error, ec.recipient_count`,
		map[string]any{"campaign_id": campaignID})
	if err != nil {
		return nil, fmt.Errorf("get campaign dispatch status: %w", err)
	}
	out := &entity.EmailCampaignDispatchStatus{
		CampaignID: campaignID,
		Status:     entity.EmailCampaignStatus(row.Status),
		Counts: entity.EmailCampaignDispatchCounts{
			RecipientCount: row.RecipientCount,
			Pending:        row.Pending,
			Accepted:       row.Accepted,
			Failed:         row.Failed,
			Skipped:        row.Skipped,
		},
	}
	if row.AudienceMaterializedAt.Valid {
		value := row.AudienceMaterializedAt.Time
		out.AudienceMaterializedAt = &value
	}
	if row.DispatchError.Valid {
		value := row.DispatchError.String
		out.DispatchError = &value
	}
	return out, nil
}

func (s *Store) GetEmailCampaignRecipients(
	ctx context.Context,
	campaignID int,
	afterID uint64,
	limit int,
) (*entity.EmailCampaignRecipientPage, error) {
	if limit <= 0 || limit > 250 {
		return nil, errors.New("recipient page limit must be in 1..250")
	}
	rows, err := storeutil.QueryListNamed[recipientRow](ctx, s.DB, `
		SELECT `+recipientColumns+`
		FROM email_campaign_recipient
		WHERE campaign_id = :campaign_id AND id > :after_id
		ORDER BY id LIMIT :limit`,
		map[string]any{
			"campaign_id": campaignID,
			"after_id":    afterID,
			"limit":       limit,
		})
	if err != nil {
		return nil, fmt.Errorf("list campaign recipients: %w", err)
	}
	out := &entity.EmailCampaignRecipientPage{
		Recipients: make([]entity.EmailCampaignRecipient, len(rows)),
	}
	for i := range rows {
		out.Recipients[i] = recipientRowToEntity(rows[i])
		out.NextID = rows[i].ID
	}
	if len(rows) < limit {
		out.NextID = 0
	}
	return out, nil
}
