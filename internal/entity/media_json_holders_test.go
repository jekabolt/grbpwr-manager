package entity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The walkers in media_json_holders.go decide which files the media library refuses to delete, so
// the two ways they can go wrong both lose an operator's picture: a walker that misses a field
// calls a shown file free, and a *MediaKeys list that misses a key makes the SQL prefilter drop the
// document before the walker ever sees it. These tests fill every media-looking field of each
// document with its own id and check both, with no database.

// mediaLikeKey picks the JSON keys that can hold a media id. Deliberately broad: a new picture
// field named in any of these ways lands in the fill, and a walker that ignores it fails below.
var mediaLikeKey = regexp.MustCompile(`media|portrait|landscape|poster|logo|image|thumb`)

// mediaFill gives every media-looking int (and every element of a media-looking []int) a unique id
// and remembers the JSON path it went to.
type mediaFill struct {
	next  int
	paths map[int]string
	depth map[reflect.Type]int
}

func newMediaFill() *mediaFill {
	return &mediaFill{next: 1000, paths: map[int]string{}, depth: map[reflect.Type]int{}}
}

func (f *mediaFill) id(path string) int {
	f.next++
	f.paths[f.next] = path
	return f.next
}

// fill walks v (addressable) and fills it. Pointers to structs are allocated and slices of structs
// get two elements, each type at most twice on the current path, so the recursive email two-column
// block is filled one level deep and the walk ends.
func (f *mediaFill) fill(v reflect.Value, path string) {
	t := v.Type()
	if t.Kind() != reflect.Struct {
		return
	}
	f.depth[t]++
	defer func() { f.depth[t]-- }()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(sf.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		at := name
		if path != "" {
			at = path + "." + name
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Int, reflect.Int32, reflect.Int64:
			if mediaLikeKey.MatchString(name) {
				fv.SetInt(int64(f.id(at)))
			}
		case reflect.Struct:
			f.fill(fv, at)
		case reflect.Pointer:
			if el := sf.Type.Elem(); el.Kind() == reflect.Struct && f.depth[el] < 2 {
				fv.Set(reflect.New(el))
				f.fill(fv.Elem(), at)
			}
		case reflect.Slice:
			el := sf.Type.Elem()
			switch {
			case el.Kind() == reflect.Int && mediaLikeKey.MatchString(name):
				fv.Set(reflect.ValueOf([]int{f.id(at + "[0]"), f.id(at + "[1]")}).Convert(sf.Type))
			case el.Kind() == reflect.Struct && f.depth[el] < 2:
				s := reflect.MakeSlice(sf.Type, 2, 2)
				for j := 0; j < 2; j++ {
					f.fill(s.Index(j), fmt.Sprintf("%s[%d]", at, j))
				}
				fv.Set(s)
			}
		}
	}
}

// idsUnder returns the filled ids whose path starts with any of the prefixes, sorted.
func (f *mediaFill) idsUnder(prefixes ...string) []int {
	out := []int{}
	for id, p := range f.paths {
		for _, pre := range prefixes {
			if strings.HasPrefix(p, pre) {
				out = append(out, id)
				break
			}
		}
	}
	sort.Ints(out)
	return out
}

func sortedSlotIds(slots []MediaSlot) []int {
	ids := MediaSlotIds(slots)
	sort.Ints(ids)
	return ids
}

// The payload key each block type selects — the hero resolver reads only that one.
var heroPayloadKey = map[HeroType]string{
	HeroTypeSingle: "single", HeroTypeDouble: "double", HeroTypeMain: "main",
	HeroTypeFeaturedProducts: "featured_products", HeroTypeFeaturedProductsTag: "featured_products_tag",
	HeroTypeFeaturedArchive: "featured_archive", HeroTypeEmbed: "embed", HeroTypeDrop: "drop",
	HeroTypeLastChance: "last_chance", HeroTypeMarquee: "marquee", HeroTypeNewArrivals: "new_arrivals",
	HeroTypeSlideshow: "slideshow", HeroTypeMosaic: "mosaic", HeroTypeSplit: "split",
	HeroTypeVideo: "video", HeroTypeProductSpotlight: "product_spotlight",
	HeroTypeNewsletter: "newsletter", HeroTypeStatement: "statement", HeroTypeLookbook: "lookbook",
}

var archivePayloadKey = map[ArchiveItemType]string{
	ArchiveItemTypeMainMedia: "main_media", ArchiveItemTypeMediaLine: "media_line",
	ArchiveItemTypeText: "text", ArchiveItemTypeEmbed: "embed",
	ArchiveItemTypeMediaWithCaption: "media_with_caption", ArchiveItemTypeProduct: "product",
	ArchiveItemTypeProductsTag: "products_tag", ArchiveItemTypeProductsManual: "products_manual",
}

