package entity

import (
	"database/sql"
	"time"
)

// BodyMeasurementName is the canonical key for a fit-model body measurement.
// It mirrors the common.BodyMeasurementName proto enum and is stored as a string
// in model_measurement.measurement_name.
type BodyMeasurementName string

const (
	BodyChest                BodyMeasurementName = "chest"
	BodyUnderBust            BodyMeasurementName = "under_bust"
	BodyWaist                BodyMeasurementName = "waist"
	BodyHighHip              BodyMeasurementName = "high_hip"
	BodyHip                  BodyMeasurementName = "hip"
	BodyNeckBase             BodyMeasurementName = "neck_base"
	BodyAcrossShoulder       BodyMeasurementName = "across_shoulder"
	BodySleeveLength         BodyMeasurementName = "sleeve_length"
	BodyBicep                BodyMeasurementName = "bicep"
	BodyWrist                BodyMeasurementName = "wrist"
	BodyInseam               BodyMeasurementName = "inseam"
	BodyThigh                BodyMeasurementName = "thigh"
	BodyKnee                 BodyMeasurementName = "knee"
	BodyCalf                 BodyMeasurementName = "calf"
	BodyAnkle                BodyMeasurementName = "ankle"
	BodyHeight               BodyMeasurementName = "height"
	BodyHPSToWaistFront      BodyMeasurementName = "hps_to_waist_front"
	BodyCBNeckToWaist        BodyMeasurementName = "cb_neck_to_waist"
	BodyAcrossFront          BodyMeasurementName = "across_front"
	BodyAcrossBack           BodyMeasurementName = "across_back"
	BodyHPSHeight            BodyMeasurementName = "hps_height"
	BodyCFNeckToWaist        BodyMeasurementName = "cf_neck_to_waist"
	BodyWaistHeight          BodyMeasurementName = "waist_height"
	BodyWaistToHighHip       BodyMeasurementName = "waist_to_high_hip"
	BodyWaistToHip           BodyMeasurementName = "waist_to_hip"
	BodyChestDepth           BodyMeasurementName = "chest_depth"
	BodyWaistDepth           BodyMeasurementName = "waist_depth"
	BodyHipDepth             BodyMeasurementName = "hip_depth"
	BodyShoulderLength       BodyMeasurementName = "shoulder_length"
	BodyShoulderDrop         BodyMeasurementName = "shoulder_drop"
	BodyArmscyeDepth         BodyMeasurementName = "armscye_depth"
	BodyArmscyeCircumference BodyMeasurementName = "armscye_circumference"
	BodyBustApexToApex       BodyMeasurementName = "bust_apex_to_apex"
	BodyHPSToBustApex        BodyMeasurementName = "hps_to_bust_apex"
)

// ValidBodyMeasurementNames is the set of accepted measurement keys.
var ValidBodyMeasurementNames = map[BodyMeasurementName]bool{
	BodyChest:                true,
	BodyUnderBust:            true,
	BodyWaist:                true,
	BodyHighHip:              true,
	BodyHip:                  true,
	BodyNeckBase:             true,
	BodyAcrossShoulder:       true,
	BodySleeveLength:         true,
	BodyBicep:                true,
	BodyWrist:                true,
	BodyInseam:               true,
	BodyThigh:                true,
	BodyKnee:                 true,
	BodyCalf:                 true,
	BodyAnkle:                true,
	BodyHeight:               true,
	BodyHPSToWaistFront:      true,
	BodyCBNeckToWaist:        true,
	BodyAcrossFront:          true,
	BodyAcrossBack:           true,
	BodyHPSHeight:            true,
	BodyCFNeckToWaist:        true,
	BodyWaistHeight:          true,
	BodyWaistToHighHip:       true,
	BodyWaistToHip:           true,
	BodyChestDepth:           true,
	BodyWaistDepth:           true,
	BodyHipDepth:             true,
	BodyShoulderLength:       true,
	BodyShoulderDrop:         true,
	BodyArmscyeDepth:         true,
	BodyArmscyeCircumference: true,
	BodyBustApexToApex:       true,
	BodyHPSToBustApex:        true,
}

// BodyMeasurementSchemaVersionP0 is emitted to measurement-aware clients. A
// request with version 0 is a legacy writer and cannot authoritatively delete
// P0 fields that it could not represent.
const BodyMeasurementSchemaVersionP0 uint32 = 1

// P0BodyMeasurementNames is the append-only set introduced by schema version 1.
var P0BodyMeasurementNames = map[BodyMeasurementName]bool{
	BodyHPSHeight:            true,
	BodyCFNeckToWaist:        true,
	BodyWaistHeight:          true,
	BodyWaistToHighHip:       true,
	BodyWaistToHip:           true,
	BodyChestDepth:           true,
	BodyWaistDepth:           true,
	BodyHipDepth:             true,
	BodyShoulderLength:       true,
	BodyShoulderDrop:         true,
	BodyArmscyeDepth:         true,
	BodyArmscyeCircumference: true,
	BodyBustApexToApex:       true,
	BodyHPSToBustApex:        true,
}

// BodyMeasurementSource records acquisition provenance; it does not make an
// accuracy, fit, or landmark-validity claim.
type BodyMeasurementSource string

const (
	BodyMeasurementManualTape BodyMeasurementSource = "manual_tape"
	BodyMeasurementThreeDScan BodyMeasurementSource = "three_d_scan"
	BodyMeasurementImported   BodyMeasurementSource = "imported"
	BodyMeasurementDerived    BodyMeasurementSource = "derived"
)

var ValidBodyMeasurementSources = map[BodyMeasurementSource]bool{
	BodyMeasurementManualTape: true,
	BodyMeasurementThreeDScan: true,
	BodyMeasurementImported:   true,
	BodyMeasurementDerived:    true,
}

const BodyMeasurementMetadataSchemaVersionV1 uint32 = 1

type BodyMeasurementMetadataV1 struct {
	SchemaVersion uint32
	Source        BodyMeasurementSource
	ProtocolID    string
}

// ModelMeasurement is a single body measurement value, in millimetres.
type ModelMeasurement struct {
	Name     BodyMeasurementName        `db:"measurement_name"`
	ValueMM  int                        `db:"measurement_value_mm"`
	Metadata *BodyMeasurementMetadataV1 `db:"-"`
}

// ModelInsert is the writable payload for a fit-model profile. Measurements are
// sparse: only the filled-in ones are present.
type ModelInsert struct {
	Name                         string             `db:"name"`
	Comment                      sql.NullString     `db:"comment"`
	Gender                       sql.NullString     `db:"gender"`
	ThumbnailId                  sql.NullInt32      `db:"thumbnail_id"`
	Measurements                 []ModelMeasurement `db:"-"`
	BodyMeasurementSchemaVersion uint32             `db:"-"`
	MediaIds                     []int              `db:"-"`
	DefaultSizeIds               []int              `db:"-"`
}

// Model is a stored fit-model profile (model table row + measurements + media).
type Model struct {
	Id int `db:"id"`
	ModelInsert
	CreatedAt time.Time   `db:"created_at"`
	UpdatedAt time.Time   `db:"updated_at"`
	Thumbnail *MediaFull  `db:"-"`
	Media     []MediaFull `db:"-"`
}
