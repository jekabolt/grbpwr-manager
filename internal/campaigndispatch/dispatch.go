package campaigndispatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/mail/campaignrender"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
)

// Keep automatic recovery strictly below 12h. Operations must verify that
// Resend's documented idempotency retention remains at least this long before
// increasing the horizon.
const automaticRetryHorizon = 12*time.Hour - time.Minute

type renderWarningError struct {
	message string
}

func (e *renderWarningError) Error() string { return e.message }

type payloadMismatchError struct {
	message string
}

func (e *payloadMismatchError) Error() string { return e.message }

// campaignConfigError marks a deterministic, campaign-wide preparation failure
// (unusable envelope, missing variant, unbuildable unsubscribe URL, empty or
// oversized subject). Every retry reproduces it, so the campaign is paused with
// the reason instead of being released for an endless re-claim.
type campaignConfigError struct {
	message string
}

func (e *campaignConfigError) Error() string { return e.message }

// deterministicPrepareFailure reports the admin-visible reason for a prepare
// error that cannot succeed on retry.
func deterministicPrepareFailure(err error) (string, bool) {
	var warningErr *renderWarningError
	if errors.As(err, &warningErr) {
		return warningErr.Error(), true
	}
	var configErr *campaignConfigError
	if errors.As(err, &configErr) {
		return configErr.Error(), true
	}
	return "", false
}

func (w *Worker) dispatchOne(ctx context.Context) (bool, error) {
	batch, err := w.repo.Campaigns().ClaimEmailCampaignBatch(
		ctx,
		w.c.BatchSize,
		w.c.ClaimLease,
	)
	if err != nil || batch == nil {
		return false, err
	}
	store := w.repo.Campaigns()

	campaign, err := store.GetEmailCampaignByID(ctx, batch.CampaignID)
	if err != nil {
		_ = store.ReleaseEmailCampaignBatch(ctx, batch.BatchID, batch.ClaimToken, nil, nil, nil)
		return false, fmt.Errorf("load claimed campaign: %w", err)
	}
	oldestProviderAttempt, maxAttempt, providerMayHaveAccepted := batchProviderAttemptState(batch)
	if !providerMayHaveAccepted {
		switch campaign.Status {
		case entity.EmailCampaignStatusPaused:
			_ = store.ReleaseEmailCampaignBatch(ctx, batch.BatchID, batch.ClaimToken, nil, nil, nil)
			return false, nil
		case entity.EmailCampaignStatusCancelled:
			err := store.CompleteEmailCampaignBatch(
				ctx,
				batch.BatchID,
				batch.ClaimToken,
				entity.EmailCampaignRecipientStatusSkipped,
				"campaign_cancelled",
				nil,
			)
			return true, err
		}
	}
	if w.mailer.CampaignSendingDisabled() {
		return w.holdBatchForDisabledMailer(ctx, campaign, batch, providerMayHaveAccepted)
	}
	if providerMayHaveAccepted &&
		(time.Since(oldestProviderAttempt) >= automaticRetryHorizon ||
			maxAttempt >= w.c.MaxAttempts) {
		// Never re-POST marketing mail once the key's safe retry limits are
		// exhausted: beyond the provider's idempotency horizon, a replay can be
		// a fresh send, so a possible miss is safer than a possible double-send.
		message := fmt.Sprintf(
			"automatic replay blocked after ambiguous provider acknowledgement: first attempt %s, attempt_count %d, max_attempts %d",
			oldestProviderAttempt.UTC().Format(time.RFC3339),
			maxAttempt,
			w.c.MaxAttempts,
		)
		err := store.CompleteEmailCampaignBatch(
			ctx,
			batch.BatchID,
			batch.ClaimToken,
			entity.EmailCampaignRecipientStatusFailed,
			"ambiguous_provider_ack",
			&message,
		)
		return true, err
	}

	requests, err := w.prepareBatch(ctx, campaign, batch)
	if err != nil {
		var payloadErr *payloadMismatchError
		if errors.As(err, &payloadErr) {
			message := payloadErr.Error()
			completeErr := store.CompleteEmailCampaignBatch(
				ctx,
				batch.BatchID,
				batch.ClaimToken,
				entity.EmailCampaignRecipientStatusFailed,
				"payload_hash_mismatch",
				&message,
			)
			return true, completeErr
		}
		message, deterministic := deterministicPrepareFailure(err)
		paused := false
		if deterministic {
			if pauseErr := store.PauseEmailCampaign(ctx, campaign.ID, &message); pauseErr != nil {
				if !errors.Is(pauseErr, context.Canceled) {
					err = fmt.Errorf("%v; pause campaign: %w", err, pauseErr)
				}
			} else {
				paused = true
			}
		}
		_ = store.ReleaseEmailCampaignBatch(ctx, batch.BatchID, batch.ClaimToken, nil, nil, nil)
		if paused {
			// The released batch keeps its dispatch_batch_id and the recovery picker
			// claims it before any fresh batch of any campaign, so returning an error
			// here would starve every other campaign's dispatch, A/B promotion and
			// finalization. The campaign is out of rotation with the reason in
			// dispatch_error: keep draining the rest of the tick.
			slog.ErrorContext(ctx, "email campaign paused by dispatcher",
				slog.Int("campaign_id", campaign.ID),
				slog.String("err", message))
			return true, nil
		}
		return false, err
	}
	if len(requests) == 0 {
		return true, nil
	}

	providerIDs, err := w.mailer.SendCampaignBatch(
		ctx,
		requests,
		batch.IdempotencyKey,
		func() error {
			return store.MarkEmailCampaignBatchProviderAttempt(
				ctx,
				batch.BatchID,
				batch.ClaimToken,
			)
		},
	)
	if err == nil {
		if err := store.RecordEmailCampaignBatchAccepted(
			ctx,
			batch.BatchID,
			batch.ClaimToken,
			providerIDs,
		); err != nil {
			// Crash/commit failure recovery re-posts the same request array with
			// the same key; Resend returns the same ordered ids.
			return false, fmt.Errorf("record campaign batch acceptance: %w", err)
		}
		return true, nil
	}
	if errors.Is(err, mail.ErrCampaignBudgetUnavailable) {
		if releaseErr := store.ReleaseEmailCampaignBatch(
			ctx, batch.BatchID, batch.ClaimToken, nil, nil, nil,
		); releaseErr != nil {
			return false, releaseErr
		}
		return false, nil
	}
	if errors.Is(err, mail.ErrSuppressedDisabled) {
		// The kill switch flipped after the pre-flight check above: no POST happened,
		// so hold the batch exactly like the pre-flight path instead of skipping it.
		return w.holdBatchForDisabledMailer(ctx, campaign, batch, providerMayHaveAccepted)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// Leave the persisted claim/batch untouched; lease recovery owns it.
		return false, err
	}
	var beforePostErr *mail.CampaignBeforePostError
	if errors.As(err, &beforePostErr) {
		_ = store.ReleaseEmailCampaignBatch(
			ctx,
			batch.BatchID,
			batch.ClaimToken,
			nil,
			nil,
			nil,
		)
		return false, err
	}
	return false, w.classifyBatchFailure(ctx, batch, err)
}

