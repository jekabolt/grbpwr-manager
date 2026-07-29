package campaignrender

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/canonical"
	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/slug"
	"github.com/shopspring/decimal"
)

type resolver struct {
	rep      dependency.Repository
	media    map[int]entity.MediaFull
	products map[int]entity.Colorway
}

func newResolver(rep dependency.Repository) *resolver {
	return &resolver{
		rep:      rep,
		media:    make(map[int]entity.MediaFull),
		products: make(map[int]entity.Colorway),
	}
}

func (r *resolver) prime(ctx context.Context, mediaIDs, productIDs []int) {
	if len(mediaIDs) > 0 {
		resolved, err := r.rep.Media().GetMediaByIds(ctx, mediaIDs)
		if err != nil {
			slog.ErrorContext(ctx, "failed to batch-load campaign media; falling back to per-id reads",
				slog.String("err", err.Error()))
		} else {
			for id, media := range resolved {
				r.media[id] = media
			}
		}
	}
	if len(productIDs) > 0 {
		resolved, err := r.rep.Products().GetProductsByIds(ctx, productIDs)
		if err != nil {
			slog.ErrorContext(ctx, "failed to batch-load campaign products; falling back to per-id reads",
				slog.String("err", err.Error()))
		} else {
			for _, product := range resolved {
				if product.IsPubliclyVisible() {
					r.products[product.Id] = product
				}
			}
		}
	}
}

func (r *resolver) getMedia(ctx context.Context, id int) (*entity.MediaFull, bool) {
	if id <= 0 {
		return nil, false
	}
	if media, ok := r.media[id]; ok {
		return &media, true
	}
	media, err := r.rep.Media().GetMediaById(ctx, id)
	if err != nil || media == nil {
		if err != nil {
			slog.WarnContext(ctx, "campaign media reference could not be resolved",
				slog.Int("media_id", id), slog.String("err", err.Error()))
		}
		return nil, false
	}
	r.media[id] = *media
	return media, true
}

func (r *resolver) getProduct(ctx context.Context, id int) (*entity.Colorway, bool) {
	if id <= 0 {
		return nil, false
	}
	if product, ok := r.products[id]; ok {
		return &product, true
	}
	products, err := r.rep.Products().GetProductsByIds(ctx, []int{id})
	if err != nil {
		slog.WarnContext(ctx, "campaign product reference could not be resolved",
			slog.Int("product_id", id), slog.String("err", err.Error()))
		return nil, false
	}
	for i := range products {
		if products[i].Id == id && products[i].IsPubliclyVisible() {
			r.products[id] = products[i]
			return &products[i], true
		}
	}
	return nil, false
}

