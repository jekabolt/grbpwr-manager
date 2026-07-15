package entity

import "testing"

// TestArchiveCodeFromID pins the code shape and, implicitly, its parity with the
// SQL backfill in migration 0136: CONCAT('AR', LPAD(UPPER(CONV(id,10,36)),4,'0')).
func TestArchiveCodeFromID(t *testing.T) {
	cases := []struct{ id int; want string }{
		{1, "AR0001"},
		{12, "AR000C"},   // 12 -> base36 "c"
		{35, "AR000Z"},   // 35 -> base36 "z"
		{36, "AR0010"},   // 36 -> base36 "10"
		{1295, "AR00ZZ"}, // 36^2-1 -> "zz"
		{1296, "AR0100"}, // 36^2 -> "100"
	}
	for _, c := range cases {
		if got := ArchiveCodeFromID(c.id); got != c.want {
			t.Errorf("ArchiveCodeFromID(%d) = %q, want %q", c.id, got, c.want)
		}
	}
}