// holdBatchForDisabledMailer keeps a claimed batch replayable while the mailer
// kill switch is on. Completing it as skipped would be terminal and lossy: rows
// whose first POST may already have been accepted would never get their
// resend_email_id (engagement webhooks are attributed by that id alone, so their
// delivery facts would be unattributable), and rows that were never attempted
// could not be resumed once sending is re-enabled. The campaign is paused so the
// hold is visible and an operator re-arms the send explicitly.
func (w *Worker) holdBatchForDisabledMailer(
	ctx context.Context,
	campaign *entity.EmailCampaignFull,
	batch *entity.EmailCampaignBatch,
	providerMayHaveAccepted bool,
) (bool, error) {
	store := w.repo.Campaigns()
	code := "mailer_disabled"
	message := "campaign sending is disabled: batch held pending"
	if providerMayHaveAccepted {
		message = "campaign sending is disabled: batch held for provider acknowledgement replay"
	}
	if campaign.Status == entity.EmailCampaignStatusSending {
		if err := store.PauseEmailCampaign(ctx, campaign.ID, &message); err != nil {
			if errors.Is(err, context.Canceled) {
				return false, err
			}
			slog.ErrorContext(ctx, "can't pause campaign while sending is disabled",
				slog.Int("campaign_id", campaign.ID),
				slog.String("err", err.Error()))
		}
	}
	if err := store.ReleaseEmailCampaignBatch(
		ctx,
		batch.BatchID,
		batch.ClaimToken,
		nil,
		&code,
		&message,
	); err != nil {
		return false, err
	}
	return false, nil
}

func batchProviderAttemptState(batch *entity.EmailCampaignBatch) (time.Time, int, bool) {
	var oldest time.Time
	maxAttempt := 0
	for i := range batch.Recipients {
		recipient := &batch.Recipients[i]
		if recipient.AttemptCount > maxAttempt {
			maxAttempt = recipient.AttemptCount
		}
		if recipient.FirstProviderAttemptAt != nil &&
			(oldest.IsZero() || recipient.FirstProviderAttemptAt.Before(oldest)) {
			oldest = *recipient.FirstProviderAttemptAt
		}
	}
	return oldest, maxAttempt, !oldest.IsZero()
}

