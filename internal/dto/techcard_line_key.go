package dto

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
)

var techCardLineKeyEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// mintTechCardLineKey creates the stable 26-character identity used by BOM lines and cut-pieces.
// RPC payloads receive keys before section digests are stamped; the store keeps its own fallback for
// direct callers that bypass DTO conversion.
func mintTechCardLineKey() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("read randomness for tech card line key: %w", err)
	}
	return techCardLineKeyEncoding.EncodeToString(raw[:]), nil
}
