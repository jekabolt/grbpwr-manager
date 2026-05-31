package dto

// Email data structs for loyalty tier / membership notifications.
// Transactional emails set EmailB64 to " " so the footer hides the unsubscribe
// link; marketing emails set it to the base64 email so unsubscribe is shown.

// TierChangeEmail is shared by upgrade / backfill / downgrade / rollback emails.
type TierChangeEmail struct {
	Preheader       string
	EmailB64        string
	Name            string
	TierDisplay     string // target tier display name, e.g. "grbpwr++"
	PrevTierDisplay string // previous tier display name
	SpendEUR        string // formatted, e.g. "1,240"
	ThresholdEUR    string // threshold to keep/reach a tier, formatted
	NextReview      string // formatted date, may be empty
	IsBackfill      bool
}

// HackerInviteEmail carries the one-time invite link.
type HackerInviteEmail struct {
	Preheader string
	EmailB64  string
	InviteURL string
	ExpiresAt string
}

// MemberCustomEmail is used for admin-sent custom / event-invite emails.
type MemberCustomEmail struct {
	Preheader string
	EmailB64  string
	Name      string
	Heading   string
	Body      string // plain text; rendered as-is (no HTML injection)
	CTALabel  string
	CTAURL    string
}

// BirthdayEmail is the yearly birthday gift email (marketing).
type BirthdayEmail struct {
	Preheader string
	EmailB64  string
	Name      string
	PromoCode string
}

// UnsubscribeConfirmationEmail confirms a newsletter opt-out (transactional).
type UnsubscribeConfirmationEmail struct {
	Preheader string
	EmailB64  string
	Name      string
}
