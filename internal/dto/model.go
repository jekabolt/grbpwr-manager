package dto

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxBodyMeasurementMM bounds an accepted body measurement (5 metres in mm).
const maxBodyMeasurementMM = 5000

const maxBodyMeasurementProtocolIDChars = 128

// maxVarchar255 bounds inputs stored in VARCHAR(255) columns (model name,
// fitting recorded_by) so over-length input fails as InvalidArgument rather
// than a MySQL 1406 (data too long) Internal error.
const maxVarchar255 = 255

var bodyMeasurementPbToEntity = map[pb_common.BodyMeasurementName]entity.BodyMeasurementName{
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CHEST:                 entity.BodyChest,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_UNDER_BUST:            entity.BodyUnderBust,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_WAIST:                 entity.BodyWaist,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HIGH_HIP:              entity.BodyHighHip,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HIP:                   entity.BodyHip,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_NECK_BASE:             entity.BodyNeckBase,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_ACROSS_SHOULDER:       entity.BodyAcrossShoulder,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_SLEEVE_LENGTH:         entity.BodySleeveLength,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_BICEP:                 entity.BodyBicep,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_WRIST:                 entity.BodyWrist,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_INSEAM:                entity.BodyInseam,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_THIGH:                 entity.BodyThigh,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_KNEE:                  entity.BodyKnee,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CALF:                  entity.BodyCalf,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_ANKLE:                 entity.BodyAnkle,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HEIGHT:                entity.BodyHeight,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HPS_TO_WAIST_FRONT:    entity.BodyHPSToWaistFront,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CB_NECK_TO_WAIST:      entity.BodyCBNeckToWaist,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_ACROSS_FRONT:          entity.BodyAcrossFront,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_ACROSS_BACK:           entity.BodyAcrossBack,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HPS_HEIGHT:            entity.BodyHPSHeight,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CF_NECK_TO_WAIST:      entity.BodyCFNeckToWaist,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_WAIST_HEIGHT:          entity.BodyWaistHeight,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_WAIST_TO_HIGH_HIP:     entity.BodyWaistToHighHip,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_WAIST_TO_HIP:          entity.BodyWaistToHip,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CHEST_DEPTH:           entity.BodyChestDepth,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_WAIST_DEPTH:           entity.BodyWaistDepth,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HIP_DEPTH:             entity.BodyHipDepth,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_SHOULDER_LENGTH:       entity.BodyShoulderLength,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_SHOULDER_DROP:         entity.BodyShoulderDrop,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_ARMSCYE_DEPTH:         entity.BodyArmscyeDepth,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_ARMSCYE_CIRCUMFERENCE: entity.BodyArmscyeCircumference,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_BUST_APEX_TO_APEX:     entity.BodyBustApexToApex,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HPS_TO_BUST_APEX:      entity.BodyHPSToBustApex,
}

var bodyMeasurementEntityToPb = func() map[entity.BodyMeasurementName]pb_common.BodyMeasurementName {
	m := make(map[entity.BodyMeasurementName]pb_common.BodyMeasurementName, len(bodyMeasurementPbToEntity))
	for k, v := range bodyMeasurementPbToEntity {
		m[v] = k
	}
	return m
}()

var bodyMeasurementSourcePbToEntity = map[pb_common.BodyMeasurementSource]entity.BodyMeasurementSource{
	pb_common.BodyMeasurementSource_BODY_MEASUREMENT_SOURCE_MANUAL_TAPE:  entity.BodyMeasurementManualTape,
	pb_common.BodyMeasurementSource_BODY_MEASUREMENT_SOURCE_THREE_D_SCAN: entity.BodyMeasurementThreeDScan,
	pb_common.BodyMeasurementSource_BODY_MEASUREMENT_SOURCE_IMPORTED:     entity.BodyMeasurementImported,
	pb_common.BodyMeasurementSource_BODY_MEASUREMENT_SOURCE_DERIVED:      entity.BodyMeasurementDerived,
}

var bodyMeasurementSourceEntityToPb = func() map[entity.BodyMeasurementSource]pb_common.BodyMeasurementSource {
	m := make(map[entity.BodyMeasurementSource]pb_common.BodyMeasurementSource, len(bodyMeasurementSourcePbToEntity))
	for k, v := range bodyMeasurementSourcePbToEntity {
		m[v] = k
	}
	return m
}()

