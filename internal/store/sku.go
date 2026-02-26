package store

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// GenerateSKU builds an immutable SKU.
// Format: CAT-COL-GID  e.g. TSH-BLK-M34
func GenerateSKU(p *entity.ProductInsert, id int) string {
	cat := categoryCode(p.ProductBodyInsert.SubCategoryId, p.ProductBodyInsert.TopCategoryId)
	col := alphaPrefix(p.ProductBodyInsert.Color, 3, "UNK")
	g := genderLetter(p.ProductBodyInsert.TargetGender)
	return fmt.Sprintf("%s-%s-%s%d", cat, col, g, id)
}

func alphaPrefix(s string, n int, fallback string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) {
			b.WriteRune(unicode.ToUpper(r))
			if b.Len() >= n {
				break
			}
		}
	}
	if b.Len() == 0 {
		return fallback
	}
	return b.String()
}

func categoryCode(subID sql.NullInt32, topID int) string {
	categories := cache.GetCategories()

	if subID.Valid && subID.Int32 > 0 {
		for _, c := range categories {
			if c.ID == int(subID.Int32) {
				return alphaPrefix(c.Name, 3, "UNK")
			}
		}
	}
	for _, c := range categories {
		if c.ID == topID {
			return alphaPrefix(c.Name, 3, "UNK")
		}
	}
	return "UNK"
}

func genderLetter(g entity.GenderEnum) string {
	switch g {
	case entity.Male:
		return "M"
	case entity.Female:
		return "F"
	default:
		return "U"
	}
}
