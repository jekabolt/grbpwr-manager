package designgen

import (
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// patternCraft is the craft block of the PATTERN route (K-13): the paragraph that turns «here is a
// picture» into «a tile that joins to itself».
//
// ═══ THE ONE WORD THAT DECIDES EVERYTHING IS «REPEATING» ════════════════════════════════════════
//
// The owner's ask is «заапдоудить картинку и через gpt image 2 сделать из неё повторяемый паттерн».
// A pretty square derived from a swatch is worthless: the whole value is that it can be laid out
// edge to edge, on cloth or in a mock-up, without a visible join. So the instruction is written
// around the WRAP and says it four ways, because an image model asked politely for «a seamless
// pattern» returns a picture that merely looks patterned:
//
//   - what must be true at the boundary (the right edge continues into the left, the bottom into
//     the top);
//   - what that forbids at the boundary (a border, a frame, a vignette, a drop shadow, a fade);
//   - what must be true of the interior (an even field, no single focal object, nothing that draws
//     the eye to the centre and thereby announces the grid when tiled);
//   - what the source picture is FOR (its motif, palette and material), as opposed to what it is
//     not (its crop, its lighting, its background).
//
// ⚠ THE SOURCE IS A PHOTOGRAPH OF REAL CLOTH, NOT A TILE, AND THAT SENTENCE IS THE WHOLE ROUTE.
// The owner's own description of the input (J-12): «я могу дать картинку на которой может быть
// какая-то ткань например помятая или что-то еще с ней она не как прямо паттерн нам надо через аи
// ее превратить в реальный паттерн». So the instruction does not merely forbid copying the crop —
// it says what to DO with a crumpled, draped, angled, half-lit photograph: RECONSTRUCT the print
// that cloth carries as it would look pressed flat and photographed face-on. Told only «take its
// motif», a model returns the creases with the motif, and the creases tile into a grid of creases.
//
// ⚠ THE SOURCE'S LIGHTING IS EXCLUDED DELIBERATELY, and it is the most common way a tile fails
// while looking correct. A photographed swatch is lit from one side: it is brighter at the top and
// darker at the bottom. Tiled, that gradient becomes horizontal stripes across the whole cloth —
// each of them a seam, none of them at the edge where anybody thought to look.
//
// THE REPEAT IN MILLIMETRES IS SAID WHEN IT IS KNOWN, and it is said as SCALE, not as size. The
// tile is a square of pixels; the millimetres tell the model how much garment one tile covers, and
// therefore how large the motif must be drawn inside it. That number is the same one V-7 already
// puts on a pattern asset, so a tile generated at 120 mm and an asset placed at 120 mm are the same
// claim about the same cloth.
//
// ⚠ AND ZERO IS NO LONGER SILENCE. It used to say nothing at all, which was the honest reading of
// «nobody has decided the scale» while a screen still asked for the number. The owner removed that
// screen — «SCALE этот мне сейчас вообще не кажется нужным в MAKE A TILE он только путает» — so
// zero is now the ORDINARY case and the question it answered still has to be answered by somebody.
// It is answered by the model, out of the source: the density is a fact the photograph already
// carries, and «take it from there» is an instruction, where saying nothing is not. Both error
// directions are named, because «choose a sensible repeat» is what produces one motif blown up to
// fill the square.
//
// ═══ SWATCH MODE (STEP 3): THE SAME TILE, A DIFFERENT SOURCE ════════════════════════════════════
//
// With params.pattern.mode = "swatch" there is no photograph to reconstruct: the cloth is built
// from the STATED colour and the cloth words, and the picture — zero or one of them — is a TEXTURE
// reference. The wrap and the exclusions are shared word for word, because a swatch is laid out on
// the render exactly as a tile is; the even field, the light and the source paragraph are replaced
// by swatchCraft, and the repeat falls back to «natural scale» instead of «read it off the
// picture». `pictures` is how many pictures actually attached; the image mode ignores it, and its
// text is byte-identical to what every frozen prompt before this mode was composed from.
func patternCraft(p patternParams, pictures int) string {
	// ФУРНИТУРА — НЕ ПЛИТКА. Ни стыка, ни раппорта, ни «заполни кадр от края до края»: это
	// предметный снимок одной вещи на белом, и общая половина ремесла плитки ему противоречит.
	// БИРКА — ТОЖЕ ПРЕДМЕТНЫЙ СНИМОК, но её картинка — логотип, который нужно воспроизвести, а
	// hardwareCraft велит у референсов цвет не брать и исключает «любой логотип» — ровно обратное.
	if p.Mode == entity.DesignPatternModeLabel {
		return labelCraft(pictures)
	}
	if p.Mode == entity.DesignPatternModeHardware {
		return hardwareCraft(pictures)
	}
	var b strings.Builder
	b.WriteString(patternWrapParagraph + patternExclusionParagraph)
	if p.Mode == entity.DesignPatternModeSwatch {
		b.WriteString(swatchCraft(pictures))
	} else {
		b.WriteString(patternEvenFieldParagraph + patternLightParagraph + patternSourceParagraph)
	}

	if p.RepeatMM > 0 {
		// SCALE, NOT SIZE. See the doc comment: the pixels are a square either way; the millimetres
		// say how much garment that square covers, which is what decides how big the motif is drawn.
		b.WriteString("\nDraw the motif at the scale of a " + strconv.Itoa(p.RepeatMM) +
			" mm repeat on the finished garment: one whole tile covers " + strconv.Itoa(p.RepeatMM) +
			" mm of cloth in each direction.")
	} else if p.Mode == entity.DesignPatternModeSwatch {
		// A SWATCH HAS NO SOURCE TO READ THE DENSITY FROM, so «take it from the picture» would point
		// at nothing. The scale it needs is the scale of the cloth itself: its yarn and weave at the
		// size they have on a real garment.
		b.WriteString("\nShow the cloth at natural scale: one tile covers a hand-sized piece of the " +
			"real fabric, so the weave or knit reads at the size it has on a finished garment — " +
			"neither magnified into a close-up of single yarns nor shrunk into a flat, featureless " +
			"fill.")
	} else {
		// THE DEFAULT SINCE ROUND 15. The number is gone from the screen; the question it answered
		// is handed to the model together with the place to read the answer from.
		b.WriteString("\nChoose the size of the repeat yourself, from the cloth in the picture: one " +
			"tile holds one natural, complete unit of the motif at the density the source shows — " +
			"neither a single element blown up to fill the square nor a field so fine that the motif " +
			"dissolves into texture. If the source shows the motif at a clear size against the " +
			"cloth, keep that size.")
	}
	return b.String()
}

// The paragraphs of the tile craft, as constants so that the two modes share the WRAP and the
// EXCLUSIONS word for word and the image mode stays byte-identical to what every frozen prompt of
// history was composed from.
const (
	patternWrapParagraph = "repeating tile:\n" +
		"Produce ONE square tile that repeats seamlessly. The tile is the unit of a wallpaper-style " +
		"repeat: laid out in a grid it must join to itself invisibly, so the right edge must continue " +
		"exactly into the left edge and the bottom edge exactly into the top edge, with every motif " +
		"that crosses a boundary completed on the opposite side.\n"
	patternExclusionParagraph = "Fill the frame edge to edge. Strictly excluded: any border, frame, margin, matte, vignette, " +
		"drop shadow, fade or rounded corner; any signature, watermark, logo, caption or text; any " +
		"mock-up, garment, hanger, hand, surface or background the tile is shown ON — the output is " +
		"the cloth itself, flat and face-on, and nothing else.\n"
	patternEvenFieldParagraph = "Distribute the motif evenly across the whole square with no single focal object and no empty " +
		"quarter: a tile with a centre announces its own grid the moment it is repeated.\n"
	patternLightParagraph = "Light the tile flatly and evenly. Do not carry over the lighting of the source photograph — " +
		"its gradient, its hot spots and its cast shadows become visible stripes once the tile is " +
		"laid out.\n"
	patternSourceParagraph = "The reference picture is not the tile and must not be copied as a picture. It may be a " +
		"photograph of real cloth — folded, crumpled, draped, hanging, seen at an angle, lit from " +
		"one side, cropped in the middle of the motif — or a sketch of a print. Reconstruct the " +
		"PRINT that cloth carries as it would look pressed flat and photographed face-on under even " +
		"light: straighten the motif, complete what the folds and the crop hide, keep its " +
		"proportions, its colours and the character of the material seen through it — the knit, the " +
		"weave, the grain — and remove every trace of the crumpling: the folds, the creases, the " +
		"highlights and the shadows they cast, the perspective. Take from the picture its motif, " +
		"its palette and its material, and nothing else: not its crop, not its perspective, not " +
		"its lighting, not its background."
)

// swatchCraft is the second half of the tile craft in SWATCH MODE (STEP 3): the cloth is built
// from the STATED COLOUR and the cloth words, not reconstructed from a photograph.
//
// ⚠ THE COLOUR IS THE ONE THING IT MUST NOT INTERPRET. The client sends the Pantone's screen hex
// in the `colour` block and the Pantone's name and code with the cloth words in `fabric in words`;
// a model left to «pick a nice red» returns a red, and a swatch of the wrong red is worse than no
// swatch — it goes into the render as the fabric of that colourway. So the value is to be matched,
// across the whole square, and the words say so.
//
// ⚠ A TEXTURE REFERENCE CONTRIBUTES THE MATERIAL AND NOTHING ELSE. It is a photograph of some
// cloth in some colour under some light; everything about it except how the cloth is built is a
// contradiction of the stated colour or of the flat light, and the paragraph names each of them.
//
// ⚠ NO MOTIF UNLESS ONE IS NAMED. The tile craft's wrap and exclusions talk about motifs because
// the image mode reconstructs a print; a plain cotton twill asked for «seamlessly» must not come
// back printed, so the absence is stated rather than left for the model to infer.
//
// `pictures` is the count that ACTUALLY attached (composePrompt reads it off `attached`), so the
// paragraph describes the call that happens rather than the one that was frozen.
func swatchCraft(pictures int) string {
	var b strings.Builder
	b.WriteString("Keep the surface even across the whole square: the same weave, the same density and " +
		"the same tone everywhere, no single focal spot and no darker or brighter quarter — a tile " +
		"with a centre announces its own grid the moment it is repeated.\n")
	b.WriteString("Light the tile flatly and evenly: no gradient, no hot spot, no cast shadow — each of " +
		"them becomes a visible stripe once the tile is laid out.\n")
	b.WriteString("This tile is a FABRIC SWATCH built from the colour and the cloth stated above, not " +
		"from a photograph. The stated colour is the colour of the cloth itself: match the stated " +
		"colour value exactly across the whole tile — not a tint of it, not a lighting effect on it, " +
		"and not drifting lighter, darker, warmer or cooler anywhere in the square. Build the cloth " +
		"the words describe: its material, its composition and its weight.")
	if pictures > 0 {
		b.WriteString(" The attached picture is a TEXTURE REFERENCE only: take from it the material " +
			"— the weave or the knit, the grain, the yarn, the surface and how it catches light — and " +
			"NOTHING of its colour, its lighting, its crop, its perspective or its background. Rebuild " +
			"that material flat and face-on, in the stated colour.")
	} else {
		b.WriteString(" No picture is attached: render plain cloth of that material in that colour, " +
			"with a believable weave or knit at natural scale — the texture of real fabric seen " +
			"face-on, not a flat digital fill.")
	}
	b.WriteString(" Draw no motif, print, stripe or check unless the cloth words above name one.")
	return b.String()
}

// hardwareCraft is the whole craft of HARDWARE MODE: a clean product photograph of ONE trim item —
// a button, a zipper, a buckle, a trim, a label — for one (colourway, BOM line) pair of the bench.
// It is not a tile: there is no wrap, no repeat and no edge-to-edge fill, so none of the tile
// paragraphs are shared.
//
// ⚠ WHAT THE ITEM IS COMES FROM THE WORDS ABOVE. The client puts the BOM line's material in the
// `colour` words and the ask («button · 4-hole horn 20L»); the server does not read the BOM, so the
// paragraph points at those words instead of naming an item itself.
//
// ⚠ THE STATED COLOUR, WHEN THERE IS ONE, IS MATCHED; WHEN THERE IS NONE, THE WORDS DECIDE IT. A
// horn button or a brass zip carries its colour in its material, so the colour is optional here.
//
// ⚠ REFERENCES SHOW SHAPE AND MATERIAL, NOT NECESSARILY THE COLOUR — the same separation swatchCraft
// makes for a texture reference, for the same reason: a photograph of the right button in the
// wrong colour is the ordinary case.
//
// `pictures` is the count that ACTUALLY attached (composePrompt reads it off `attached`).
func hardwareCraft(pictures int) string {
	var b strings.Builder
	b.WriteString("hardware item:\n" +
		"Produce ONE square product photograph of a single garment trim item — the button, zipper, " +
		"buckle, snap, trim or label described in the words above. Show exactly one of it, whole and " +
		"in sharp focus, centered on a plain, seamless pure white background, with generous empty " +
		"space around it.\n")
	b.WriteString("Light it with soft, even studio light: no hard cast shadow, no hot spot, no " +
		"coloured reflection; at most a faint soft contact shadow directly beneath the item.\n")
	b.WriteString("Strictly excluded: any garment, cloth or fabric the item is attached to; any hand, " +
		"body part, mannequin or packaging; any props, surface texture, scenery or second item; any " +
		"text, caption, watermark, logo that the words do not name, measurement, ruler or frame.\n")
	b.WriteString("Build the item the words describe: its type, its shape, its size, its material and " +
		"its finish — the grain of horn or wood, the lustre of metal, the sheen of plastic or resin, " +
		"the weave of a tape or a woven label. If a colour is stated above, the item is that colour: " +
		"match the stated colour value exactly, not a tint of it and not a lighting effect on it. If no " +
		"colour is stated, give the item the natural colour of the material the words name.")
	if pictures > 0 {
		b.WriteString(" The attached pictures are REFERENCES of the item's shape, construction and " +
			"material: follow them for the form — the hole count, the teeth, the puller, the edge, the " +
			"proportions — but NOT necessarily for the colour, and take nothing of their background, " +
			"lighting, crop or surroundings. Where a reference and the stated colour disagree, the " +
			"stated colour wins.")
	} else {
		b.WriteString(" No picture is attached: build the item from the words alone, as a real, " +
			"manufactured trim item would look.")
	}
	return b.String()
}

// labelCraft — mode label: ONE product photograph of ONE garment label, flat, front view. Its
// 0..1 attached picture is the brand's LOGO ARTWORK, reproduced exactly — the opposite of
// hardwareCraft, whose references give shape only and whose exclusions drop any logo. «sewn at …»
// in the words is context (where the label sits on the garment), never something to draw.
func labelCraft(pictures int) string {
	var b strings.Builder
	b.WriteString("garment label:\n" +
		"Produce ONE square product photograph of a single garment LABEL as described in the words " +
		"above — shown alone, lying flat, in front view, whole and in sharp focus, centered on a plain, " +
		"seamless pure white background, with generous empty space around it.\n")
	b.WriteString("Light it with soft, even studio light: no hard cast shadow, no hot spot, no " +
		"coloured reflection; at most a faint soft contact shadow directly beneath the item.\n")
	b.WriteString("Strictly excluded: any garment, cloth or fabric the item is attached to; any hand, " +
		"body part, mannequin or packaging; any props, surface texture, scenery or second item; any " +
		"text, caption or watermark that the words do not name, measurement, ruler or frame.\n")
	b.WriteString("Build the label the words describe: its construction — woven, printed, satin, " +
		"leather patch, rubber, embroidered or whatever the words name — with that technique's real " +
		"surface: the woven threads of a woven label, the ink of a print, the debossing of leather, " +
		"the moulded relief of rubber, the stitches of embroidery. Keep the proportions of its stated " +
		"size; if the words name a fold, show the finished folded label. If a colour is stated above, " +
		"the label ground is that colour: match the stated colour value exactly, not a tint of it and " +
		"not a lighting effect on it. If no colour is stated, give the ground the natural colour of " +
		"its construction.\n")
	b.WriteString("Where the words say «sewn at …», that names where the label sits on the garment — " +
		"CONTEXT ONLY: never draw the garment, the seam or any stitching into cloth; the picture is " +
		"the label alone.")
	if pictures > 0 {
		b.WriteString(" The attached picture is the brand's LOGO ARTWORK: reproduce the mark exactly — " +
			"its letterforms, proportions and spacing — as the label's artwork, executed in the label's " +
			"technique (woven, printed, embossed, embroidered…). Add, drop, restyle or redraw nothing, " +
			"take nothing of the picture's background, and render no text the words do not name.")
	} else {
		b.WriteString(" No logo is given: make a blank label carrying only what the words name — " +
			"invent no wordmark, no monogram, no text.")
	}
	return b.String()
}
