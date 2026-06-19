package dto

import (
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxBodyMeasurementMM bounds an accepted body measurement (5 metres in mm).
const maxBodyMeasurementMM = 5000

// maxVarchar255 bounds inputs stored in VARCHAR(255) columns (model name,
// fitting recorded_by) so over-length input fails as InvalidArgument rather
// than a MySQL 1406 (data too long) Internal error.
const maxVarchar255 = 255

var bodyMeasurementPbToEntity = map[pb_common.BodyMeasurementName]entity.BodyMeasurementName{
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CHEST:              entity.BodyChest,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_UNDER_BUST:         entity.BodyUnderBust,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_WAIST:              entity.BodyWaist,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HIGH_HIP:           entity.BodyHighHip,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HIP:                entity.BodyHip,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_NECK_BASE:          entity.BodyNeckBase,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_ACROSS_SHOULDER:    entity.BodyAcrossShoulder,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_SLEEVE_LENGTH:      entity.BodySleeveLength,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_BICEP:              entity.BodyBicep,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_WRIST:              entity.BodyWrist,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_INSEAM:             entity.BodyInseam,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_THIGH:              entity.BodyThigh,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_KNEE:               entity.BodyKnee,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CALF:               entity.BodyCalf,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_ANKLE:              entity.BodyAnkle,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HEIGHT:             entity.BodyHeight,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HPS_TO_WAIST_FRONT: entity.BodyHPSToWaistFront,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CB_NECK_TO_WAIST:   entity.BodyCBNeckToWaist,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_ACROSS_FRONT:       entity.BodyAcrossFront,
	pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_ACROSS_BACK:        entity.BodyAcrossBack,
}

var bodyMeasurementEntityToPb = func() map[entity.BodyMeasurementName]pb_common.BodyMeasurementName {
	m := make(map[entity.BodyMeasurementName]pb_common.BodyMeasurementName, len(bodyMeasurementPbToEntity))
	for k, v := range bodyMeasurementPbToEntity {
		m[v] = k
	}
	return m
}()

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

	if pb.DefaultSampleSizeId < 0 {
		return nil, fmt.Errorf("default_sample_size_id must not be negative")
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
		seen[name] = true
		measurements = append(measurements, entity.ModelMeasurement{
			Name:    name,
			ValueMM: int(m.ValueMm),
		})
	}

	mediaIds := make([]int, 0, len(pb.MediaIds))
	for _, mid := range pb.MediaIds {
		mediaIds = append(mediaIds, int(mid))
	}

	return &entity.ModelInsert{
		Name:                pb.Name,
		Comment:             nullStringFromPb(pb.Comment),
		Gender:              gender,
		DefaultSampleSizeId: nullInt32FromPb(pb.DefaultSampleSizeId),
		ThumbnailId:         nullInt32FromPb(pb.ThumbnailId),
		Measurements:        measurements,
		MediaIds:            mediaIds,
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
			Name:    pbName,
			ValueMm: int32(em.ValueMM),
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

	return &pb_common.Model{
		Id: int32(m.Id),
		Model: &pb_common.ModelInsert{
			Name:                m.Name,
			Comment:             pbStringFromNull(m.Comment),
			Gender:              pbGenderFromNull(m.Gender),
			DefaultSampleSizeId: pbInt32FromNull(m.DefaultSampleSizeId),
			ThumbnailId:         pbInt32FromNull(m.ThumbnailId),
			Measurements:        measurements,
			MediaIds:            mediaIds,
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

func nullInt32FromPb(v int32) sql.NullInt32 {
	if v == 0 {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: v, Valid: true}
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