// filledHero is a hero with one block of type t, every payload of it filled (the stale ones too),
// and both nav pictures.
func filledHero(t HeroType) (HeroFullInsert, *mediaFill) {
	f := newMediaFill()
	var e HeroEntityInsert
	f.fill(reflect.ValueOf(&e).Elem(), "entities[0]")
	e.Type = t
	h := HeroFullInsert{Entities: []HeroEntityInsert{e}}
	f.fill(reflect.ValueOf(&h.NavFeatured).Elem(), "nav_featured")
	return h, f
}

func filledArchive(t ArchiveItemType) ([]ArchiveItemInsert, *mediaFill) {
	f := newMediaFill()
	var it ArchiveItemInsert
	f.fill(reflect.ValueOf(&it).Elem(), "[0]")
	it.Type = t
	return []ArchiveItemInsert{it}, f
}

func filledEmail() ([]EmailBlock, *mediaFill) {
	f := newMediaFill()
	var b EmailBlock
	f.fill(reflect.ValueOf(&b).Elem(), "[0]")
	return []EmailBlock{b}, f
}

func filledColourPlan() ([]DesignColourMap, *mediaFill) {
	f := newMediaFill()
	maps := make([]DesignColourMap, 2)
	for i := range maps {
		f.fill(reflect.ValueOf(&maps[i]).Elem(), fmt.Sprintf("[%d]", i))
	}
	maps[0].View, maps[1].View = "front", "back"
	return maps, f
}

func TestHeroMediaSlotsReadsExactlyTheSelectedPayload(t *testing.T) {
	require.Len(t, heroPayloadKey, int(HeroTypeLookbook), "a hero type without a payload key here is untested")
	shows := map[HeroType]bool{
		HeroTypeSingle: true, HeroTypeDouble: true, HeroTypeMain: true, HeroTypeEmbed: true,
		HeroTypeDrop: true, HeroTypeSlideshow: true, HeroTypeMosaic: true, HeroTypeSplit: true,
		HeroTypeVideo: true, HeroTypeProductSpotlight: true, HeroTypeNewsletter: true,
		HeroTypeStatement: true, HeroTypeLookbook: true,
	}
	for typ, key := range heroPayloadKey {
		h, f := filledHero(typ)
		if shows[typ] {
			require.NotEmpty(t, f.idsUnder("entities[0]."+key+"."), "the fill must reach the pictures of hero type %d (%s)", typ, key)
		}
		want := f.idsUnder("entities[0]."+key+".", "nav_featured.")
		assert.Equal(t, want, sortedSlotIds(HeroMediaSlots(h)),
			"hero type %d (%s): the walker must count every picture of its own payload and the nav, nothing of the others", typ, key)
	}

	// An unknown type shows nothing of its payloads; the nav still holds.
	h, f := filledHero(HeroTypeUnknown)
	assert.Equal(t, f.idsUnder("nav_featured."), sortedSlotIds(HeroMediaSlots(h)))
}

func TestArchiveBodyMediaSlotsReadsExactlyTheSelectedPayload(t *testing.T) {
	require.Len(t, archivePayloadKey, int(ArchiveItemTypeProductsManual), "an archive block type without a payload key here is untested")
	for typ, key := range archivePayloadKey {
		items, f := filledArchive(typ)
		assert.Equal(t, f.idsUnder("[0]."+key+"."), sortedSlotIds(ArchiveBodyMediaSlots(items)),
			"archive block type %d (%s)", typ, key)
	}
	// A payload whose pointer is missing is not a panic.
	assert.Empty(t, ArchiveBodyMediaSlots([]ArchiveItemInsert{{Type: ArchiveItemTypeMediaLine}}))
}

func TestEmailBodyMediaSlotsReadsEveryPayloadAndTwoColumnChildren(t *testing.T) {
	blocks, f := filledEmail()
	got := sortedSlotIds(EmailBodyMediaSlots(blocks))
	assert.Equal(t, f.idsUnder("[0]."), got, "the email walker reads every present payload, like the renderer's preload")
	require.NotEmpty(t, f.idsUnder("[0].two_column.left[1]."), "the fill must reach a two-column child")
}

func TestColourPlanMediaSlotsCountsTheMapNotItsBase(t *testing.T) {
	maps, f := filledColourPlan()
	slots := ColourPlanMediaSlots(maps)
	assert.Equal(t, []MediaSlot{
		{MediaId: maps[0].MediaId, Slot: "colour map, front"},
		{MediaId: maps[1].MediaId, Slot: "colour map, back"},
	}, slots)
	for _, id := range f.idsUnder("[0].base_media_id", "[1].base_media_id") {
		assert.NotContains(t, MediaSlotIds(slots), id, "the flat a map was painted over is not held by the plan")
	}
}

// jsonKeysOf maps every number in a JSON document to the object key it sits under; an array
// element takes the key of its array. That is exactly what '$**.k' and '$**.k[*]' select.
func jsonKeysOf(t *testing.T, doc any) map[int]string {
	t.Helper()
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	require.NoError(t, dec.Decode(&v))
	out := map[int]string{}
	var walk func(v any, key string)
	walk = func(v any, key string) {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				walk(c, k)
			}
		case []any:
			for _, c := range x {
				walk(c, key)
			}
		case json.Number:
			if n, err := x.Int64(); err == nil && n >= 1000 {
				out[int(n)] = key
			}
		}
	}
	walk(v, "")
	return out
}

