package campaignrender

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html"
	"html/template"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/microcosm-cc/bluemonday"
)

//go:embed templates/*.gohtml templates/partials/*.gohtml templates/blocks/*.gohtml
var campaignTemplates embed.FS

type Renderer struct {
	templates *template.Template
	policy    *bluemonday.Policy
}

type sectionView struct {
	BackgroundColor string
	Body            template.HTML
}

func dict(values ...any) (map[string]any, error) {
	if len(values)%2 != 0 {
		return nil, fmt.Errorf("dict requires an even number of arguments")
	}
	out := make(map[string]any, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict keys must be strings")
		}
		out[key] = values[i+1]
	}
	return out, nil
}

func productRows(products []productView, columns int) [][]productView {
	columns = clamp(columns, 1, 3)
	rows := make([][]productView, 0, (len(products)+columns-1)/columns)
	for len(products) > 0 {
		count := columns
		if len(products) < count {
			count = len(products)
		}
		rows = append(rows, products[:count])
		products = products[count:]
	}
	return rows
}

func vmlButton(block blockView) template.HTML {
	if block.CTAButton == nil || block.CTAButton.URL == "" || block.Copy.CTALabel == "" {
		return ""
	}
	// The CTA "ink" is the palette Rule; a solid button fills with it and prints
	// the OnRule label, an outline button fills with the background and prints Rule.
	stroke := block.Palette.Rule
	fill := block.Palette.Rule
	textColor := block.Palette.OnRule
	if block.CTAButton.Style == "outline" {
		fill = safeColor(block.BackgroundColor, defaultSectionBackground)
		textColor = block.Palette.Rule
	}
	url := html.EscapeString(safeURL(block.CTAButton.URL))
	label := html.EscapeString(block.Copy.CTALabel)
	return template.HTML(fmt.Sprintf(
		`<!--[if mso]><v:roundrect xmlns:v="urn:schemas-microsoft-com:vml" href="%s" style="height:42px;v-text-anchor:middle;width:220px;" arcsize="0%%" strokecolor="%s" fillcolor="%s"><w:anchorlock xmlns:w="urn:schemas-microsoft-com:office:word"/><center style="color:%s;font-family:Consolas,'Courier New',monospace;font-size:13px;letter-spacing:1px;">%s</center></v:roundrect><![endif]-->`,
		url, stroke, fill, textColor, label,
	))
}

// setBlockPalette derives each block's foreground palette from ITS OWN background (already resolved
// to inherit the document background when unset), so text stays readable on any block — light text
// on a dark block, dark text on a light one — even when a block overrides the campaign background.
// The doc-level pal is retained for the document chrome (footer etc.).
func setBlockPalette(blocks []blockView, docPal palette) {
	for i := range blocks {
		blocks[i].Palette = newPalette(blocks[i].BackgroundColor)
		if blocks[i].TwoColumn != nil {
			setBlockPalette(blocks[i].TwoColumn.Left, docPal)
			setBlockPalette(blocks[i].TwoColumn.Right, docPal)
		}
	}
}

func campaignTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"dict":    dict,
		"safeURL": safeURL,
		"cssColor": func(value, fallback string) template.CSS {
			return template.CSS(safeColor(value, fallback))
		},
		"rows":      productRows,
		"gridWidth": func(columns int) string { return fmt.Sprintf("%d%%", 100/clamp(columns, 1, 3)) },
		"vmlButton": vmlButton,
	}
}

func New() (*Renderer, error) {
	templates, err := template.New("campaign").Funcs(campaignTemplateFuncs()).ParseFS(
		campaignTemplates,
		"templates/partials/*.gohtml",
		"templates/blocks/*.gohtml",
		"templates/*.gohtml",
	)
	if err != nil {
		return nil, fmt.Errorf("parse campaign email templates: %w", err)
	}
	return &Renderer{templates: templates, policy: richTextPolicy}, nil
}

func applyTranslation(block *blockView, languageID int, langs []entity.Language) {
	block.Copy = pickTranslation(block.Translations, languageID, langs)
	if block.Type == entity.EmailBlockTypeRichText {
		block.RichHTML = template.HTML(SanitizeRichText(block.Copy.Body))
	}
	if block.ImageLink != nil {
		block.ImageLink.Media.Alt = block.Copy.AltText
		block.ImageLink.URL = safeURL(block.ImageLink.URL)
	}
	if block.VideoThumb != nil {
		block.VideoThumb.Poster.Alt = block.Copy.AltText
		block.VideoThumb.URL = safeURL(block.VideoThumb.URL)
	}
	if block.CTAButton != nil {
		block.CTAButton.URL = safeURL(block.Copy.CTAURL)
	}
	links := make([]entity.EmailLink, 0, len(block.Copy.Links))
	for _, link := range block.Copy.Links {
		if url := safeURL(link.URL); url != "" {
			link.URL = url
			links = append(links, link)
		}
	}
	block.Copy.Links = links
	if block.TwoColumn != nil {
		for i := range block.TwoColumn.Left {
			applyTranslation(&block.TwoColumn.Left[i], languageID, langs)
		}
		for i := range block.TwoColumn.Right {
			applyTranslation(&block.TwoColumn.Right[i], languageID, langs)
		}
	}
}

