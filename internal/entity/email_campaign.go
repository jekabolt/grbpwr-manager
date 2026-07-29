package entity

import "time"

type EmailBlockType int32

const (
	EmailBlockTypeUnknown     EmailBlockType = 0
	EmailBlockTypeHeader      EmailBlockType = 1
	EmailBlockTypeImageLink   EmailBlockType = 2
	EmailBlockTypeRichText    EmailBlockType = 3
	EmailBlockTypeProductCard EmailBlockType = 4
	EmailBlockTypeProductGrid EmailBlockType = 5
	EmailBlockTypeCTAButton   EmailBlockType = 6
	EmailBlockTypeDivider     EmailBlockType = 7
	EmailBlockTypeSpacer      EmailBlockType = 8
	EmailBlockTypeTwoColumn   EmailBlockType = 9
	EmailBlockTypeSocialLinks EmailBlockType = 10
	EmailBlockTypeCountdown   EmailBlockType = 11
	EmailBlockTypeVideoThumb  EmailBlockType = 12
)

type EmailLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// EmailFooterStrings are the localized campaign-footer labels, resolved per recipient locale
// from the shared transactional i18n catalog (common.footer.* keys) and passed into the campaign
// renderer. A neutral type (not campaignrender's) so the mailer can return it without importing
// the renderer, which itself imports dependency. Empty fields fall back to the template's English.
type EmailFooterStrings struct {
	Help      string // "NEED HELP?"
	Faq       string // "FAQ"
	Aftersale string // "AFTER-SALE SERVICES"
	UnsubPre  string // "if you no longer wish to receive email updates,"
	UnsubWord string // "unsubscribe"
}

// EmailBlockTranslation is the shared translation superset for all block types.
type EmailBlockTranslation struct {
	LanguageID int         `json:"language_id"`
	Heading    string      `json:"heading"`
	Subheading string      `json:"subheading"`
	Body       string      `json:"body"`
	Caption    string      `json:"caption"`
	CTALabel   string      `json:"cta_label"`
	CTAURL     string      `json:"cta_url"`
	AltText    string      `json:"alt_text"`
	Preheader  string      `json:"preheader"`
	Links      []EmailLink `json:"links"`
}

type EmailHeaderBlock struct {
	LogoMediaID int `json:"logo_media_id"`
}

type EmailImageLinkBlock struct {
	MediaID int    `json:"media_id"`
	URL     string `json:"url"`
}

type EmailRichTextBlock struct{}

type EmailProductCardBlock struct {
	ProductID int `json:"product_id"`
}

type EmailProductGridBlock struct {
	ProductIDs []int `json:"product_ids"`
	Columns    int   `json:"columns"`
}

type EmailCTAButtonBlock struct {
	Style     string `json:"style"`
	Alignment string `json:"alignment"`
}

type EmailDividerBlock struct {
	Color  string `json:"color"`
	Height int    `json:"height"`
}

type EmailSpacerBlock struct {
	Height int `json:"height"`
}

type EmailTwoColumnBlock struct {
	Left  []EmailBlock `json:"left"`
	Right []EmailBlock `json:"right"`
}

type EmailSocialLink struct {
	Network string `json:"network"`
	URL     string `json:"url"`
}

type EmailSocialLinksBlock struct {
	Links []EmailSocialLink `json:"links"`
}

type EmailCountdownBlock struct {
	EndsAt int64 `json:"ends_at"`
}

type EmailVideoThumbBlock struct {
	MediaID  int    `json:"media_id"`
	VideoURL string `json:"video_url"`
}

