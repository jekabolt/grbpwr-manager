package designgen

import (
	"strconv"
	"strings"
)

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
	// ⚠ TWO CRAFTS, CHOSEN BY WHETHER A CLOTH WITH A PICTURE TRAVELS (J-31). They are not variants
	// of one paragraph: one says «the same garment cut from cloth dyed differently», the other says
	// «the same garment made of a DIFFERENT cloth», and the sentence about keeping the print is
	// true in the first and flatly wrong in the second — the whole point of the second is that the
	// print changes. A run that took both would end on whichever happens to be written last.
	//
	// THE PREDICATE IS THE PICTURE, NOT THE NAME AND NOT THE REPEAT, because the picture is what
	// physically reaches the call: clothPictures selects on `media_id > 0` and imageCalls appends
	// exactly what it selected. A craft chosen on a cloth's NAME would tell the model to lay out an
	// image nobody attached.
	if cloths := clothsWithTexture(p); len(cloths) > 0 {
		return reclothCraft(cloths)
	}
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

// reclothCraft is the craft block of a recolour that was ALSO GIVEN A CLOTH (J-31): the owner's
// «выбрать и или паттерн/цвет и результатом должен быть уже то что там вещь поменяла цвет ткань».
//
// ═══ WHY IT IS NOT recolorCraft WITH ONE SENTENCE ADDED ═════════════════════════════════════════
//
// Half of recolorCraft is an instruction to PRESERVE the material: «carry the material through the
// change», «the weave and the surface texture must still read at the same scale», and — when the
// garment is patterned — «keep the pattern's motif … recolour it, do not redraw it». Every one of
// those sentences is the OPPOSITE of what this run asks for. Told to keep the weave and to lay a
// new cloth in the same breath, a model resolves the contradiction by doing neither properly: it
// tints the existing surface and calls that the new cloth.
//
// ⚠ AND EVERYTHING THAT MUST NOT MOVE STILL MUST NOT MOVE. The list of exclusions is the substance
// of the ON MODEL route, not decoration — the value of the whole section is that the frame is REAL
// — so it is repeated here in full rather than assumed. What changes is exactly one thing: what
// the garment is made of.
//
// ⚠ THE GEOMETRY OF THE CLOTH IS SPELLED OUT SEPARATELY FROM THE FACT OF IT. «Make it out of this
// cloth» produces a flat sticker of the tile pasted over the garment; what makes it read as cloth
// is that the weave and the print FOLLOW THE FOLDS that are already in the photograph, and that
// the highlights and shadows keep their shape on the new surface. That is the same argument
// recolorCraft makes about a colour, applied one level up.
//
// THE COLOUR, WHEN ONE IS ALSO STATED, RE-TINTS THE CLOTH RATHER THAN COMPETING WITH IT. That is
// the render route's own order of authority (renderprompt: the photograph governs the material, the
// picked colour governs the colour), said here in one sentence so the two routes cannot drift into
// two different answers to «a pattern AND a colour».
func reclothCraft(cloths []fabricUse) string {
	var b strings.Builder
	b.WriteString("re-cloth, not re-photograph:\n" +
		"You are given a real photograph of a garment worn by a real person (image 1) and a " +
		"photograph of a cloth (image 2). Return THAT SAME PHOTOGRAPH with the garment made of the " +
		"cloth in image 2, and change nothing else.\n" +
		"Keep exactly as they are: the person, their face, their skin, their hair and their hands; " +
		"the pose and the framing; the background and the floor; the lighting, its direction and its " +
		"colour temperature; every other garment, shoe and accessory in the frame; the image's " +
		"resolution, crop and aspect ratio.\n" +
		"Keep the garment's cut, seams, topstitching, pockets, zips, buttons and labels in the same " +
		"places and at the same size.\n" +
		"Lay the cloth ON the garment: its weave, surface and print must follow the folds, creases " +
		"and drape exactly where they fall now, with the highlights and the shadows keeping their " +
		"shape and their strength on the new cloth. The result must look like the same garment cut " +
		"from a different cloth, never like a picture of cloth pasted over a photograph.")

	// THE REPEAT IN MILLIMETRES, IN THE SAME WORDS renderClothLine USES FOR IT, so one repeat is
	// one sentence across the band. Said only when the run states one: a number nobody stated
	// would be a scale we invented.
	for _, c := range cloths {
		if c.RepeatMM > 0 {
			b.WriteString(" Its pattern repeats every " + strconv.Itoa(c.RepeatMM) +
				" mm on the finished garment.")
			break
		}
	}

	b.WriteString("\nThe colour stated above, if any, governs the colour of the cloth: re-tint the " +
		"cloth of image 2 to it and keep its motif, its weave and its scale.\n" +
		"Strictly excluded: a different person, a different pose, a different background, a " +
		"different crop; cloth laid over the skin, the hair or the background; a flat printed " +
		"sticker instead of woven or knitted cloth; a smoothed or repainted surface; added or " +
		"removed items; retouched skin; added logos or text.")
	return b.String()
}

// clothsWithTexture — ткани рецепта, которые ФИЗИЧЕСКИ уезжают в вызов: у них есть картинка.
//
// ОДИН ВОПРОС, ОДНО МЕСТО. Тот же отбор делает clothPictures (по списку уже собранных ссылок) и
// зеркалит дверь (designAnyClothWithPicture, по сообщению клиента). Здесь он нужен третьей
// стороне — ремеслу, — и написан через statedCloths, чтобы полупустая строка «add cloth», которую
// клиент оставил и бросил, не переключала род ремесла целиком.
func clothsWithTexture(p runParams) []fabricUse {
	out := make([]fabricUse, 0, 2)
	for _, c := range statedCloths(p.Colour) {
		if c.MediaID > 0 {
			out = append(out, c)
		}
	}
	return out
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
