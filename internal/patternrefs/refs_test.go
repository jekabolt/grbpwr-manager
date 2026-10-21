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
	)

	snapshotURLs, err := URLsFromSnapshots([]string{
		`{"techCard":{"patterns":[{"url":"` + snapshot + `"}]}}`,
		`{"tech_card":{"patterns":[{"url":"` + release + `"}]}}`,
	})
	if err != nil {
		t.Fatalf("extract snapshot urls: %v", err)
	}
	references := append([]string{active, history}, snapshotURLs...)
	got := UnreferencedObjectURLs([]string{active, historyCDN, snapshot, release, orphan}, references)
	if !reflect.DeepEqual(got, []string{orphan}) {
		t.Fatalf("unreferenced = %#v, want only orphan", got)
	}
}

func TestURLsFromSnapshotsFailsClosed(t *testing.T) {
	if _, err := URLsFromSnapshots([]string{`{"techCard":`}); err == nil {
		t.Fatal("malformed snapshot must be reported so GC can skip deletion")
	}
}
