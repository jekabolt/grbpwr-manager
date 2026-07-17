// Package materialattr is the single source of truth for the closed value sets of a material's
// typed CTI attributes (S15). It mirrors the DB CHECK constraints (migration 0157) and the proto
// attribute messages; golden tests keep the three in lock-step, giving the class-table-inheritance
// design the drift protection a JSON-schema/oneof approach would have had for free. Validate turns a
// bad enum value into a field-tagged error (S24) at the app layer, before the DB CHECK would.
package materialattr

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

//go:embed material-attributes-v1.json
var fixtureJSON []byte

type classSchema struct {
	EnumAttrs map[string][]string `json:"enum_attrs"`
}

type schema struct {
	Version int                    `json:"version"`
	Classes map[string]classSchema `json:"classes"`
}

var loaded schema

func init() {
	if err := json.Unmarshal(fixtureJSON, &loaded); err != nil {
		panic(fmt.Sprintf("materialattr: invalid material-attributes fixture: %v", err))
	}
}

// AllowedEnumValues returns the fixture's allowed values for an enum-constrained attribute of a
// material class, or nil when the class/attribute carries no enum constraint.
func AllowedEnumValues(class, attr string) []string {
	return loaded.Classes[class].EnumAttrs[attr]
}

// Validate checks a material's enum-constrained typed attributes against the fixture and returns a
// field-tagged error (S24) on the first violation. Numeric/free-text attributes are not checked
// here — their column types and range CHECKs cover them; this guards the string enums that would
// otherwise drift as free text.
func Validate(ins *entity.MaterialInsert) error {
	if ins.FabricAttr != nil && ins.FabricAttr.FabricDirection.Valid {
		if dir := strings.TrimSpace(ins.FabricAttr.FabricDirection.String); dir != "" && !isAllowed("fabric", "fabric_direction", dir) {
			return entity.NewFieldViolation(
				"material.fabric_attrs.fabric_direction",
				fmt.Sprintf("unknown fabric direction %q", dir),
				"",
				"use one of: "+strings.Join(AllowedEnumValues("fabric", "fabric_direction"), ", "),
			)
		}
	}
	return nil
}

func isAllowed(class, attr, val string) bool {
	for _, v := range AllowedEnumValues(class, attr) {
		if v == val {
			return true
		}
	}
	return false
}
