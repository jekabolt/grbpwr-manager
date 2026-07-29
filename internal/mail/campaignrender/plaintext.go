package campaignrender

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
)

// plaintextFooter returns the localized footer label, falling back to the English literal when
// the localized value is empty.
func plaintextFooter(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func appendTextLine(lines *[]string, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		*lines = append(*lines, value)
	}
}

func appendProductText(lines *[]string, product productView) {
	label := strings.TrimSpace(strings.Join([]string{product.Brand, product.Name}, " "))
	price := product.Price
	if product.OnSale {
		price = product.OriginalPrice + " → " + product.Price
	}
	appendTextLine(lines, fmt.Sprintf("%s — %s (%s)", label, price, product.URL))
}

func walkPlaintext(blocks []blockView, lines *[]string) {
	for i := range blocks {
		block := &blocks[i]
		switch block.Type {
		case entity.EmailBlockTypeHeader:
			appendTextLine(lines, block.Copy.Heading)
			appendTextLine(lines, block.Copy.Subheading)
			for _, link := range block.Copy.Links {
				appendTextLine(lines, fmt.Sprintf("%s (%s)", link.Label, link.URL))
			}
		case entity.EmailBlockTypeImageLink:
			appendTextLine(lines, block.Copy.Caption)
			if block.ImageLink != nil && block.ImageLink.URL != "" {
				appendTextLine(lines, block.ImageLink.URL)
			}
		case entity.EmailBlockTypeRichText:
			appendTextLine(lines, mail.HTMLToText(string(block.RichHTML)))
		case entity.EmailBlockTypeProductCard:
			if block.ProductCard != nil {
				appendProductText(lines, block.ProductCard.Product)
			}
		case entity.EmailBlockTypeProductGrid:
			if block.ProductGrid != nil {
				for _, product := range block.ProductGrid.Products {
					appendProductText(lines, product)
				}
			}
		case entity.EmailBlockTypeCTAButton:
			if block.CTAButton != nil && block.CTAButton.URL != "" {
				appendTextLine(lines, fmt.Sprintf("%s (%s)", block.Copy.CTALabel, block.CTAButton.URL))
			}
		case entity.EmailBlockTypeTwoColumn:
			if block.TwoColumn != nil {
				walkPlaintext(block.TwoColumn.Left, lines)
				walkPlaintext(block.TwoColumn.Right, lines)
			}
		case entity.EmailBlockTypeSocialLinks:
			if block.SocialLinks != nil {
				for _, link := range block.SocialLinks.Links {
					appendTextLine(lines, fmt.Sprintf("%s (%s)", link.Label, link.URL))
				}
			}
		case entity.EmailBlockTypeCountdown:
			if block.Countdown != nil {
				appendTextLine(lines, block.Copy.Heading)
				appendTextLine(lines, block.Countdown.EndsAt)
				appendTextLine(lines, block.Copy.Caption)
			}
		case entity.EmailBlockTypeVideoThumb:
			appendTextLine(lines, block.Copy.Caption)
			if block.VideoThumb != nil && block.VideoThumb.URL != "" {
				appendTextLine(lines, block.VideoThumb.URL)
			}
		}
	}
}

func collapsePlaintext(lines []string) string {
	out := make([]string, 0, len(lines))
	blank := false
	for _, value := range lines {
		for _, line := range strings.Split(value, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				if len(out) > 0 && !blank {
					out = append(out, "")
					blank = true
				}
				continue
			}
			out = append(out, line)
			blank = false
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func renderPlaintext(blocks []blockView, unsubscribeURL string, footer entity.EmailFooterStrings) string {
	var lines []string
	walkPlaintext(blocks, &lines)
	// Footer — mirrors the transactional email footer (partials/email_footer.gohtml), localized
	// per recipient (empty fields fall back to the English literal).
	help := plaintextFooter(footer.Help, "NEED HELP?")
	faq := plaintextFooter(footer.Faq, "FAQ")
	aftersale := plaintextFooter(footer.Aftersale, "AFTER-SALE SERVICES")
	appendTextLine(&lines, "—")
	appendTextLine(&lines, help+" customer@grbpwr.com")
	appendTextLine(&lines, faq+": https://grbpwr.com/faq · "+aftersale+": https://grbpwr.com/aftersale-services")
	if unsubscribeURL != "" {
		unsubPre := plaintextFooter(footer.UnsubPre, "If you no longer wish to receive email updates,")
		unsubWord := plaintextFooter(footer.UnsubWord, "unsubscribe")
		appendTextLine(&lines, unsubPre+" "+unsubWord+": "+unsubscribeURL)
	}
	appendTextLine(&lines, "GRBPWR LIMITED · 167–169 GREAT PORTLAND STREET, LONDON W1W 5PF · NO.17015705")
	return collapsePlaintext(lines)
}
