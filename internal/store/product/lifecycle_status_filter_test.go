package product

import (
	"reflect"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestLifecycleStatusFilter locks the ADMIN paged-list statuses -> SQL mapping (task: honour the full
// statuses filter). An empty or all-invalid set falls back to ACTIVE-only (never widens exposure); a valid
// set becomes an IN-list with []int args (so sqlx.In expands it, not treats a []byte as a scalar); invalid
// and duplicate statuses are dropped while caller order is preserved. Storefront tier gating is never part
// of this clause — it lives only in the non-admin branch of GetProductsPaged.
func TestLifecycleStatusFilter(t *testing.T) {
	cases := []struct {
		name       string
		in         []entity.ColorwayStatus
		wantClause string
		wantArgs   []int
	}{
		{"empty -> active-only default", nil, "p.lifecycle_status = 2", nil},
		{"only unknown -> active-only fallback", []entity.ColorwayStatus{entity.ColorwayStatusUnknown}, "p.lifecycle_status = 2", nil},
		{"only out-of-range -> active-only fallback", []entity.ColorwayStatus{entity.ColorwayStatus(9)}, "p.lifecycle_status = 2", nil},
		{"archived only returns archived", []entity.ColorwayStatus{entity.ColorwayStatusArchived}, "p.lifecycle_status IN (:lifecycleStatuses)", []int{4}},
		{"draft only returns draft", []entity.ColorwayStatus{entity.ColorwayStatusDraft}, "p.lifecycle_status IN (:lifecycleStatuses)", []int{1}},
		{"hidden only returns hidden", []entity.ColorwayStatus{entity.ColorwayStatusHidden}, "p.lifecycle_status IN (:lifecycleStatuses)", []int{3}},
		{"active explicit -> IN, not =", []entity.ColorwayStatus{entity.ColorwayStatusActive}, "p.lifecycle_status IN (:lifecycleStatuses)", []int{2}},
		{"draft+hidden union", []entity.ColorwayStatus{entity.ColorwayStatusDraft, entity.ColorwayStatusHidden}, "p.lifecycle_status IN (:lifecycleStatuses)", []int{1, 3}},
		{"active+archived union preserves order", []entity.ColorwayStatus{entity.ColorwayStatusArchived, entity.ColorwayStatusActive}, "p.lifecycle_status IN (:lifecycleStatuses)", []int{4, 2}},
		{"dedup and drop invalid", []entity.ColorwayStatus{entity.ColorwayStatusHidden, entity.ColorwayStatusHidden, entity.ColorwayStatusUnknown, entity.ColorwayStatusArchived}, "p.lifecycle_status IN (:lifecycleStatuses)", []int{3, 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clause, args := lifecycleStatusFilter(tc.in)
			if clause != tc.wantClause {
				t.Errorf("clause = %q, want %q", clause, tc.wantClause)
			}
			if !reflect.DeepEqual(args, tc.wantArgs) {
				t.Errorf("args = %v, want %v", args, tc.wantArgs)
			}
		})
	}
}