// EmailBlock is the persisted discriminated union. Only the payload selected by
// Type is expected to be non-nil.
type EmailBlock struct {
	Type            EmailBlockType          `json:"type"`
	Header          *EmailHeaderBlock       `json:"header,omitempty"`
	ImageLink       *EmailImageLinkBlock    `json:"image_link,omitempty"`
	RichText        *EmailRichTextBlock     `json:"rich_text,omitempty"`
	ProductCard     *EmailProductCardBlock  `json:"product_card,omitempty"`
	ProductGrid     *EmailProductGridBlock  `json:"product_grid,omitempty"`
	CTAButton       *EmailCTAButtonBlock    `json:"cta_button,omitempty"`
	Divider         *EmailDividerBlock      `json:"divider,omitempty"`
	Spacer          *EmailSpacerBlock       `json:"spacer,omitempty"`
	TwoColumn       *EmailTwoColumnBlock    `json:"two_column,omitempty"`
	SocialLinks     *EmailSocialLinksBlock  `json:"social_links,omitempty"`
	Countdown       *EmailCountdownBlock    `json:"countdown,omitempty"`
	VideoThumb      *EmailVideoThumbBlock   `json:"video_thumb,omitempty"`
	BackgroundColor string                  `json:"background_color"`
	Translations    []EmailBlockTranslation `json:"translations"`
}

type EmailCampaignTopic string

const (
	EmailCampaignTopicUnknown     EmailCampaignTopic = ""
	EmailCampaignTopicNewsletter  EmailCampaignTopic = "newsletter"
	EmailCampaignTopicNewArrivals EmailCampaignTopic = "new_arrivals"
	EmailCampaignTopicEvents      EmailCampaignTopic = "events"
)

type EmailCampaignStatus string

const (
	EmailCampaignStatusUnknown   EmailCampaignStatus = ""
	EmailCampaignStatusDraft     EmailCampaignStatus = "draft"
	EmailCampaignStatusScheduled EmailCampaignStatus = "scheduled"
	EmailCampaignStatusSending   EmailCampaignStatus = "sending"
	EmailCampaignStatusPaused    EmailCampaignStatus = "paused"
	EmailCampaignStatusSent      EmailCampaignStatus = "sent"
	EmailCampaignStatusCancelled EmailCampaignStatus = "cancelled"
)

type ABDimension string

const (
	ABDimensionUnknown ABDimension = ""
	ABDimensionSubject ABDimension = "subject"
	ABDimensionContent ABDimension = "content"
)

type ABConfig struct {
	Enabled              bool
	Dimension            ABDimension
	TestPct              int
	DecisionAfterMinutes int
	WinnerVariantID      *int
}

type SubjectTranslation struct {
	LanguageID int    `json:"language_id"`
	Subject    string `json:"subject"`
}

type EmailCampaignVariant struct {
	ID          int
	Label       string
	SubjectI18n []SubjectTranslation
	Body        []EmailBlock
	IsWinner    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// EmailCampaignInsert is the write-side campaign aggregate.
type EmailCampaignInsert struct {
	Name            string
	Topic           EmailCampaignTopic
	Body            []EmailBlock
	BackgroundColor string
	FromName        *string
	FromEmail       *string
	ReplyTo         *string
	ScheduleAt      *time.Time
	ABConfig        ABConfig
	Variants        []EmailCampaignVariant
	Status          EmailCampaignStatus
	SegmentID       *int
	CreatedBy       string
}

type EmailCampaignFull struct {
	ID int
	EmailCampaignInsert
	CreatedAt              time.Time
	UpdatedAt              time.Time
	SendingStartedAt       *time.Time
	SentAt                 *time.Time
	AudiencePredicate      *SegmentPredicate
	AudienceSnapshotAt     *time.Time
	FanoutMaxAccountID     int
	FanoutCursorAccountID  int
	AudienceMaterializedAt *time.Time
	RecipientCount         *int
	DispatchError          *string
}

type SegmentOp int32

const (
	SegmentOpUnknown SegmentOp = 0
	SegmentOpAnd     SegmentOp = 1
	SegmentOpOr      SegmentOp = 2
)

type SegmentNode struct {
	Op       SegmentOp     `json:"op"`
	Children []SegmentNode `json:"children"`
	Field    string        `json:"field"`
	Operator string        `json:"operator"`
	Values   []string      `json:"values"`
}

type SegmentPredicate struct {
	Root *SegmentNode `json:"root,omitempty"`
}

type EmailSegment struct {
	ID          int
	Name        string
	Description string
	Predicate   SegmentPredicate
	LastCount   *int
	LastCountAt *time.Time
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
