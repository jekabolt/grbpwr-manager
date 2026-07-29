package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
)

var ErrSuppressedDisabled = errors.New("campaign send suppressed because mailer is disabled")

type CampaignBatchSendError struct {
	StatusCode int
	RetryAfter time.Duration
	Ambiguous  bool
	Body       string
	Err        error
}

func (e *CampaignBatchSendError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("campaign batch send: %v", e.Err)
	}
	return fmt.Sprintf("campaign batch send returned HTTP %d: %s", e.StatusCode, e.Body)
}

func (e *CampaignBatchSendError) Unwrap() error { return e.Err }

type CampaignBeforePostError struct {
	Err error
}

func (e *CampaignBeforePostError) Error() string {
	return fmt.Sprintf("persist campaign batch provider attempt: %v", e.Err)
}

func (e *CampaignBeforePostError) Unwrap() error { return e.Err }

func idempotencyKeyEditor(key string) resend.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("Idempotency-Key", key)
		return nil
	}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

// SendCampaignBatch posts one persisted provider batch. The caller must reuse
// both requests and idempotencyKey on every retry.
func (m *Mailer) SendCampaignBatch(
	ctx context.Context,
	requests []resend.SendEmailRequest,
	idempotencyKey string,
	beforePost func() error,
) ([]string, error) {
	if m.c.Disabled {
		return nil, ErrSuppressedDisabled
	}
	if len(requests) == 0 || len(requests) > 100 {
		return nil, fmt.Errorf("campaign batch must contain 1..100 requests")
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return nil, errors.New("campaign batch idempotency key is required")
	}
	if !m.budget.TryCampaign() {
		return nil, ErrCampaignBudgetUnavailable
	}
	if beforePost == nil {
		return nil, errors.New("campaign batch before-POST persistence callback is required")
	}
	if err := beforePost(); err != nil {
		return nil, &CampaignBeforePostError{Err: err}
	}

	resp, err := m.cli.PostEmailsBatch(ctx, requests, idempotencyKeyEditor(idempotencyKey))
	if err != nil {
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		return nil, &CampaignBatchSendError{Ambiguous: true, Err: err}
	}
	if resp == nil {
		return nil, &CampaignBatchSendError{Ambiguous: true, Err: errors.New("nil provider response")}
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if readErr != nil {
		return nil, &CampaignBatchSendError{StatusCode: resp.StatusCode, Ambiguous: true, Err: readErr}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		if resp.StatusCode == http.StatusTooManyRequests {
			if retryAfter <= 0 {
				retryAfter = time.Second
			}
			m.budget.CooldownCampaign(retryAfter)
		}
		return nil, &CampaignBatchSendError{
			StatusCode: resp.StatusCode,
			RetryAfter: retryAfter,
			Body:       string(body),
		}
	}

	var decoded resend.CreateBatchEmailsResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, &CampaignBatchSendError{
			StatusCode: resp.StatusCode,
			Ambiguous:  true,
			Err:        fmt.Errorf("decode provider acknowledgement: %w", err),
		}
	}
	if decoded.Data == nil || len(*decoded.Data) != len(requests) {
		return nil, &CampaignBatchSendError{
			StatusCode: resp.StatusCode,
			Ambiguous:  true,
			Err: fmt.Errorf("provider acknowledgement length %d does not match request length %d",
				func() int {
					if decoded.Data == nil {
						return 0
					}
					return len(*decoded.Data)
				}(),
				len(requests)),
		}
	}
	ids := make([]string, len(requests))
	for i, item := range *decoded.Data {
		if item.Id == nil || strings.TrimSpace(*item.Id) == "" {
			return nil, &CampaignBatchSendError{
				StatusCode: resp.StatusCode,
				Ambiguous:  true,
				Err:        fmt.Errorf("provider acknowledgement %d has no id", i),
			}
		}
		ids[i] = *item.Id
	}
	return ids, nil
}
