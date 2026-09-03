package designgen

import (
	"strconv"
	"strings"
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
func patternCraft(p patternParams) string {
	var b strings.Builder
	b.WriteString("repeating tile:\n" +
		"Produce ONE square tile that repeats seamlessly. The tile is the unit of a wallpaper-style " +
		"repeat: laid out in a grid it must join to itself invisibly, so the right edge must continue " +
		"exactly into the left edge and the bottom edge exactly into the top edge, with every motif " +
		"that crosses a boundary completed on the opposite side.\n" +
		"Fill the frame edge to edge. Strictly excluded: any border, frame, margin, matte, vignette, " +
		"drop shadow, fade or rounded corner; any signature, watermark, logo, caption or text; any " +
		"mock-up, garment, hanger, hand, surface or background the tile is shown ON — the output is " +
		"the cloth itself, flat and face-on, and nothing else.\n" +
		"Distribute the motif evenly across the whole square with no single focal object and no empty " +
		"quarter: a tile with a centre announces its own grid the moment it is repeated.\n" +
		"Light the tile flatly and evenly. Do not carry over the lighting of the source photograph — " +
		"its gradient, its hot spots and its cast shadows become visible stripes once the tile is " +
		"laid out.\n" +
		"The reference picture is not the tile and must not be copied as a picture. It may be a " +
		"photograph of real cloth — folded, crumpled, draped, hanging, seen at an angle, lit from " +
		"one side, cropped in the middle of the motif — or a sketch of a print. Reconstruct the " +
		"PRINT that cloth carries as it would look pressed flat and photographed face-on under even " +
		"light: straighten the motif, complete what the folds and the crop hide, keep its " +
		"proportions, its colours and the character of the material seen through it — the knit, the " +
		"weave, the grain — and remove every trace of the crumpling: the folds, the creases, the " +
		"highlights and the shadows they cast, the perspective. Take from the picture its motif, " +
		"its palette and its material, and nothing else: not its crop, not its perspective, not " +
		"its lighting, not its background.")

	if p.RepeatMM > 0 {
		// SCALE, NOT SIZE. See the doc comment: the pixels are a square either way; the millimetres
		// say how much garment that square covers, which is what decides how big the motif is drawn.
		b.WriteString("\nDraw the motif at the scale of a " + strconv.Itoa(p.RepeatMM) +
			" mm repeat on the finished garment: one whole tile covers " + strconv.Itoa(p.RepeatMM) +
			" mm of cloth in each direction.")
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
