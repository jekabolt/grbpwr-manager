package techcardarchive

// Reason is the CLOSED half of a hole: one code, one meaning, and no code that does not live in
// this file. The open half is the free-text detail beside it, which carries no contract.
//
// The dictionary is closed on purpose. A hole is read twice — once by the export that could not
// carry something, once by the import that could not place it — and both readers, plus the report
// action text the operator is shown, have to agree on what happened. A code invented at a call
// site agrees with nobody. Adding one is a format change: the code, the line of explanation below,
// its row in FORMAT.md §7 and its action text in the report belong in the same commit.
type Reason string

const (
	// ReasonMaterialNotFound — no article in the target catalogue matches the passport.
	ReasonMaterialNotFound Reason = "material_not_found"
	// ReasonMaterialAmbiguous — several live articles carry that code, so none is picked.
	ReasonMaterialAmbiguous Reason = "material_ambiguous"
	// ReasonMaterialUnitMismatch — the code matched but the unit differs; not the same article.
	ReasonMaterialUnitMismatch Reason = "material_unit_mismatch"

	// ReasonMediaMissing — the card references a media slot the archive carries no file for.
	ReasonMediaMissing Reason = "media_missing"
	// ReasonMediaObjectMissing — EXPORT side: the source bucket would not give up the object, so
	// the archive carries no bytes for that slot at all.
	ReasonMediaObjectMissing Reason = "media_object_missing"
	// ReasonMediaUploadFailed — IMPORT side: the bytes WERE in the archive and the target bucket
	// refused them, so the slot is cleared and the card imports without the picture.
	//
	// A second code rather than one widened to «either side could not move the bytes», because a
	// report line has no field saying which side it came from (TechCardImportReportLine is
	// entity/ref/status/reason/detail/action) and the import re-reports the manifest's export
	// holes in the SAME list. The two also send the operator to different places: nothing on the
	// target closes media_object_missing — the bytes never travelled — while this one is closed by
	// retrying the import. One code with two action texts is exactly the drift the closed
	// dictionary exists to prevent.
	ReasonMediaUploadFailed Reason = "media_upload_failed"
	// ReasonMediaVanished — IMPORT side, and a RACE rather than a failure: the picture's bytes
	// matched a media row this base ALREADY held, so nothing was uploaded — and that row was
	// deleted out from under the import before it committed (in practice by another import being
	// rolled back). The slot is cleared and the card lands without the picture.
	//
	// Its own code and not media_upload_failed, whose ACTION is right here («import the archive
	// again») and whose PROSE is false: nothing about this instance's storage refused anything, and
	// a sentence ending «if it keeps failing, the bucket needs looking at» sends the operator to
	// inspect a bucket that is working perfectly. Not media_missing either — the archive DID carry
	// the file, and «the archive has no file for this slot» would send them to re-export a source
	// card that exported fine. The wrong dictionary entry does not merely read oddly: it decides
	// which of three places a person spends an afternoon in.
	ReasonMediaVanished Reason = "media_vanished"

	// ReasonPatternInvalid — the pattern file is unreadable or is not a DXF/PDF.
	ReasonPatternInvalid Reason = "pattern_invalid"

	// ReasonSizeUnknown — the size name is not in the target size dictionary.
	ReasonSizeUnknown Reason = "size_unknown"
	// ReasonSizeNotInCardRange — the size RESOLVED (it is in this base's dictionary) and the
	// IMPORTED CARD does not make it, so rows filed under it were dropped.
	//
	// Its own code and not size_unknown, for the same reason measurement_unknown is not size_unknown:
	// the action text is an instruction to a human, and size_unknown's sends them to the size
	// DICTIONARY, which in this case is in perfect order. The two are told apart by WHO refused —
	// the dictionary lookup happens in the resolver (before anything is written) and this one only
	// inside the transaction, against the imported card's own range — and an operator sent to fix
	// the wrong one of the two fixes nothing and concludes the report is noise.
	ReasonSizeNotInCardRange Reason = "size_not_in_card_range"
	// ReasonMeasurementUnknown — the measurement name is not in the target dictionary: the row is
	// dropped and the chart imports without it. Its own code and not size_unknown, because
	// sizechart.json carries TWO name axes (§5.1) and an operator told "size unknown" about a
	// measurement would look for the wrong dictionary.
	ReasonMeasurementUnknown Reason = "measurement_unknown"
	// ReasonWorkTokenUnknown — the operation's work token is not in the target work catalogue.
	ReasonWorkTokenUnknown Reason = "work_token_unknown"
	// ReasonCategoryUnknown — the category path does not resolve; the card lands without one.
	ReasonCategoryUnknown Reason = "category_unknown"
	// ReasonAssemblyComponentNotFound — the assembly component's style number is not in the base.
	ReasonAssemblyComponentNotFound Reason = "assembly_component_not_found"

	// ReasonColorwaysNotApplied — colourways travelled as reference and were not created.
	ReasonColorwaysNotApplied Reason = "colorways_not_applied"
	// ReasonColorwayExists — the card ALREADY carries a colourway of that colour, so the archive's
	// one was not created and its recipe was not written over the standing one.
	//
	// This is what makes the «create colourways from archive» button idempotent, and it is a
	// DEGRADATION rather than a skip on purpose: the colour IS on the card, so telling the operator
	// «the row is not there» would send them to create a duplicate the UNIQUE(style_id, color_code)
	// would refuse anyway. What is missing is only THIS archive's recipe for it — which nobody may
	// silently write over a colourway somebody has already been working on.
	ReasonColorwayExists Reason = "colorway_exists"
	// ReasonColorwayNotCreated — the draft colourway could not be created in this base at all, so
	// neither it nor its recipe landed. The commonest cause is a colour code that is not in THIS
	// base's colour dictionary: color_code is a dictionary FK, and the archive's codes are the
	// source's.
	//
	// Its own code and not archive_row_invalid, whose contract is «the row was already broken when
	// it was written»: nothing is wrong with the archive here, this base is simply missing the
	// colour. It is closed HERE — add the colour, press the button again — which is why it must not
	// borrow a sentence that sends the operator to the source card.
	ReasonColorwayNotCreated Reason = "colorway_not_created"
	// ReasonColorwayPinLost — the recipe row's material PIN could not be re-resolved, so the row
	// landed with its norm and its placement and with no article pinned (it therefore takes the BOM
	// slot's own article).
	//
	// A pin resolves through materials/index.json (§5.4) — the recipe carries only the source's
	// material_id — and that index lives in the ARCHIVE, while this action runs off
	// tech_card_import.colorways_payload, which is all that outlives the archive object. Once the
	// object has aged out of the bucket there is nothing left to match a pin against, and that is
	// what this code says.
	//
	// Its own code and NOT material_not_found: that one means «this catalogue has no such article»
	// and sends the operator to create it. Here the catalogue may hold the article perfectly well —
	// what was lost is the description of WHICH article the source meant. Sending somebody to
	// create an article they already have is exactly the wrong afternoon.
	ReasonColorwayPinLost Reason = "colorway_pin_lost"
	// ReasonCompositionNotDerived — the structured fibre breakdown travelled and was not written.
	//
	// It is a PROJECTION of style_composition, a table whose only writer re-derives it from the
	// card's own shell-fabric lines against THIS catalogue's articles, on every save of the card.
	// Writing the archive's rows would therefore state a breakdown of somebody else's catalogue as
	// a fact about this base's BOM, and the imported card's first save would replace it without
	// saying so — a reported loss traded for an unreported one. The legacy free-text `composition`
	// travels and IS written, so the card is not silent about what it is made of.
	ReasonCompositionNotDerived Reason = "composition_not_derived"
	// ReasonWastageClaimDegraded — a wastage/consumption claim could not be confirmed against THIS
	// base's own cut lays, so the figure imports as entered by hand.
	//
	// The badge is not a label that travels: «медиана по N раскроям» is an ASSERTION that the
	// number IS this server's current median, and the server that stores it is the one that has to
	// stand behind it. So the import re-runs the same check the save path runs (verifyBomWastageClaims)
	// against the lays measured HERE — a restore into the base the card came from re-earns the badge,
	// an import into a base that never cut that article does not — and this code is what says which
	// of the two happened. Without it the badge simply became 'manual' with nothing written down,
	// which reads on the fabric tab as a number somebody typed.
	ReasonWastageClaimDegraded Reason = "wastage_claim_degraded"
	// ReasonNormMarkerLost — the norm's marker stamp could not be re-sewn: the norm stands, the
	// stamp does not.
	ReasonNormMarkerLost Reason = "norm_marker_lost"

	// ReasonStyleNumberTaken — the style number already exists in the target base.
	ReasonStyleNumberTaken Reason = "style_number_taken"
	// ReasonUnknownEntry — the archive holds a file this server does not know (a newer MINOR).
	ReasonUnknownEntry Reason = "unknown_entry"
	// ReasonArchiveRowInvalid — the archive's own row is not a usable row: it names nothing (a size
	// chart cell with no size, a measured area with no cut piece), or it carries a value that is
	// not one (a negative area, a date no column can hold). The row is dropped and everything
	// around it imports.
	//
	// ONE code across entities rather than one per entity, and that is the whole point of the
	// split this file draws: the ENTITY says what it happened to, the reason says why. Every other
	// code here means «this side is missing a reference» and is closed by adding that reference
	// HERE; this one means the row was already broken when it was written, and no dictionary entry
	// on this side closes it. A code per offending shape would be a dozen action texts saying the
	// same sentence, which is how a closed dictionary stops being read.
	ReasonArchiveRowInvalid Reason = "archive_row_invalid"

	// ReasonCardNotImportable — EXPORT side, and the only code here that is about the WHOLE card
	// rather than a row: the card as it stands in this base breaks a rule the import's converter
	// enforces, so an import would refuse this archive entirely.
	//
	// It exists because THE STORE IS SOFTER THAN THE CONVERTER. Every write that goes through the
	// API passes dto.ConvertPbTechCardInsertToEntity; a write that goes straight to the store —
	// a seeder, a migration backfill, a future importer, a hand-written repair — passes nothing,
	// and the store documents that it relies on the converter having run (see the «сторожа
	// счётной секции здесь нет» note in store/techcard/production.go). Such a card exports
	// perfectly and is refused whole on the far side.
	//
	// A HOLE AND NOT AN EXPORT FAILURE. The archive is still worth having: it opens, it can be
	// read, its sidecars and files are all there, and the operator may be exporting it to look at
	// it rather than to move it. What was missing was not the file but the SENTENCE — the refusal
	// used to happen weeks later, in another base, worded as a field violation about a payload
	// nobody there had written.
	//
	// Its own code and not archive_row_invalid, whose whole contract is «the row is dropped and
	// everything around it imports». Nothing is dropped here and nothing around it imports: the
	// import refuses the card. An operator told «one row was unusable» would go looking for the
	// row and find a card that simply does not arrive.
	ReasonCardNotImportable Reason = "card_not_importable"
)
