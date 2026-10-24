package techcard

import (
	"strings"
	"testing"
)

// The pre-image has to be assembled after the card-row lock but before the header/document rewrite.
// This source-order alarm complements the real-DB seam race probe in the parent store package.
func TestUpdateSnapshotBuilderRunsUnderTheCardLockBeforeTheRewrite(t *testing.T) {
	body := funcBody(t, "techcard.go", "func (s *Store) UpdateTechCardTx")
	lock := strings.Index(body, "FROM tech_card WHERE id = :id FOR UPDATE")
	reload := strings.Index(body, "rep.TechCards().GetTechCardById(ctx, id)")
	build := strings.Index(body, "tc.BuildSnapshotFromLockedCard(locked)")
	rewrite := strings.Index(body, "techCardUpdateHeaderSQL")
	if lock < 0 || reload < 0 || build < 0 || rewrite < 0 {
		t.Fatalf("snapshot lock path changed shape: lock=%d reload=%d build=%d rewrite=%d", lock, reload, build, rewrite)
	}
	if !(lock < reload && reload < build && build < rewrite) {
		t.Fatalf("snapshot order must be card lock -> transactional reload -> build -> rewrite; got lock=%d reload=%d build=%d rewrite=%d",
			lock, reload, build, rewrite)
	}
}