func (w *Worker) prepareBatch(
	ctx context.Context,
	campaign *entity.EmailCampaignFull,
	batch *entity.EmailCampaignBatch,
) ([]resend.SendEmailRequest, error) {
	langs, err := w.repo.Language().GetAllLanguages(ctx)
	if err != nil {
		return nil, fmt.Errorf("load campaign render languages: %w", err)
	}
	fromValue, replyTo, err := w.mailer.CampaignEnvelope(campaign)
	if err != nil {
		return nil, &campaignConfigError{message: err.Error()}
	}
	requests := make([]resend.SendEmailRequest, 0, len(batch.Recipients))
	for i := range batch.Recipients {
		recipient := &batch.Recipients[i]
		if recipient.VariantID == nil {
			continue
		}
		// The only recipient-scoped payload defect is an unusable address: every
		// other BuildCampaignSendRequest rejection below is campaign-wide, so it
		// must pause the campaign instead of burning the audience batch by batch.
		if _, addrErr := mail.NormalizeEmailAddress(recipient.Email); addrErr != nil {
			message := fmt.Sprintf("invalid campaign recipient: %v", addrErr)
			if recipient.FirstProviderAttemptAt != nil {
				return nil, &payloadMismatchError{message: message}
			}
			if quarantineErr := w.repo.Campaigns().QuarantineEmailCampaignRecipient(
				ctx,
				recipient.ID,
				batch.BatchID,
				batch.ClaimToken,
				"invalid_recipient_payload",
				message,
			); quarantineErr != nil {
				return nil, quarantineErr
			}
			continue
		}
		snapshot, err := w.ensureSnapshot(
			ctx,
			campaign,
			*recipient.VariantID,
			recipient.LanguageID,
			langs,
			fromValue,
			replyTo,
		)
		if err != nil {
			return nil, err
		}
		unsubscribeURL := ""
		if recipient.UnsubscribeURL != nil {
			unsubscribeURL = *recipient.UnsubscribeURL
		} else {
			unsubscribeURL, err = w.mailer.CampaignUnsubscribeURL(batch.Topic, recipient.Email)
			if err != nil {
				return nil, &campaignConfigError{
					message: fmt.Sprintf("build campaign unsubscribe URL: %v", err),
				}
			}
		}
		rendered, err := campaignrender.ResolvePrepared(campaignrender.Rendered{
			HTML: snapshot.HTMLTemplate,
			Text: snapshot.TextTemplate,
		}, unsubscribeURL)
		if err != nil {
			return nil, &payloadMismatchError{message: err.Error()}
		}
		request, err := w.mailer.BuildCampaignSendRequest(
			recipient.Email,
			*snapshot,
			rendered.HTML,
			rendered.Text,
			unsubscribeURL,
		)
		if err != nil {
			if recipient.FirstProviderAttemptAt != nil {
				return nil, &payloadMismatchError{message: err.Error()}
			}
			return nil, &campaignConfigError{message: err.Error()}
		}
		payload, err := json.Marshal(request)
		if err != nil {
			return nil, fmt.Errorf("marshal campaign provider payload: %w", err)
		}
		digest := sha256.Sum256(payload)
		if len(recipient.PayloadSHA256) > 0 {
			if len(recipient.PayloadSHA256) != sha256.Size ||
				!bytes.Equal(recipient.PayloadSHA256, digest[:]) {
				return nil, &payloadMismatchError{message: "reconstructed provider payload differs from persisted hash"}
			}
		} else if err := w.repo.Campaigns().SaveEmailCampaignRecipientPayload(
			ctx,
			recipient.ID,
			batch.BatchID,
			batch.ClaimToken,
			unsubscribeURL,
			digest[:],
		); err != nil {
			return nil, fmt.Errorf("persist campaign recipient payload: %w", err)
		}
		requests = append(requests, request)
	}
	return requests, nil
}

