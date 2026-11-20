package entity

import (
	"strconv"
	"strings"
)

// NamedRatios is the server's list of aspect ratios that have a name in the library: the ratio
// filter and the ratio facet speak in these keys, and the client uses them verbatim. Copied once
// from the admin client, src/components/managers/media/utils/calculate-aspect.ts (NAMED_RATIOS);
// the crop/slot matching in the client's MediaSelector stays client-side and is not affected.
//
// A ratio is the reduced width:height of a media row's thumbnail (the same gcd reduction as the
// client's calculateAspectRatio), compared exactly: 1080x1350 is 4:5, 1447x1080 is not named.
var NamedRatios = []string{"16:9", "4:3", "2:1", "1:1", "4:5", "3:4", "5:4", "9:16"}

// LibraryRatioOther is the ratio key of every media row with known dimensions whose reduced ratio
// is not one of NamedRatios.
const LibraryRatioOther = "other"

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// ParseLibraryRatio reads "w:h" (two positive integers, at most 5 digits each) and returns it
// reduced. "4:5" and "8:10" both answer (4, 5). Anything else is not a ratio.
func ParseLibraryRatio(s string) (w, h int, ok bool) {
	a, b, found := strings.Cut(strings.TrimSpace(s), ":")
	if !found {
		return 0, 0, false
	}
	parse := func(v string) (int, bool) {
		v = strings.TrimSpace(v)
		if v == "" || len(v) > 5 {
			return 0, false
		}
		for _, r := range v {
			if r < '0' || r > '9' {
				return 0, false
			}
		}
		n, err := strconv.Atoi(v)
		return n, err == nil && n > 0
	}
	w, okW := parse(a)
	h, okH := parse(b)
	if !okW || !okH {
		return 0, 0, false
	}
	g := gcd(w, h)
	return w / g, h / g, true
}

// LibraryRatioKey reduces thumbnail dimensions to a facet key: a NamedRatios entry, or
// LibraryRatioOther. Rows without both dimensions have no ratio ("").
func LibraryRatioKey(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	g := gcd(width, height)
	key := strconv.Itoa(width/g) + ":" + strconv.Itoa(height/g)
	for _, n := range NamedRatios {
		if n == key {
			return key
		}
	}
	return LibraryRatioOther
}
