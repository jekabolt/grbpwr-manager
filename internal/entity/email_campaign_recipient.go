package entity

import "time"

type EmailCampaignRecipientStatus string

const (
	EmailCampaignRecipientStatusPending EmailCampaignRecipientStatus = "pending"
	EmailCampaignRecipientStatusSent    EmailCampaignRecipientStatus = "sent"
	EmailCampaignRecipientStatusFailed  EmailCampaignRecipientStatus = "failed"
	EmailCampaignRecipientStatusSkipped EmailCampaignRecipientStatus = "skipped"
)

type EmailCampaignCohort string

const (
	EmailCampaignCohortAB        EmailCampaignCohort = "ab"
	EmailCampaignCohortRemainder EmailCampaignCohort = "remainder"
)

type EmailCampaignRecipient struct {
	ID                     uint64
	CampaignID             int
	AccountID              *int
	Email                  string
	LanguageID             int
	VariantID              *int
	Cohort                 EmailCampaignCohort
	Status                 EmailCampaignRecipientStatus
	UnsubscribeURL         *string
	DispatchBatchID        *string
	BatchOrdinal           *int
	ResendIdempotencyKey   *string
	ClaimToken             *string
	ClaimExpiresAt         *time.Time
	PayloadSHA256          []byte
	ResendEmailID          *string
	AttemptCount           int
	NextAttemptAt          *time.Time
	FirstProviderAttemptAt *time.Time
	LastAttemptAt          *time.Time
	ErrorCode              *string
	LastError              *string
	SentAt                 *time.Time
	CompletedAt            *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type EmailCampaignRenderSnapshot struct {
	CampaignID     int
	VariantID      int
	LanguageID     int
	Subject        string
	HTMLTemplate   string
	TextTemplate   string
	FromValue      string
	ReplyTo        *string
	PayloadVersion int
	CreatedAt      time.Time
}

type EmailCampaignDispatchCounts struct {
	RecipientCount int
	Pending        int
	Accepted       int
	Failed         int
	Skipped        int
}

type EmailCampaignDispatchStatus struct {
	CampaignID             int
	Status                 EmailCampaignStatus
	AudienceMaterializedAt *time.Time
	DispatchError          *string
	Counts                 EmailCampaignDispatchCounts
}

type EmailCampaignRecipientPage struct {
	Recipients []EmailCampaignRecipient
	NextID     uint64
}

type EmailCampaignBatch struct {
	CampaignID     int
	Topic          EmailCampaignTopic
	BatchID        string
	IdempotencyKey string
	ClaimToken     string
	Recipients     []EmailCampaignRecipient
}

type EmailCampaignAudienceCandidate struct {
	AccountID  int
	Email      string
	LanguageID int
}

type EmailCampaignAudienceAssignment struct {
	Cohort    EmailCampaignCohort
	VariantID *int
}

type EmailCampaignVariantAssigner func(
	campaignID int,
	normalizedEmail string,
	abEnabled bool,
	abTestPct int,
	variantIDs []int,
) EmailCampaignAudienceAssignment

type EmailCampaignFanoutPageResult struct {
	CampaignID   int
	Inserted     int
	Cursor       int
	Materialized bool
}
