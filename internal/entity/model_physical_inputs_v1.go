package entity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const modelPhysicalInputsDomainV1 = "tech-card-physical-model-inputs/1"

// PersistedModelPhysicalInputsSHA256V1 seals the body inputs exposed by GetModel.
// The model timestamp is bound separately. Measurement or provenance edits can
// change the body input without changing the model header timestamp.
func PersistedModelPhysicalInputsSHA256V1(modelID int, gender GenderEnum, measurements []ModelMeasurement) (string, error) {
	if modelID <= 0 || modelID > math.MaxInt32 {
		return "", fmt.Errorf("physical model id must be a positive protobuf int32")
	}
	switch gender {
	case Male, Female, Unisex:
	default:
		return "", fmt.Errorf("physical model gender must be a known persisted body basis")
	}

	ordered := slices.Clone(measurements)
	slices.SortFunc(ordered, func(a, b ModelMeasurement) int {
		return strings.Compare(strings.ToUpper(string(a.Name)), strings.ToUpper(string(b.Name)))
	})
	wire := make([]any, 0, len(ordered))
	for index, measurement := range ordered {
		if !ValidBodyMeasurementNames[measurement.Name] || measurement.ValueMM < 1 || measurement.ValueMM > 5000 {
			return "", fmt.Errorf("unknown physical model measurement or invalid millimetres")
		}
		if index > 0 && ordered[index-1].Name == measurement.Name {
			return "", fmt.Errorf("duplicate physical model measurement")
		}
		var metadata any
		if stored := measurement.Metadata; stored != nil {
			protocol := stored.ProtocolID
			if stored.SchemaVersion != BodyMeasurementMetadataSchemaVersionV1 ||
				!ValidBodyMeasurementSources[stored.Source] || !utf8.ValidString(protocol) ||
				protocol == "" || strings.TrimSpace(protocol) != protocol ||
				utf8.RuneCountInString(protocol) > 128 || strings.IndexFunc(protocol, unicode.IsControl) >= 0 {
				return "", fmt.Errorf("invalid physical model measurement provenance")
			}
			metadata = map[string]any{
				"protocolId":    protocol,
				"schemaVersion": int(stored.SchemaVersion),
				"source":        "BODY_MEASUREMENT_SOURCE_" + strings.ToUpper(string(stored.Source)),
			}
		}
		wire = append(wire, map[string]any{
			"metadata": metadata,
			"name":     "BODY_MEASUREMENT_NAME_" + strings.ToUpper(string(measurement.Name)),
			"valueMm":  measurement.ValueMM,
		})
	}

	canonical, err := canonicalCompositeValue(map[string]any{
		"gender":       "GENDER_ENUM_" + strings.ToUpper(string(gender)),
		"measurements": wire,
		"modelId":      modelID,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte(modelPhysicalInputsDomainV1+"\x00"), canonical...))
	return hex.EncodeToString(digest[:]), nil
}