func blockTemplateName(blockType entity.EmailBlockType) (string, error) {
	switch blockType {
	case entity.EmailBlockTypeHeader:
		return "block_header", nil
	case entity.EmailBlockTypeImageLink:
		return "block_image_link", nil
	case entity.EmailBlockTypeRichText:
		return "block_rich_text", nil
	case entity.EmailBlockTypeProductCard:
		return "block_product_card", nil
	case entity.EmailBlockTypeProductGrid:
		return "block_product_grid", nil
	case entity.EmailBlockTypeCTAButton:
		return "block_cta_button", nil
	case entity.EmailBlockTypeDivider:
		return "block_divider", nil
	case entity.EmailBlockTypeSpacer:
		return "block_spacer", nil
	case entity.EmailBlockTypeTwoColumn:
		return "block_two_column", nil
	case entity.EmailBlockTypeSocialLinks:
		return "block_social_links", nil
	case entity.EmailBlockTypeCountdown:
		return "block_countdown", nil
	case entity.EmailBlockTypeVideoThumb:
		return "block_video_thumb", nil
	default:
		return "", fmt.Errorf("unsupported campaign block type %d", blockType)
	}
}

func (r *Renderer) renderRows(blocks []blockView) (string, error) {
	var rows bytes.Buffer
	for i := range blocks {
		block := &blocks[i]
		if block.TwoColumn != nil {
			leftRows, err := r.renderRows(block.TwoColumn.Left)
			if err != nil {
				return "", err
			}
			rightRows, err := r.renderRows(block.TwoColumn.Right)
			if err != nil {
				return "", err
			}
			block.TwoColumn.LeftHTML = template.HTML(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">` + leftRows + `</table>`)
			block.TwoColumn.RightHTML = template.HTML(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">` + rightRows + `</table>`)
		}
		name, err := blockTemplateName(block.Type)
		if err != nil {
			return "", err
		}
		var body bytes.Buffer
		if err := r.templates.ExecuteTemplate(&body, name, *block); err != nil {
			return "", fmt.Errorf("execute %s for block %d: %w", name, block.BlockIndex, err)
		}
		if err := r.templates.ExecuteTemplate(&rows, "section", sectionView{
			BackgroundColor: block.BackgroundColor,
			Body:            template.HTML(body.String()),
		}); err != nil {
			return "", fmt.Errorf("wrap block %d section: %w", block.BlockIndex, err)
		}
	}
	return rows.String(), nil
}

func firstHeaderPreheader(blocks []blockView) (string, bool) {
	for i := range blocks {
		if blocks[i].Type == entity.EmailBlockTypeHeader {
			return blocks[i].Copy.Preheader, true
		}
		if blocks[i].TwoColumn != nil {
			if value, ok := firstHeaderPreheader(blocks[i].TwoColumn.Left); ok {
				return value, true
			}
			if value, ok := firstHeaderPreheader(blocks[i].TwoColumn.Right); ok {
				return value, true
			}
		}
	}
	return "", false
}

// Render resolves references into immutable display literals, selects copy, and
// executes the standalone campaign templates. It intentionally has no clock.
func (r *Renderer) Render(
	ctx context.Context,
	rep dependency.Repository,
	in Input,
) (Rendered, []Warning, error) {
	if r == nil || r.templates == nil || r.policy == nil {
		return Rendered{}, nil, fmt.Errorf("campaign renderer is not initialized")
	}
	if rep == nil {
		return Rendered{}, nil, fmt.Errorf("repository is required")
	}
	background := safeColor(in.BackgroundColor, defaultEmailBackground)
	pal := newPalette(background)

	resolver := newResolver(rep)
	resolver.prime(ctx, collectMediaIDs(in.Blocks), collectProductIDs(in.Blocks))
	blocks, warnings := resolveBlocks(ctx, resolver, in.Blocks, in.LanguageID, in.Langs, background, 0, 0)
	for i := range blocks {
		applyTranslation(&blocks[i], in.LanguageID, in.Langs)
	}
	setBlockPalette(blocks, pal)

	preheader, _ := firstHeaderPreheader(blocks)
	body, err := r.renderRows(blocks)
	if err != nil {
		return Rendered{}, warnings, err
	}

	unsubscribeURL := safeURL(in.UnsubscribeURL)
	var document bytes.Buffer
	if err := r.templates.ExecuteTemplate(&document, "document", documentView{
		BackgroundColor: background,
		Palette:         pal,
		Preheader:       preheader,
		Body:            template.HTML(body),
		UnsubscribeURL:  unsubscribeURL,
		Footer:          in.Footer,
	}); err != nil {
		return Rendered{}, warnings, fmt.Errorf("execute campaign document: %w", err)
	}
	return Rendered{
		HTML: document.String(),
		Text: renderPlaintext(blocks, unsubscribeURL, in.Footer),
	}, warnings, nil
}