func collectMediaIDs(blocks []entity.EmailBlock) []int {
	seen := make(map[int]struct{})
	var out []int
	var walk func([]entity.EmailBlock)
	add := func(id int) {
		if id <= 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	walk = func(items []entity.EmailBlock) {
		for i := range items {
			block := &items[i]
			if block.Header != nil {
				add(block.Header.LogoMediaID)
			}
			if block.ImageLink != nil {
				add(block.ImageLink.MediaID)
			}
			if block.VideoThumb != nil {
				add(block.VideoThumb.MediaID)
			}
			if block.TwoColumn != nil {
				walk(block.TwoColumn.Left)
				walk(block.TwoColumn.Right)
			}
		}
	}
	walk(blocks)
	return out
}

func collectProductIDs(blocks []entity.EmailBlock) []int {
	seen := make(map[int]struct{})
	var out []int
	var walk func([]entity.EmailBlock)
	add := func(id int) {
		if id <= 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	walk = func(items []entity.EmailBlock) {
		for i := range items {
			block := &items[i]
			if block.ProductCard != nil {
				add(block.ProductCard.ProductID)
			}
			if block.ProductGrid != nil {
				for _, id := range block.ProductGrid.ProductIDs {
					add(id)
				}
			}
			if block.TwoColumn != nil {
				walk(block.TwoColumn.Left)
				walk(block.TwoColumn.Right)
			}
		}
	}
	walk(blocks)
	return out
}

func mediaToView(media *entity.MediaFull, alt string) mediaView {
	if media == nil {
		return mediaView{}
	}
	url := media.CompressedMediaURL
	width := media.CompressedWidth
	height := media.CompressedHeight
	if url == "" {
		url = media.FullSizeMediaURL
		width = media.FullSizeWidth
		height = media.FullSizeHeight
	}
	return mediaView{URL: safeURL(url), Width: width, Height: height, Alt: alt}
}

func selectCampaignPrice(product *entity.Colorway) (entity.ColorwayPrice, bool) {
	if product == nil || len(product.Prices) == 0 {
		return entity.ColorwayPrice{}, false
	}
	for _, price := range product.Prices {
		if strings.EqualFold(price.Currency, "EUR") {
			return price, true
		}
	}
	prices := append([]entity.ColorwayPrice(nil), product.Prices...)
	sort.Slice(prices, func(i, j int) bool { return prices[i].Currency < prices[j].Currency })
	return prices[0], true
}

func formatCampaignMoney(amount decimal.Decimal, code string) string {
	return currency.Symbol(code) + amount.StringFixed(currency.DecimalPlaces(code))
}

func productToView(product *entity.Colorway, languageID int, langs []entity.Language) (productView, bool) {
	if product == nil || !product.IsPubliclyVisible() {
		return productView{}, false
	}
	// Localize the card name to the recipient's resolved language (falls back to the
	// canonical default-language name). The URL slug stays on the default name below so
	// public product links remain stable across locales.
	name, ok := canonical.ProductNameForLanguageID(product.ProductDisplay.ProductBody.Translations, languageID, langs)
	if !ok || strings.TrimSpace(name) == "" {
		return productView{}, false
	}
	price, ok := selectCampaignPrice(product)
	if !ok {
		return productView{}, false
	}
	original := formatCampaignMoney(price.Price, price.Currency)
	display := original
	onSale := false
	salePercentage := product.ProductDisplay.ProductBody.SalePercentageDecimal()
	if salePercentage.GreaterThan(decimal.Zero) {
		multiplier := decimal.NewFromInt(100).Sub(salePercentage).Div(decimal.NewFromInt(100))
		display = formatCampaignMoney(price.Price.Mul(multiplier), price.Currency)
		onSale = true
	}
	// The public product URL uses the canonical (default-language) name so the campaign link
	// matches the storefront's actual slug regardless of the recipient's locale.
	slugName := name
	if canonicalName, ok := canonical.ProductName(product.ProductDisplay.ProductBody.Translations, langs); ok && strings.TrimSpace(canonicalName) != "" {
		slugName = canonicalName
	}
	return productView{
		Brand:         product.ProductDisplay.ProductBody.ProductBodyInsert.Brand,
		Name:          name,
		Price:         display,
		OriginalPrice: original,
		OnSale:        onSale,
		Thumbnail:     mediaToView(&product.ProductDisplay.Thumbnail, name),
		URL:           safeURL("https://grbpwr.com" + slug.ProductPath(slugName, product.SKU)),
	}, true
}

func clamp(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func resolveBlocks(
	ctx context.Context,
	r *resolver,
	blocks []entity.EmailBlock,
	languageID int,
	langs []entity.Language,
	sectionBg string,
	depth int,
	rootIndex int,
) ([]blockView, []Warning) {
	out := make([]blockView, 0, len(blocks))
	var warnings []Warning
	for i := range blocks {
		index := rootIndex
		if depth == 0 {
			index = i
		}
		resolved, blockWarnings, ok := resolveBlock(ctx, r, &blocks[i], languageID, langs, sectionBg, depth, index)
		warnings = append(warnings, blockWarnings...)
		if ok {
			out = append(out, resolved)
		}
	}
	return out, warnings
}

func resolveBlock(
	ctx context.Context,
	r *resolver,
	block *entity.EmailBlock,
	languageID int,
	langs []entity.Language,
	sectionBg string,
	depth, blockIndex int,
) (blockView, []Warning, bool) {
	view := blockView{
		Type: block.Type,
		BlockIndex: blockIndex,
		// A block with no explicit background inherits the campaign (document) background instead
		// of forcing white — so setting the campaign background applies to every block and changing
		// it propagates automatically. Its palette (text color) is derived from this in
		// setBlockPalette, keeping text readable on any background.
		BackgroundColor: safeColor(block.BackgroundColor, sectionBg),
		Translations:    append([]entity.EmailBlockTranslation(nil), block.Translations...),
	}
	warn := func(reason string) (blockView, []Warning, bool) {
		return blockView{}, []Warning{{BlockIndex: blockIndex, Reason: reason}}, false
	}

	switch block.Type {
	case entity.EmailBlockTypeHeader:
		logo := mediaView{URL: defaultLogoURL, Width: 54, Height: 54, Alt: "GRBPWR"}
		if block.Header != nil && block.Header.LogoMediaID > 0 {
			media, ok := r.getMedia(ctx, block.Header.LogoMediaID)
			if !ok {
				return warn(fmt.Sprintf("header logo media %d is missing", block.Header.LogoMediaID))
			}
			logo = mediaToView(media, "GRBPWR")
		}
		view.Header = &headerView{Logo: logo}
	case entity.EmailBlockTypeImageLink:
		if block.ImageLink == nil {
			return warn("image_link payload is missing")
		}
		media, ok := r.getMedia(ctx, block.ImageLink.MediaID)
		if !ok {
			return warn(fmt.Sprintf("image_link media %d is missing", block.ImageLink.MediaID))
		}
		view.ImageLink = &imageLinkView{Media: mediaToView(media, ""), URL: safeURL(block.ImageLink.URL)}
	case entity.EmailBlockTypeRichText:
		view.RichHTML = ""
	case entity.EmailBlockTypeProductCard:
		if block.ProductCard == nil {
			return warn("product_card payload is missing")
		}
		product, ok := r.getProduct(ctx, block.ProductCard.ProductID)
		if !ok {
			return warn(fmt.Sprintf("product %d is missing or hidden", block.ProductCard.ProductID))
		}
		productSnapshot, ok := productToView(product, languageID, langs)
		if !ok {
			return warn(fmt.Sprintf("product %d cannot be rendered", block.ProductCard.ProductID))
		}
		view.ProductCard = &productCardView{Product: productSnapshot}
	case entity.EmailBlockTypeProductGrid:
		if block.ProductGrid == nil {
			return warn("product_grid payload is missing")
		}
		var products []productView
		var warnings []Warning
		for _, id := range block.ProductGrid.ProductIDs {
			product, ok := r.getProduct(ctx, id)
			if !ok {
				warnings = append(warnings, Warning{BlockIndex: blockIndex, Reason: fmt.Sprintf("product %d is missing or hidden", id)})
				continue
			}
			snapshot, ok := productToView(product, languageID, langs)
			if !ok {
				warnings = append(warnings, Warning{BlockIndex: blockIndex, Reason: fmt.Sprintf("product %d cannot be rendered", id)})
				continue
			}
			products = append(products, snapshot)
		}
		if len(products) == 0 {
			if len(warnings) == 0 {
				warnings = append(warnings, Warning{BlockIndex: blockIndex, Reason: "product_grid contains no visible products"})
			}
			return blockView{}, warnings, false
		}
		// An unset columns count (0) must not collapse the grid into a single-column list — default
		// to 2 so a product grid actually reads as a grid.
		gridCols := block.ProductGrid.Columns
		if gridCols <= 0 {
			gridCols = 2
		}
		columns := clamp(gridCols, 1, 3)
		view.ProductGrid = &productGridView{
			Products:  products,
			Columns:   columns,
			CellWidth: fmt.Sprintf("%d%%", 100/columns),
		}
		return view, warnings, true
	case entity.EmailBlockTypeCTAButton:
		style := "solid"
		alignment := "center"
		if block.CTAButton != nil {
			if block.CTAButton.Style == "outline" {
				style = "outline"
			}
			switch block.CTAButton.Alignment {
			case "left", "center", "right":
				alignment = block.CTAButton.Alignment
			}
		}
		view.CTAButton = &ctaButtonView{Style: style, Alignment: alignment, Color: defaultTextColor}
	case entity.EmailBlockTypeDivider:
		color := defaultTextColor
		height := 1
		if block.Divider != nil {
			color = safeColor(block.Divider.Color, defaultTextColor)
			height = clamp(block.Divider.Height, 1, 8)
		}
		view.Divider = &dividerView{Color: color, Height: height}
	case entity.EmailBlockTypeSpacer:
		height := 24
		if block.Spacer != nil {
			height = clamp(block.Spacer.Height, 1, 120)
		}
		view.Spacer = &spacerView{Height: height}
	case entity.EmailBlockTypeTwoColumn:
		if depth >= 1 {
			return warn("nested two_column blocks are not supported")
		}
		if block.TwoColumn == nil {
			return warn("two_column payload is missing")
		}
		left, leftWarnings := resolveBlocks(ctx, r, block.TwoColumn.Left, languageID, langs, view.BackgroundColor, depth+1, blockIndex)
		right, rightWarnings := resolveBlocks(ctx, r, block.TwoColumn.Right, languageID, langs, view.BackgroundColor, depth+1, blockIndex)
		view.TwoColumn = &twoColumnView{Left: left, Right: right}
		return view, append(leftWarnings, rightWarnings...), true
	case entity.EmailBlockTypeSocialLinks:
		if block.SocialLinks == nil {
			return warn("social_links payload is missing")
		}
		links := make([]socialLinkView, 0, len(block.SocialLinks.Links))
		for _, link := range block.SocialLinks.Links {
			url := safeURL(link.URL)
			if url == "" {
				continue
			}
			label := strings.ToUpper(strings.TrimSpace(link.Network))
			if len(label) > 2 {
				label = label[:2]
			}
			links = append(links, socialLinkView{Label: label, URL: url})
		}
		view.SocialLinks = &socialLinksView{Links: links}
	case entity.EmailBlockTypeCountdown:
		if block.Countdown == nil || block.Countdown.EndsAt <= 0 {
			return warn("countdown end time is missing")
		}
		absolute := time.Unix(block.Countdown.EndsAt, 0).UTC()
		view.Countdown = &countdownView{EndsAt: "ENDS " + strings.ToUpper(absolute.Format("02 Jan 2006 · 15:04 UTC"))}
	case entity.EmailBlockTypeVideoThumb:
		if block.VideoThumb == nil {
			return warn("video_thumb payload is missing")
		}
		media, ok := r.getMedia(ctx, block.VideoThumb.MediaID)
		if !ok {
			return warn(fmt.Sprintf("video_thumb media %d is missing", block.VideoThumb.MediaID))
		}
		view.VideoThumb = &videoThumbView{
			Poster: mediaToView(media, ""),
			URL:    safeURL(block.VideoThumb.VideoURL),
		}
	default:
		return warn(fmt.Sprintf("unsupported email block type %d", block.Type))
	}
	return view, nil, true
}
