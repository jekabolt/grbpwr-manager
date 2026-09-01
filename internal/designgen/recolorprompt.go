package designgen

import "strings"

// recolorCraft is the craft block of the RECOLOUR route (K-17): the paragraph that turns «here is a
// photograph and a colour» into «give me back this exact photograph with the garment in that
// colour».
//
// ═══ WHY THIS IS NOT THE RENDER CRAFT WITH A COLOUR IN IT ═══════════════════════════════════════
//
// The render craft's job is to COMPOSE a photograph of a garment that does not exist yet, from
// flats and a description of cloth. This one's job is the opposite: everything already exists and
// exactly one property may move. The two paragraphs contradict each other line by line — «build the
// scene» against «do not touch the scene» — which is why they are two blocks and why composePrompt
// writes exactly one of them.
//
// ⚠ THE LIST OF THINGS THAT MUST NOT CHANGE IS THE SUBSTANCE OF THE INSTRUCTION, NOT DECORATION. An
// image model given «make this jacket olive» and nothing else returns A JACKET, plausibly olive, in
// a plausible pose, on a plausible person — a new photograph that answers a question nobody asked.
// The customer-facing value of the whole ON MODEL section is that the frame is REAL: the model, the
// light and the fall of the cloth are photographed, not invented. So the exclusions are enumerated
// and they are enumerated concretely, because «keep everything else» is not a thing a model can
// act on and «the same person, the same pose, the same shadows» is.
//
// ⚠ AND THE FABRIC IS NAMED SEPARATELY FROM THE COLOUR. Recolouring by repainting pixels is what
// the naive instruction produces: a flat patch of colour where a garment used to be, with the
// weave, the sheen and the folds smoothed away. Saying that the WEAVE, the FOLDS and the SHADOWS
// must survive the change is what makes the difference between a recolour and a paint bucket — and
// it is the whole reason the owner chose generation over a filter.
//
// THE COLOUR ITSELF IS NOT REPEATED HERE. It is already stated above, by composePrompt, in the
// `colour` and `fabric in words` blocks — one writer of the human's colour, exactly as on the render
// route. Restating it in this paragraph would make two sentences that disagree the day either one is
// edited.
func recolorCraft(p runParams) string {
	var b strings.Builder
	b.WriteString("recolour, not re-photograph:\n" +
		"You are given a real photograph of a garment worn by a real person. Return THAT SAME " +
		"PHOTOGRAPH with the garment recoloured to the colour stated above, and change nothing else.\n" +
		"Keep exactly as they are: the person, their face, their skin, their hair and their hands; " +
		"the pose and the framing; the background and the floor; the lighting, its direction and its " +
		"colour temperature; every other garment, shoe and accessory in the frame; the image's " +
		"resolution, crop and aspect ratio.\n" +
		"Keep the garment itself in every respect except its colour: the same cut, the same seams, " +
		"topstitching, pockets, zips, buttons and labels, in the same places and at the same size.\n" +
		"Carry the material through the change instead of painting over it. The weave and the surface " +
		"texture must still read at the same scale; the folds, creases and drape must fall exactly " +
		"where they fall now; the highlights and the shadows on the cloth must keep their shape and " +
		"their strength, re-tinted to the new colour rather than flattened out. The result must look " +
		"like the same garment cut from cloth dyed differently, never like a colour laid over a " +
		"photograph.\n" +
		"Strictly excluded: a different person, a different pose, a different background, a different " +
		"crop, added or removed items, retouched skin, a smoothed or repainted surface, added logos or " +
		"text, a colour that spills onto skin, hair or background.")

	// THE PRINT IS ITS OWN SENTENCE, AND ONLY WHEN THERE IS ONE. A garment carrying a pattern has a
	// second thing that could be destroyed by a naive recolour, and it needs saying — but saying it
	// on a plain garment invites a model to invent a print that was never there.
	if hasPatternCloth(p) {
		b.WriteString("\nThe garment carries a print or a woven pattern. Keep the pattern's motif, its " +
			"scale and its placement on the body exactly as they are; recolour it, do not redraw it.")
	}
	return b.String()
}

// hasPatternCloth reports whether the run's own colour recipe states a cloth with a repeat — the
// only evidence, inside the frozen snapshot, that this garment is patterned rather than plain.
//
// READ FROM THE RECIPE AND NEVER GUESSED FROM THE PICTURE. The pass has the picture as a url and
// never as pixels; a guess would have to be the model's, and asking the model whether there is a
// pattern before telling it to preserve one is how an invented print gets authorised.
func hasPatternCloth(p runParams) bool {
	if p.Colour == nil {
		return false
	}
	for _, c := range p.Colour.Fabrics {
		if c.RepeatMM > 0 {
			return true
		}
	}
	return false
}
