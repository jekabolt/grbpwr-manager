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
)
