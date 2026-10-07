package designgen

import (
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ─────────────────────────── HARDWARE ON A RENDER (ROUND 9 Ф1) ───────────────────────────
//
// A hardware slot (a button, a snap, a zip) painted on PARTS reaches a render as a fabric use of
// `kind: hardware`: its picture (a white shot of the item, or none — words only), its words, and a
// `parts` text that says where it sits and how many there are on each view («left front body · 2 on
// the front»). It carries NO map label: the client exports the colour map with the hardware pixels
// given the cloth around them, because a flooded hardware hex leaked its colour into the button
// (Ф0, run 197). Place and size come from the CLOTH MOCKUP of the view, where the client draws the
// slot's picture into each painted instance — Ф0 measured that the model copies the button from
// there (B4). So the paragraph points at the mockup image numbers, and the picture is captioned as
// the item on a white ground.

// statedHardware is the hardware uses of the recipe that say something — the twin of statedCloths,
// which leaves them out.
func statedHardware(c *colourRecipe) []fabricUse {
	if c == nil {
		return nil
	}
	var out []fabricUse
	for _, f := range c.Fabrics {
		if !clothIsHardware(f) {
			continue
		}
		if f.MediaID > 0 || oneLine(f.Name) != "" || oneLine(f.Words) != "" || oneLine(f.Parts) != "" {
			out = append(out, f)
		}
	}
	return out
}

// hardwareName is the slot name as the person wrote it, or the plain word.
func hardwareName(h fabricUse) string {
	if n := oneLine(h.Name); n != "" {
		return n
	}
	return "hardware"
}

// hardwareNouns — the word the caption calls the item by, the first whose stem the name or the words
// carry; «piece» when none does. Order matters: «zip puller» is a zip, «snap button» a snap.
var hardwareNouns = []struct{ stem, noun string }{
	{"zip", "zip"},
	{"snap", "snap"},
	{"press stud", "snap"},
	{"rivet", "rivet"},
	{"button", "button"},
	{"buckle", "buckle"},
	{"eyelet", "eyelet"},
	{"grommet", "grommet"},
	{"d-ring", "ring"},
	{"toggle", "toggle"},
	{"hook", "hook"},
	{"stud", "stud"},
}

func hardwareNoun(h fabricUse) string {
	w := strings.ToLower(oneLine(h.Name) + " " + oneLine(h.Words))
	for _, n := range hardwareNouns {
		if strings.Contains(w, n.stem) {
			return n.noun
		}
	}
	return "piece"
}

// hardwareCaption — the caption of the hardware picture in the reference list.
func hardwareCaption(h fabricUse) string {
	return "hardware «" + hardwareName(h) + "» — the " + hardwareNoun(h) + " itself on a white ground"
}

// hardwareNumberOf — the number of the picture that went out AS this hardware; 0 = not sent.
func hardwareNumberOf(attached []refCaption, mediaID int) int {
	if mediaID <= 0 {
		return 0
	}
	for i, rc := range attached {
		if rc.MediaID == mediaID {
			if !rc.IsHardware {
				return 0
			}
			return i + 1
		}
	}
	return 0
}

// hardwareMockups — the numbers of the cloth mockups that went out for the views this hardware's
// `parts` names («2 on the front» → the front's mockup). A `parts` that names no view at all (an
// older client's words) points at every mockup that went out; one that names views none of whose
// mockups went out points at none — the front's mockup does not show a button on the back.
func hardwareMockups(h fabricUse, maps []colourMap, attached []refCaption) []string {
	parts := strings.ToLower(oneLine(h.Parts))
	namesAView := false
	for _, v := range []string{entity.DesignViewFront, entity.DesignViewBack, entity.DesignViewSideL, entity.DesignViewSideR} {
		if strings.Contains(parts, "on the "+viewWord(v)) {
			namesAView = true
		}
	}
	var named, all []string
	for _, m := range colourMapsSent(maps, attached) {
		img := mockupNumberOf(attached, m.MockupMediaID)
		if img == 0 {
			continue
		}
		all = append(all, strconv.Itoa(img))
		if v := viewWord(m.View); v != "" && strings.Contains(parts, "on the "+v) {
			named = append(named, strconv.Itoa(img))
		}
	}
	if namesAView {
		return named
	}
	return all
}

// renderHardwareParagraphs — one HARDWARE paragraph per painted hardware use, after the cloths and
// the artworks. The picture (or the words, when none was sent) says WHAT; the mockup image(s) say
// WHERE and HOW BIG; `parts` says how many on which view. No hardware — nothing, so every run
// composed before this round composes the same prompt.
func renderHardwareParagraphs(uses []fabricUse, maps []colourMap, attached []refCaption) []string {
	var out []string
	for _, h := range uses {
		var b strings.Builder
		name := hardwareName(h)
		words := oneLine(h.Words)
		k := hardwareNumberOf(attached, h.MediaID)
		if k > 0 {
			b.WriteString("HARDWARE. Image " + strconv.Itoa(k) + " is the hardware «" + name + "»")
			if words != "" {
				b.WriteString(" (" + words + ")")
			}
			b.WriteString(".")
		} else {
			b.WriteString("HARDWARE. The hardware «" + name + "»")
			if words != "" {
				b.WriteString(" (" + words + ")")
			}
			b.WriteString(": no picture is given — build it from the words.")
		}
		parts := oneLine(h.Parts)
		switch mockups := hardwareMockups(h, maps, attached); {
		case len(mockups) == 1:
			b.WriteString(" The mockup image " + mockups[0] + " shows exactly where it sits and how big it is")
		case len(mockups) > 1:
			b.WriteString(" The mockup images " + joinWords(mockups) + " show exactly where it sits and how big it is")
		default:
			b.WriteString(" It sits on the garment")
		}
		if parts != "" {
			b.WriteString(": " + parts)
		}
		b.WriteString(".")
		if k > 0 {
			b.WriteString(" Reproduce exactly this piece — shape, colour, holes, finish — at those places and " +
				"that size, sewn on with its own shadow; its white ground is not part of it.")
		} else {
			b.WriteString(" Make it as those words say — shape, colour, holes, finish — at those places and " +
				"that size, sewn on with its own shadow.")
		}
		b.WriteString(" No other hardware of this kind anywhere else.")
		out = append(out, b.String())
	}
	return out
}