func (w *Worker) ensureSnapshot(
	ctx context.Context,
	campaign *entity.EmailCampaignFull,
	variantID, languageID int,
	langs []entity.Language,
	fromValue string,
	replyTo *string,
) (*entity.EmailCampaignRenderSnapshot, error) {
	store := w.repo.Campaigns()
	snapshot, err := store.GetEmailCampaignRenderSnapshot(
		ctx,
		campaign.ID,
		variantID,
		languageID,
	)
	if err == nil {
		return snapshot, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	variant := campaignVariant(campaign.Variants, variantID)
	if variant == nil {
		return nil, &campaignConfigError{
			message: fmt.Sprintf("campaign %d variant %d is missing", campaign.ID, variantID),
		}
	}
	blocks := campaign.Body
	if campaign.ABConfig.Enabled &&
		campaign.ABConfig.Dimension == entity.ABDimensionContent &&
		len(variant.Body) > 0 {
		blocks = variant.Body
	}
	rendered, warnings, err := w.renderer.Prepare(func(unsubscribeURL string) (
		campaignrender.Rendered,
		[]campaignrender.Warning,
		error,
	) {
		return w.renderer.Render(ctx, w.repo, campaignrender.Input{
			Blocks:          blocks,
			BackgroundColor: campaign.BackgroundColor,
			LanguageID:      languageID,
			Langs:           langs,
			UnsubscribeURL:  unsubscribeURL,
			Footer:          w.mailer.CampaignFooterStrings(languageID, langs),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("render campaign snapshot: %w", err)
	}
	if len(warnings) > 0 {
		return nil, &renderWarningError{message: fmt.Sprintf(
			"render dropped product/media block %d: %s",
			warnings[0].BlockIndex,
			warnings[0].Reason,
		)}
	}
	snapshot = &entity.EmailCampaignRenderSnapshot{
		CampaignID:     campaign.ID,
		VariantID:      variantID,
		LanguageID:     languageID,
		Subject:        campaignrender.SelectSubject(variant.SubjectI18n, languageID, langs),
		HTMLTemplate:   rendered.HTML,
		TextTemplate:   rendered.Text,
		FromValue:      fromValue,
		ReplyTo:        replyTo,
		PayloadVersion: 1,
	}
	if err := store.PutEmailCampaignRenderSnapshot(ctx, *snapshot); err != nil {
		return nil, err
	}
	return store.GetEmailCampaignRenderSnapshot(ctx, campaign.ID, variantID, languageID)
}

func campaignVariant(variants []entity.EmailCampaignVariant, id int) *entity.EmailCampaignVariant {
	for i := range variants {
		if variants[i].ID == id {
			return &variants[i]
		}
	}
	return nil
}

func (w *Worker) classifyBatchFailure(
	ctx context.Context,
	batch *entity.EmailCampaignBatch,
	sendErr error,
) error {
	var providerErr *mail.CampaignBatchSendError
	if !errors.As(sendErr, &providerErr) {
		providerErr = &mail.CampaignBatchSendError{Ambiguous: true, Err: sendErr}
	}
	attempt := 1
	var firstAttempt time.Time
	for i := range batch.Recipients {
		if candidate := batch.Recipients[i].AttemptCount + 1; candidate > attempt {
			attempt = candidate
		}
		if batch.Recipients[i].FirstProviderAttemptAt != nil &&
			(firstAttempt.IsZero() || batch.Recipients[i].FirstProviderAttemptAt.Before(firstAttempt)) {
			firstAttempt = *batch.Recipients[i].FirstProviderAttemptAt
		}
	}
	exhausted := attempt >= w.c.MaxAttempts ||
		(!firstAttempt.IsZero() && time.Since(firstAttempt) >= automaticRetryHorizon)
	message := sendErr.Error()
	permanent := providerErr.StatusCode == 400 ||
		providerErr.StatusCode == 401 ||
		providerErr.StatusCode == 403 ||
		providerErr.StatusCode == 422 ||
		(providerErr.StatusCode >= 400 &&
			providerErr.StatusCode < 500 &&
			providerErr.StatusCode != 408 &&
			providerErr.StatusCode != 429)
	if permanent {
		return w.repo.Campaigns().CompleteEmailCampaignBatch(
			ctx,
			batch.BatchID,
			batch.ClaimToken,
			entity.EmailCampaignRecipientStatusFailed,
			"provider_rejected",
			&message,
		)
	}
	if exhausted {
		code := "retry_exhausted"
		if providerErr.Ambiguous {
			code = "ambiguous_provider_ack"
		}
		return w.repo.Campaigns().CompleteEmailCampaignBatch(
			ctx,
			batch.BatchID,
			batch.ClaimToken,
			entity.EmailCampaignRecipientStatusFailed,
			code,
			&message,
		)
	}
	delay := providerErr.RetryAfter
	if delay <= 0 {
		delay = retryDelay(w.c.RetryBase, w.c.RetryMax, attempt, batch.BatchID)
	}
	next := time.Now().UTC().Add(delay)
	code := "provider_transient"
	if providerErr.StatusCode == 429 {
		code = "provider_rate_limited"
	} else if providerErr.Ambiguous {
		code = "ambiguous_provider_ack"
	}
	return w.repo.Campaigns().ReleaseEmailCampaignBatch(
		ctx,
		batch.BatchID,
		batch.ClaimToken,
		&next,
		&code,
		&message,
	)
}

func retryDelay(base, max time.Duration, attempt int, batchID string) time.Duration {
	delay := base
	for i := 1; i < attempt; i++ {
		if delay >= max/2 {
			delay = max
			break
		}
		delay *= 2
	}
	if delay > max {
		delay = max
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", batchID, attempt)))
	value := int64(binary.BigEndian.Uint16(digest[:2])) - 32768
	jitter := time.Duration(value) * delay / (32768 * 5)
	delay += jitter
	if delay < base/2 {
		return base / 2
	}
	if delay > max {
		return max
	}
	return delay
}