func bodyMeasurementMetadataFromPb(pb *pb_common.BodyMeasurementMetadataV1) (*entity.BodyMeasurementMetadataV1, error) {
	if pb == nil {
		return nil, nil
	}
	if pb.SchemaVersion != entity.BodyMeasurementMetadataSchemaVersionV1 {
		return nil, fmt.Errorf("measurement metadata schema_version must be exactly %d", entity.BodyMeasurementMetadataSchemaVersionV1)
	}
	source, ok := bodyMeasurementSourcePbToEntity[pb.Source]
	if !ok {
		return nil, fmt.Errorf("measurement metadata source must be known: %v", pb.Source)
	}
	if pb.ProtocolId == "" || strings.TrimSpace(pb.ProtocolId) != pb.ProtocolId || utf8.RuneCountInString(pb.ProtocolId) > maxBodyMeasurementProtocolIDChars {
		return nil, fmt.Errorf("measurement metadata protocol_id must be 1..%d trimmed characters", maxBodyMeasurementProtocolIDChars)
	}
	if strings.IndexFunc(pb.ProtocolId, unicode.IsControl) >= 0 {
		return nil, fmt.Errorf("measurement metadata protocol_id must not contain control characters")
	}
	return &entity.BodyMeasurementMetadataV1{
		SchemaVersion: pb.SchemaVersion,
		Source:        source,
		ProtocolID:    pb.ProtocolId,
	}, nil
}

func bodyMeasurementMetadataToPb(metadata *entity.BodyMeasurementMetadataV1) *pb_common.BodyMeasurementMetadataV1 {
	if metadata == nil {
		return nil
	}
	source, ok := bodyMeasurementSourceEntityToPb[metadata.Source]
	if !ok || metadata.SchemaVersion != entity.BodyMeasurementMetadataSchemaVersionV1 || metadata.ProtocolID == "" {
		return nil
	}
	return &pb_common.BodyMeasurementMetadataV1{
		SchemaVersion: metadata.SchemaVersion,
		Source:        source,
		ProtocolId:    metadata.ProtocolID,
	}
}

// ConvertPbModelInsertToEntity converts a pb_common.ModelInsert to entity.ModelInsert,
// validating the name, gender, and the sparse set of body measurements.
func ConvertPbModelInsertToEntity(pb *pb_common.ModelInsert) (*entity.ModelInsert, error) {
	if pb == nil {
		return nil, fmt.Errorf("model insert is nil")
	}
	if pb.Name == "" {
		return nil, fmt.Errorf("model name is required")
	}
	if len(pb.Name) > maxVarchar255 {
		return nil, fmt.Errorf("model name must be at most %d characters", maxVarchar255)
	}

	if pb.ThumbnailId < 0 {
		return nil, fmt.Errorf("thumbnail_id must not be negative")
	}

	gender, err := nullGenderFromPb(pb.Gender)
	if err != nil {
		return nil, err
	}

	measurements := make([]entity.ModelMeasurement, 0, len(pb.Measurements))
	seen := make(map[entity.BodyMeasurementName]bool, len(pb.Measurements))
	for _, m := range pb.Measurements {
		if m == nil {
			return nil, fmt.Errorf("measurement must not be null")
		}
		name, ok := bodyMeasurementPbToEntity[m.Name]
		if !ok {
			return nil, fmt.Errorf("unknown measurement name: %v", m.Name)
		}
		if m.ValueMm <= 0 || m.ValueMm > maxBodyMeasurementMM {
			return nil, fmt.Errorf("measurement %q value out of range (1..%d mm): %d", name, maxBodyMeasurementMM, m.ValueMm)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate measurement name: %q", name)
		}
		metadata, err := bodyMeasurementMetadataFromPb(m.Metadata)
		if err != nil {
			return nil, fmt.Errorf("measurement %q metadata: %w", name, err)
		}
		seen[name] = true
		measurements = append(measurements, entity.ModelMeasurement{
			Name:     name,
			ValueMM:  int(m.ValueMm),
			Metadata: metadata,
		})
	}
	if pb.BodyMeasurementSchemaVersion > entity.BodyMeasurementSchemaVersionP0 {
		return nil, fmt.Errorf("body_measurement_schema_version must be 0 or %d", entity.BodyMeasurementSchemaVersionP0)
	}

	mediaIds := make([]int, 0, len(pb.MediaIds))
	for _, mid := range pb.MediaIds {
		mediaIds = append(mediaIds, int(mid))
	}

	defaultSizeIds := make([]int, 0, len(pb.DefaultSizeIds))
	seenSizes := make(map[int]bool, len(pb.DefaultSizeIds))
	for _, sid := range pb.DefaultSizeIds {
		if sid <= 0 {
			return nil, fmt.Errorf("default_size_ids must be positive")
		}
		if seenSizes[int(sid)] {
			return nil, fmt.Errorf("duplicate default size id: %d", sid)
		}
		seenSizes[int(sid)] = true
		defaultSizeIds = append(defaultSizeIds, int(sid))
	}

	return &entity.ModelInsert{
		Name:                         pb.Name,
		Comment:                      nullStringFromPb(pb.Comment),
		Gender:                       gender,
		ThumbnailId:                  nullInt32FromPb(pb.ThumbnailId),
		Measurements:                 measurements,
		BodyMeasurementSchemaVersion: pb.BodyMeasurementSchemaVersion,
		MediaIds:                     mediaIds,
		DefaultSizeIds:               defaultSizeIds,
	}, nil
}

