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
	BodyChest           BodyMeasurementName = "chest"
	BodyUnderBust       BodyMeasurementName = "under_bust"
	BodyWaist           BodyMeasurementName = "waist"
	BodyHighHip         BodyMeasurementName = "high_hip"
	BodyHip             BodyMeasurementName = "hip"
	BodyNeckBase        BodyMeasurementName = "neck_base"
	BodyAcrossShoulder  BodyMeasurementName = "across_shoulder"
	BodySleeveLength    BodyMeasurementName = "sleeve_length"
	BodyBicep           BodyMeasurementName = "bicep"
	BodyWrist           BodyMeasurementName = "wrist"
	BodyInseam          BodyMeasurementName = "inseam"
	BodyThigh           BodyMeasurementName = "thigh"
	BodyKnee            BodyMeasurementName = "knee"
	BodyCalf            BodyMeasurementName = "calf"
	BodyAnkle           BodyMeasurementName = "ankle"
	BodyHeight          BodyMeasurementName = "height"
	BodyHPSToWaistFront BodyMeasurementName = "hps_to_waist_front"
	BodyCBNeckToWaist   BodyMeasurementName = "cb_neck_to_waist"
	BodyAcrossFront     BodyMeasurementName = "across_front"
	BodyAcrossBack      BodyMeasurementName = "across_back"
)

// ValidBodyMeasurementNames is the set of accepted measurement keys.
var ValidBodyMeasurementNames = map[BodyMeasurementName]bool{
	BodyChest:           true,
	BodyUnderBust:       true,
	BodyWaist:           true,
	BodyHighHip:         true,
	BodyHip:             true,
	BodyNeckBase:        true,
	BodyAcrossShoulder:  true,
	BodySleeveLength:    true,
	BodyBicep:           true,
	BodyWrist:           true,
	BodyInseam:          true,
	BodyThigh:           true,
	BodyKnee:            true,
	BodyCalf:            true,
	BodyAnkle:           true,
	BodyHeight:          true,
	BodyHPSToWaistFront: true,
	BodyCBNeckToWaist:   true,
	BodyAcrossFront:     true,
	BodyAcrossBack:      true,
}

// ModelMeasurement is a single body measurement value, in millimetres.
type ModelMeasurement struct {
	Name    BodyMeasurementName `db:"measurement_name"`
	ValueMM int                 `db:"measurement_value_mm"`
}

// ModelInsert is the writable payload for a fit-model profile. Measurements are
// sparse: only the filled-in ones are present.
type ModelInsert struct {
	Name                string             `db:"name"`
	Comment             sql.NullString     `db:"comment"`
	Gender              sql.NullString     `db:"gender"`
	DefaultSampleSizeId sql.NullInt32      `db:"default_sample_size_id"`
	Measurements        []ModelMeasurement `db:"-"`
}

// Model is a stored fit-model profile (model table row + measurements).
type Model struct {
	Id int `db:"id"`
	ModelInsert
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}
