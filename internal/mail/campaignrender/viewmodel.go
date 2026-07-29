package campaignrender

import (
	"html/template"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

const (
	defaultEmailBackground   = "#ffffff"
	defaultSectionBackground = "#ffffff"
	defaultTextColor         = "#0e0e0c"
	defaultLogoURL           = "https://files.grbpwr.com/grbpwr-com/mail/mail-logo.png"
)

type Input struct {
	Blocks          []entity.EmailBlock
	BackgroundColor string
	LanguageID      int
	Langs           []entity.Language
	UnsubscribeURL  string
}

type Rendered struct {
	HTML string
	Text string
}

type Warning struct {
	BlockIndex int
	Reason     string
}

type mediaView struct {
	URL    string
	Width  int
	Height int
	Alt    string
}

type productView struct {
	Brand         string
	Name          string
	Price         string
	OriginalPrice string
	OnSale        bool
	Thumbnail     mediaView
	URL           string
}

type headerView struct {
	Logo mediaView
}

type imageLinkView struct {
	Media mediaView
	URL   string
}

type productCardView struct {
	Product productView
}

type productGridView struct {
	Products  []productView
	Columns   int
	CellWidth string
}

type ctaButtonView struct {
	Style     string
	Alignment string
	URL       string
	Color     string
}

type dividerView struct {
	Color  string
	Height int
}

type spacerView struct {
	Height int
}

type twoColumnView struct {
	Left      []blockView
	Right     []blockView
	LeftHTML  template.HTML
	RightHTML template.HTML
}

type socialLinkView struct {
	Label string
	URL   string
}

type socialLinksView struct {
	Links []socialLinkView
}

type countdownView struct {
	EndsAt string
}

type videoThumbView struct {
	Poster mediaView
	URL    string
}

type blockView struct {
	Type            entity.EmailBlockType
	BlockIndex      int
	BackgroundColor string
	Palette         palette
	Translations    []entity.EmailBlockTranslation
	Copy            entity.EmailBlockTranslation
	RichHTML        template.HTML

	Header      *headerView
	ImageLink   *imageLinkView
	ProductCard *productCardView
	ProductGrid *productGridView
	CTAButton   *ctaButtonView
	Divider     *dividerView
	Spacer      *spacerView
	TwoColumn   *twoColumnView
	SocialLinks *socialLinksView
	Countdown   *countdownView
	VideoThumb  *videoThumbView
}

type documentView struct {
	BackgroundColor string
	Palette         palette
	Preheader       string
	Body            template.HTML
	UnsubscribeURL  string
}
