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
	// ReasonCompositionNotDerived — the structured fibre breakdown travelled and was not written.
	//
	// It is a PROJECTION of style_composition, a table whose only writer re-derives it from the
	// card's own shell-fabric lines against THIS catalogue's articles, on every save of the card.
	// Writing the archive's rows would therefore state a breakdown of somebody else's catalogue as
	// a fact about this base's BOM, and the imported card's first save would replace it without
	// saying so — a reported loss traded for an unreported one. The legacy free-text `composition`
	// travels and IS written, so the card is not silent about what it is made of.
	ReasonCompositionNotDerived Reason = "composition_not_derived"
	// ReasonWastageClaimDegraded — a wastage/consumption claim lost its provenance and reads as
	// manual.
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
)
