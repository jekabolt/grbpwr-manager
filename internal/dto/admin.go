package dto

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// maxAdminSpecialtyNameLen mirrors admin_specialty.name. A longer one is a
// mistake, not something to silently truncate — the same rule as a topic name.
const maxAdminSpecialtyNameLen = 64

// maxAdminSpecialties bounds one account's set. Specialties describe a person,
// and a person who does thirty different things has described nothing; the cap
// exists so a loop cannot turn the byline into a paragraph.
const maxAdminSpecialties = 12

// ValidateAdminSpecialtyName trims and bounds a specialty name.
func ValidateAdminSpecialtyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("specialty name is required")
	}
	if len([]rune(name)) > maxAdminSpecialtyNameLen {
		return "", fmt.Errorf("specialty name must be at most %d characters", maxAdminSpecialtyNameLen)
	}
	if strings.ContainsAny(name, "\r\n\x00") {
		return "", fmt.Errorf("specialty name must not contain control characters")
	}
	return name, nil
}

// ConvertPbSpecialtySelectionToEntity normalises the (specialty_ids,
// new_specialties) pair. Exactly the shape ConvertPbTopicSelectionToEntity has,
// and deliberately so: it is the same grammar on screen ("pick from the list or
// type your own"), so it must be the same grammar on the wire. Ids are deduped,
// names trimmed, validated and deduped case-insensitively — "Фотограф" typed
// twice must not try to create two entries in one request.
func ConvertPbSpecialtySelectionToEntity(specialtyIDs []int32, newSpecialties []string) ([]int, []string, error) {
	ids := make([]int, 0, len(specialtyIDs))
	seenID := make(map[int]bool, len(specialtyIDs))
	for _, id := range specialtyIDs {
		if id <= 0 {
			return nil, nil, fmt.Errorf("specialty id must be positive")
		}
		if seenID[int(id)] {
			continue
		}
		seenID[int(id)] = true
		ids = append(ids, int(id))
	}
	names := make([]string, 0, len(newSpecialties))
	seenName := make(map[string]bool, len(newSpecialties))
	for _, n := range newSpecialties {
		name, err := ValidateAdminSpecialtyName(n)
		if err != nil {
			return nil, nil, err
		}
		key := strings.ToLower(name)
		if seenName[key] {
			continue
		}
		seenName[key] = true
		names = append(names, name)
	}
	if len(ids)+len(names) > maxAdminSpecialties {
		return nil, nil, fmt.Errorf("an account can carry at most %d specialties", maxAdminSpecialties)
	}
	return ids, names, nil
}

// ConvertEntityAdminRefToPb converts one person for a picker. It carries WHO
// somebody is and WHAT THEY SAY THEY DO — never what they may do: permissions
// travel on ListAccounts, which is gated on the accounts section.
func ConvertEntityAdminRefToPb(a entity.AdminRef) *pb_admin.AdminRef {
	return &pb_admin.AdminRef{
		Id:          int32(a.Id),
		Username:    a.Username,
		Specialties: a.Specialties,
		IsSuper:     a.IsSuper,
	}
}

// ConvertEntityAdminRefsToPb converts a set of people, preserving order.
func ConvertEntityAdminRefsToPb(refs []entity.AdminRef) []*pb_admin.AdminRef {
	out := make([]*pb_admin.AdminRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, ConvertEntityAdminRefToPb(r))
	}
	return out
}
