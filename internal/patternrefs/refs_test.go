package patternrefs

import (
	"reflect"
	"testing"
)

func TestUnreferencedObjectURLsIncludesHistoryAndSnapshots(t *testing.T) {
	const (
		active     = "https://cdn.example/base/tech-card-patterns/card/active.dxf"
		history    = "https://spaces.example/base/tech-card-patterns/card/history.dxf"
		snapshot   = "https://cdn.example/base/tech-card-patterns/card/snapshot.dxf"
		release    = "https://cdn.example/base/tech-card-patterns/card/release.dxf"
		orphan     = "https://cdn.example/base/tech-card-patterns/card/orphan.dxf"
		historyCDN = "https://cdn.example/base/tech-card-patterns/card/history.dxf"
		nested     = "https://cdn.example/base/tech-card-patterns/card/nested-history.dxf"
		removed    = "https://cdn.example/base/tech-card-patterns/card/removed-history.dxf"
	)

	snapshotURLs, err := URLsFromSnapshots([]string{
		`{"techCard":{"patterns":[{"url":"` + snapshot + `","history":[{"url":"` + nested + `"}]}]},` +
			`"removedPatternHistory":[{"url":"` + removed + `"}]}`,
		`{"tech_card":{"patterns":[{"url":"` + release + `"}]}}`,
	})
	if err != nil {
		t.Fatalf("extract snapshot urls: %v", err)
	}
	references := append([]string{active, history}, snapshotURLs...)
	got := UnreferencedObjectURLs([]string{active, historyCDN, snapshot, release, nested, removed, orphan}, references)
	if !reflect.DeepEqual(got, []string{orphan}) {
		t.Fatalf("unreferenced = %#v, want only orphan", got)
	}
}

func TestSnapshotCanBeDeletedOnlyAfterLastNonRestoreReference(t *testing.T) {
	cases := []struct {
		name      string
		reason    string
		remaining int
		want      bool
	}{
		{name: "another history row retains it", reason: "dxf-delete", remaining: 1, want: false},
		{name: "replacement history retains implicit snapshot", reason: "pattern replaced", remaining: 1, want: false},
		{name: "removal history retains implicit snapshot", reason: "pattern removed", remaining: 1, want: false},
		{name: "last ordinary reference", reason: "dxf-delete", remaining: 0, want: true},
		{name: "restore snapshot is durable", reason: "restore:42", remaining: 0, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SnapshotCanBeDeleted(tc.reason, tc.remaining); got != tc.want {
				t.Fatalf("SnapshotCanBeDeleted(%q, %d) = %v, want %v", tc.reason, tc.remaining, got, tc.want)
			}
		})
	}
}

func TestObjectURLReferencedKeepsAnotherSpellingOfTheSameObject(t *testing.T) {
	candidate := "https://cdn.example/base/tech-card-patterns/card/revision.dxf"
	otherReference := "https://spaces.example/base/tech-card-patterns/card/revision.dxf"
	if !ObjectURLReferenced(candidate, []string{otherReference}) {
		t.Fatal("origin/CDN spellings of the same retained object must compare as one reference")
	}
}

func TestURLsFromSnapshotsFailsClosed(t *testing.T) {
	if _, err := URLsFromSnapshots([]string{`{"techCard":`}); err == nil {
		t.Fatal("malformed snapshot must be reported so GC can skip deletion")
	}
}
