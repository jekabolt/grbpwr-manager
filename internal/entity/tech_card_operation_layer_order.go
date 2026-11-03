package entity

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

const (
	// TechCardOperationMaxLayerOrder bounds one step's order, as TechCardOperationMaxFoldKeys does.
	TechCardOperationMaxLayerOrder = 64
	// TechCardOperationLayerOrderKeyMaxLen bounds one key: an input key is a piece line key or a
	// unit code of up to 64 characters.
	TechCardOperationLayerOrderKeyMaxLen = 64
)

// TechCardLayerOrder is a step's input keys, bottom to top, kept as a JSON array in
// tech_card_operation.layer_order. Empty is NULL, so a step with no order reads as it always did.
type TechCardLayerOrder []string

// Value implements driver.Valuer.
func (o TechCardLayerOrder) Value() (driver.Value, error) {
	if len(o) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal([]string(o))
	if err != nil {
		return nil, fmt.Errorf("layer order: %w", err)
	}
	return string(raw), nil
}

// Scan implements sql.Scanner. A broken value is an error, as it is for every other stored JSON.
func (o *TechCardLayerOrder) Scan(src any) error {
	*o = nil
	var raw []byte
	switch v := src.(type) {
	case nil:
		return nil
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("layer order: unsupported column type %T", src)
	}
	var keys []string
	if err := json.Unmarshal(raw, &keys); err != nil {
		return fmt.Errorf("layer order: %w", err)
	}
	if len(keys) > 0 {
		*o = keys
	}
	return nil
}
