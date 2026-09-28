package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ConvertFiberLabelTranslationsToPb projects a fibre's care-label names onto the wire in the label's
// language order (entity.LabelLangs), skipping languages that have no name. Iterating the closed
// list, not the map, keeps the payload byte-stable across dictionary reloads.
func ConvertFiberLabelTranslationsToPb(names map[string]string) []*pb_common.FiberLabelTranslation {
	if len(names) == 0 {
		return nil
	}
	out := make([]*pb_common.FiberLabelTranslation, 0, len(names))
	for _, lang := range entity.LabelLangs {
		if name, ok := names[lang]; ok && name != "" {
			out = append(out, &pb_common.FiberLabelTranslation{LabelLang: lang, Name: name})
		}
	}
	return out
}

// ConvertPbFiberLabelTranslations copies the wire list as-is (order and duplicates kept) so
// entity.NormalizeFiberLabelTranslations can address a violation to translations[i].
func ConvertPbFiberLabelTranslations(in []*pb_common.FiberLabelTranslation) []entity.FiberLabelTranslation {
	out := make([]entity.FiberLabelTranslation, 0, len(in))
	for _, t := range in {
		out = append(out, entity.FiberLabelTranslation{LabelLang: t.GetLabelLang(), Name: t.GetName()})
	}
	return out
}