// TestMediaJSONHolderKeysCoverWalkers ties each *MediaKeys list — the SQL prefilter of the usage
// scan — to its walker: every id a walker reports must sit, in the stored JSON, under a key the list
// names, or the prefilter drops the document and the library calls a shown file free. And every key
// the list names must be one the walker reads, so the list stays a statement of fact.
func TestMediaJSONHolderKeysCoverWalkers(t *testing.T) {
	type walk struct {
		doc   any
		slots []MediaSlot
	}
	cases := map[string]struct {
		keys  []string
		walks []walk
	}{}

	var hero []walk
	for typ := range heroPayloadKey {
		h, _ := filledHero(typ)
		hero = append(hero, walk{h, HeroMediaSlots(h)})
	}
	cases["hero"] = struct {
		keys  []string
		walks []walk
	}{HeroMediaKeys, hero}

	var archive []walk
	for typ := range archivePayloadKey {
		items, _ := filledArchive(typ)
		archive = append(archive, walk{items, ArchiveBodyMediaSlots(items)})
	}
	cases["archive"] = struct {
		keys  []string
		walks []walk
	}{ArchiveBodyMediaKeys, archive}

	blocks, _ := filledEmail()
	cases["email"] = struct {
		keys  []string
		walks []walk
	}{EmailBodyMediaKeys, []walk{{blocks, EmailBodyMediaSlots(blocks)}}}

	maps, _ := filledColourPlan()
	cases["colour plan"] = struct {
		keys  []string
		walks []walk
	}{ColourPlanMediaKeys, []walk{{maps, ColourPlanMediaSlots(maps)}}}

	for name, c := range cases {
		listed := map[string]bool{}
		for _, k := range c.keys {
			listed[k] = true
		}
		read := map[string]bool{}
		for _, w := range c.walks {
			keys := jsonKeysOf(t, w.doc)
			for _, s := range w.slots {
				k, ok := keys[s.MediaId]
				require.True(t, ok, "%s: id %d (%s) is not in the stored JSON", name, s.MediaId, s.Slot)
				assert.True(t, listed[k], "%s: the walker reads %q (%s) but the prefilter keys lack it", name, k, s.Slot)
				read[k] = true
			}
		}
		for k := range listed {
			assert.True(t, read[k], "%s: prefilter key %q is never read by the walker", name, k)
		}
	}
}

func TestMediaSlotStrings(t *testing.T) {
	h := HeroFullInsert{
		Entities: []HeroEntityInsert{
			{Type: HeroTypeVideo, Video: HeroVideoInsert{MediaId: 5, PosterMediaId: 6}},
			{Type: HeroTypeSlideshow, Slideshow: HeroSlideshowInsert{Slides: []HeroSingleInsert{
				{Media: HeroMedia{PortraitId: 7, LandscapeId: 7}}, {Media: HeroMedia{PortraitId: 8}},
			}}},
		},
		NavFeatured: NavFeaturedInsert{Men: NavFeaturedEntityInsert{MediaId: 9}},
	}
	assert.Equal(t, []MediaSlot{
		{5, "video, block 1"}, {6, "video poster, block 1"},
		{7, "slideshow, block 2"}, {8, "slideshow, block 2"},
		{9, "nav men"},
	}, HeroMediaSlots(h), "one file in both orientations of one block is one use")

	email := []EmailBlock{
		{Header: &EmailHeaderBlock{LogoMediaID: 1}},
		{TwoColumn: &EmailTwoColumnBlock{
			Left:  []EmailBlock{{ImageLink: &EmailImageLinkBlock{MediaID: 2}}},
			Right: []EmailBlock{{}, {VideoThumb: &EmailVideoThumbBlock{MediaID: 3}}},
		}},
	}
	assert.Equal(t, []MediaSlot{
		{1, "header logo, block 1"}, {2, "image, block 2 left 1"}, {3, "video thumbnail, block 2 right 2"},
	}, EmailBodyMediaSlots(email))

	archive := []ArchiveItemInsert{
		{Type: ArchiveItemTypeText},
		{Type: ArchiveItemTypeMediaLine, MediaLine: &ArchiveMediaLineInsert{MediaIds: []int{4, 0, 4, 5}}},
	}
	assert.Equal(t, []MediaSlot{{4, "media line, block 2"}, {5, "media line, block 2"}},
		ArchiveBodyMediaSlots(archive), "zero is no picture and a repeat in one block is one use")

	assert.Equal(t, []int{3, 1, 2}, MediaSlotIds([]MediaSlot{{3, "a"}, {1, "b"}, {3, "c"}, {2, "d"}}))
}
