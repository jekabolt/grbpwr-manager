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

type EmailCampaignEngagementKind string

const (
	EmailCampaignEngagementDelivered  EmailCampaignEngagementKind = "delivered"
	EmailCampaignEngagementOpened     EmailCampaignEngagementKind = "opened"
	EmailCampaignEngagementClicked    EmailCampaignEngagementKind = "clicked"
	EmailCampaignEngagementBounced    EmailCampaignEngagementKind = "bounced"
	EmailCampaignEngagementComplained EmailCampaignEngagementKind = "complained"
	EmailCampaignEngagementHardFailed EmailCampaignEngagementKind = "hard_failed"
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
	DeliveredAt            *time.Time
	FirstOpenedAt          *time.Time
	LastOpenedAt           *time.Time
	OpenCount              int
	FirstClickedAt         *time.Time
	LastClickedAt          *time.Time
	ClickCount             int
	BouncedAt              *time.Time
	ComplainedAt           *time.Time
	UnsubscribedAt         *time.Time
	HardFailedAt           *time.Time
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

// CampaignMetricCounts is computed from the recipient ledger. The ledger
// remains the only source of truth; these values are never precomputed.
type CampaignMetricCounts struct {
	Total         int64
	Pending       int64
	Sent          int64
	Failed        int64
	Skipped       int64
	Delivered     int64
	UniqueOpened  int64
	TotalOpens    int64
	UniqueClicked int64
	TotalClicks   int64
	Bounced       int64
	Complained    int64
	Unsubscribed  int64
}

type CampaignMetricRates struct {
	DeliveryRate    float64
	OpenRate        float64
	ClickRate       float64
	ClickToOpenRate float64
	BounceRate      float64
	ComplaintRate   float64
}

func campaignMetricRate(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

// Rates derives the campaign rates from unique engagement counts. In
// particular, click-to-open is unique clicks divided by unique opens.
func (c CampaignMetricCounts) Rates() CampaignMetricRates {
	return CampaignMetricRates{
		DeliveryRate:    campaignMetricRate(c.Delivered, c.Sent),
		OpenRate:        campaignMetricRate(c.UniqueOpened, c.Delivered),
		ClickRate:       campaignMetricRate(c.UniqueClicked, c.Delivered),
		ClickToOpenRate: campaignMetricRate(c.UniqueClicked, c.UniqueOpened),
		BounceRate:      campaignMetricRate(c.Bounced, c.Sent),
		ComplaintRate:   campaignMetricRate(c.Complained, c.Sent),
	}
}

type CampaignVariantMetrics struct {
	VariantID int
	Label     string
	Counts    CampaignMetricCounts
	Rates     CampaignMetricRates
}

type CampaignMetrics struct {
	CampaignID int
	Counts     CampaignMetricCounts
	Rates      CampaignMetricRates
	Variants   []CampaignVariantMetrics
}

type EmailCampaignABVariantResult struct {
	VariantID     int
	Delivered     int64
	UniqueOpened  int64
	UniqueClicked int64
}

type EmailCampaignABWinnerSelector func(
	dimension ABDimension,
	variants []EmailCampaignABVariantResult,
) (int, error)