// ConvertEntityModelToPb converts an entity.Model to pb_common.Model.
func ConvertEntityModelToPb(m *entity.Model) *pb_common.Model {
	if m == nil {
		return nil
	}
	measurements := make([]*pb_common.ModelMeasurement, 0, len(m.Measurements))
	for _, em := range m.Measurements {
		pbName, ok := bodyMeasurementEntityToPb[em.Name]
		if !ok {
			// Defensive: a stored key with no enum mapping would otherwise be
			// emitted as UNKNOWN with a real value. Skip and warn instead.
			slog.Default().Warn("model has unmapped measurement name; skipping",
				slog.Int("model_id", m.Id),
				slog.String("measurement_name", string(em.Name)),
			)
			continue
		}
		measurements = append(measurements, &pb_common.ModelMeasurement{
			Name:     pbName,
			ValueMm:  int32(em.ValueMM),
			Metadata: bodyMeasurementMetadataToPb(em.Metadata),
		})
	}
	media := make([]*pb_common.MediaFull, 0, len(m.Media))
	mediaIds := make([]int32, 0, len(m.Media))
	for i := range m.Media {
		media = append(media, ConvertEntityToCommonMedia(&m.Media[i]))
		mediaIds = append(mediaIds, int32(m.Media[i].Id))
	}

	var thumbnail *pb_common.MediaFull
	if m.Thumbnail != nil {
		thumbnail = ConvertEntityToCommonMedia(m.Thumbnail)
	}

	defaultSizeIds := make([]int32, 0, len(m.DefaultSizeIds))
	for _, sid := range m.DefaultSizeIds {
		defaultSizeIds = append(defaultSizeIds, int32(sid))
	}

	return &pb_common.Model{
		Id: int32(m.Id),
		Model: &pb_common.ModelInsert{
			Name:                         m.Name,
			Comment:                      pbStringFromNull(m.Comment),
			Gender:                       pbGenderFromNull(m.Gender),
			ThumbnailId:                  pbInt32FromNull(m.ThumbnailId),
			Measurements:                 measurements,
			BodyMeasurementSchemaVersion: entity.BodyMeasurementSchemaVersionP0,
			MediaIds:                     mediaIds,
			DefaultSizeIds:               defaultSizeIds,
		},
		Thumbnail: thumbnail,
		Media:     media,
		CreatedAt: timestamppb.New(m.CreatedAt),
		UpdatedAt: timestamppb.New(m.UpdatedAt),
	}
}

// --- shared null/optional helpers (used by model & fitting DTOs) ---

func nullStringFromPb(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func pbStringFromNull(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

// pbOptStringFromNull emits a proto3 optional string: a NULL reads back as an unset
// field rather than "".
func pbOptStringFromNull(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}

func nullInt32FromPb(v int32) sql.NullInt32 {
	if v == 0 {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: v, Valid: true}
}

func nullInt64FromPb(v int64) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}

func pbInt32FromNull(ni sql.NullInt32) int32 {
	if ni.Valid {
		return ni.Int32
	}
	return 0
}

// nullGenderFromPb maps an optional gender enum to a nullable string (UNKNOWN = NULL).
func nullGenderFromPb(g pb_common.GenderEnum) (sql.NullString, error) {
	if g == pb_common.GenderEnum_GENDER_ENUM_UNKNOWN {
		return sql.NullString{}, nil
	}
	eg, err := ConvertPbGenderEnumToEntityGenderEnum(g)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("invalid gender: %w", err)
	}
	return sql.NullString{String: string(eg), Valid: true}, nil
}

// pbGenderFromNull maps a nullable gender string back to the enum (NULL = UNKNOWN).
func pbGenderFromNull(ns sql.NullString) pb_common.GenderEnum {
	if !ns.Valid {
		return pb_common.GenderEnum_GENDER_ENUM_UNKNOWN
	}
	g, _ := ConvertEntityGenderToPbGenderEnum(entity.GenderEnum(ns.String))
	return g
}
