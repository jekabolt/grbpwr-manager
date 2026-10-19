package entity

import (
	"fmt"
	"strings"
)

// MEDIA HELD INSIDE JSON — the documents that show a media file while no foreign key says so.
//
// The home page hero (hero.data), an archive's timeline (archive.body), an email campaign's body
// (email_campaign.body, email_campaign_variant.body) and a card's colour plan
// (design_colour_plan.maps) each cite media by id inside a JSON document. The database cannot see
// those ids, so neither can the usage registry's UNION of foreign-key columns; before the walkers
// below, the media library called such a file free and its delete went through, leaving a blank
// block on the site, a broken image in a sent email or a lost painting on a card.
//
// Each walker answers one question — "which media does this document show, and where" — and is
// the ONLY answer to it: the hero rebuild primes its media from HeroMediaSlots (collectHeroMediaIds),
// and the usage scan (store/content/media_usage_json.go) reads all four. A walker mirrors what its
// renderer actually draws: a payload the renderer ignores for its block type holds nothing.
//
// The *MediaKeys lists name every JSON key under which a walker reads an id. The usage scan turns
// them into a SQL prefilter so that a document naming none of the requested ids never leaves the
// database; TestMediaJSONHolderKeysCoverWalkers keeps each list in step with its walker — a key the
// walker reads but the list lacks would filter the holder out and call its file free.

// MediaSlot is one media id a document cites and where inside the document it sits
// ("slideshow, block 3", "image, block 2 left 1", "colour map, front").
type MediaSlot struct {
	MediaId int
	Slot    string
}

// MediaSlotIds returns the distinct ids of slots, in first-seen order.
func MediaSlotIds(slots []MediaSlot) []int {
	seen := make(map[int]struct{}, len(slots))
	ids := make([]int, 0, len(slots))
	for _, s := range slots {
		if _, dup := seen[s.MediaId]; dup {
			continue
		}
		seen[s.MediaId] = struct{}{}
		ids = append(ids, s.MediaId)
	}
	return ids
}

// mediaSlotSet collects slots, skipping empty ids (0 is "no picture" in every one of these
// documents) and exact repeats (a block whose portrait and landscape are the same file is one use).
type mediaSlotSet struct {
	out  []MediaSlot
	seen map[MediaSlot]struct{}
}

func (s *mediaSlotSet) add(slot string, ids ...int) {
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		k := MediaSlot{MediaId: id, Slot: slot}
		if _, dup := s.seen[k]; dup {
			continue
		}
		if s.seen == nil {
			s.seen = make(map[MediaSlot]struct{})
		}
		s.seen[k] = struct{}{}
		s.out = append(s.out, k)
	}
}

func blockSlot(what string, i int) string { return fmt.Sprintf("%s, block %d", what, i+1) }

// HeroMediaKeys — every key HeroMediaSlots reads an id under.
var HeroMediaKeys = []string{"portrait_id", "landscape_id", "media_id", "poster_media_id"}

// HeroMediaSlots lists the media the stored hero shows, block by block, plus the two nav pictures.
// It switches on the block type exactly as the hero resolver does (store/content/hero.go), so a
// payload left behind under another type is not counted.
func HeroMediaSlots(h HeroFullInsert) []MediaSlot {
	var s mediaSlotSet
	media := func(slot string, m HeroMedia) { s.add(slot, m.PortraitId, m.LandscapeId) }
	singles := func(slot string, ss []HeroSingleInsert) {
		for i := range ss {
			media(slot, ss[i].Media)
		}
	}
	for i, e := range h.Entities {
		switch e.Type {
		case HeroTypeSingle:
			media(blockSlot("single", i), e.Single.Media)
		case HeroTypeDouble:
			media(blockSlot("double left", i), e.Double.Left.Media)
			media(blockSlot("double right", i), e.Double.Right.Media)
		case HeroTypeMain:
			media(blockSlot("main", i), e.Main.Media)
		case HeroTypeEmbed:
			media(blockSlot("embed fallback", i), e.Embed.Fallback)
		case HeroTypeDrop:
			media(blockSlot("drop", i), e.Drop.Media)
		case HeroTypeSlideshow:
			singles(blockSlot("slideshow", i), e.Slideshow.Slides)
		case HeroTypeMosaic:
			singles(blockSlot("mosaic", i), e.Mosaic.Tiles)
		case HeroTypeSplit:
			media(blockSlot("split", i), e.Split.Media.Media)
		case HeroTypeVideo:
			s.add(blockSlot("video", i), e.Video.MediaId)
			s.add(blockSlot("video poster", i), e.Video.PosterMediaId)
		case HeroTypeProductSpotlight:
			media(blockSlot("product spotlight", i), e.ProductSpotlight.Media)
		case HeroTypeNewsletter:
			media(blockSlot("newsletter", i), e.Newsletter.Media)
		case HeroTypeStatement:
			media(blockSlot("statement", i), e.Statement.Media)
		case HeroTypeLookbook:
			singles(blockSlot("lookbook", i), e.Lookbook.Frames)
		}
	}
	s.add("nav men", h.NavFeatured.Men.MediaId)
	s.add("nav women", h.NavFeatured.Women.MediaId)
	return s.out
}

// ArchiveBodyMediaKeys — every key ArchiveBodyMediaSlots reads an id under.
var ArchiveBodyMediaKeys = []string{"media_id", "media_ids"}

// ArchiveBodyMediaSlots lists the media an archive's timeline body shows. Like the timeline
// resolver (resolveArchiveItems) it reads only the payload its block type selects. The archive's
// thumbnail is a real column with a foreign key and is reported by the registry, not here.
func ArchiveBodyMediaSlots(items []ArchiveItemInsert) []MediaSlot {
	var s mediaSlotSet
	for i, it := range items {
		switch it.Type {
		case ArchiveItemTypeMainMedia:
			if it.MainMedia != nil {
				s.add(blockSlot("main media", i), it.MainMedia.MediaId)
			}
		case ArchiveItemTypeMediaLine:
			if it.MediaLine != nil {
				s.add(blockSlot("media line", i), it.MediaLine.MediaIds...)
			}
		case ArchiveItemTypeMediaWithCaption:
			if it.MediaWithCaption != nil {
				s.add(blockSlot("media with caption", i), it.MediaWithCaption.MediaId)
			}
		}
	}
	return s.out
}

// EmailBodyMediaKeys — every key EmailBodyMediaSlots reads an id under.
var EmailBodyMediaKeys = []string{"logo_media_id", "media_id"}

// EmailBodyMediaSlots lists the media an email campaign body shows, two-column children included
// ("image, block 3 left 1"). It reads every payload that is present, whatever the block type —
// the same rule the renderer's media preload follows (campaignrender collectMediaIDs) — because a
// sent email keeps showing its pictures from the bucket, so the generous reading is the safe one.
func EmailBodyMediaSlots(blocks []EmailBlock) []MediaSlot {
	var s mediaSlotSet
	var walk func(items []EmailBlock, where func(i int) string)
	walk = func(items []EmailBlock, where func(i int) string) {
		for i := range items {
			b := &items[i]
			at := where(i)
			if b.Header != nil {
				s.add("header logo, "+at, b.Header.LogoMediaID)
			}
			if b.ImageLink != nil {
				s.add("image, "+at, b.ImageLink.MediaID)
			}
			if b.VideoThumb != nil {
				s.add("video thumbnail, "+at, b.VideoThumb.MediaID)
			}
			if b.TwoColumn != nil {
				walk(b.TwoColumn.Left, func(j int) string { return fmt.Sprintf("%s left %d", at, j+1) })
				walk(b.TwoColumn.Right, func(j int) string { return fmt.Sprintf("%s right %d", at, j+1) })
			}
		}
	}
	walk(blocks, func(i int) string { return fmt.Sprintf("block %d", i+1) })
	return s.out
}

// ColourPlanMediaKeys — every key ColourPlanMediaSlots reads an id under.
var ColourPlanMediaKeys = []string{"media_id"}

// ColourPlanMediaSlots lists the painted maps of a card's colour plan ("colour map, front").
//
// The map's own picture (media_id) is the painting — its own upload, not a design picture, held by
// nothing else — so it is counted. base_media_id is deliberately NOT: it only records which flat
// the map was painted over, to tell a stale map; that flat is a design picture whose own row holds
// it, and a map whose flat is gone is already stale whether or not the file survives.
func ColourPlanMediaSlots(maps []DesignColourMap) []MediaSlot {
	var s mediaSlotSet
	for _, m := range maps {
		slot := "colour map"
		if v := strings.TrimSpace(m.View); v != "" {
			slot += ", " + v
		}
		s.add(slot, m.MediaId)
	}
	return s.out
}
