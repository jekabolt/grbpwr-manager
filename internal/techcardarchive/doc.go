// Package techcardarchive is the FORMAT of the tech-card ZIP — the layout of the archive, the
// types of its manifest and sidecars, and the closed dictionary of reasons a piece of a card could
// not travel. FORMAT.md in this directory is the document; this package is its Go transcription,
// and the two are changed together or not at all.
//
// THE PACKAGE IS A LEAF AND MUST STAY ONE. Both sides of the feature import it — the export
// (internal/apisrv/admin, internal/bucket) and the import (the same, plus the store) — so a
// dependency on internal/store or internal/apisrv here would be a cycle waiting to happen and,
// worse, would let the format's meaning drift into whichever side happened to touch it last. What
// is allowed in: the standard library and the generated proto packages. What is not: stores,
// servers, the bucket, dto converters.
//
// WHAT THE FORMAT PROMISES, in one place, because the promises are the reason the package exists:
//
//   - No money. Costing, prices, unit costs and their provenance are cut unconditionally, by name
//     and again by a recursive pass, without asking who is exporting — stripping is a property of
//     the format, not of the caller's rights. Manifest.MoneyPolicy is the flag that sits next to
//     the check: an import refuses an archive that does not carry MoneyPolicyStrippedV1.
//   - No signature, not even a reserved field for one. An archive is not evidence, and a card that
//     arrives in one carries no authority: the import forces it to draft before writing.
//   - No foreign ids written as ids. Every id in the archive is remapped through Manifest.IDMaps
//     or an index, or dropped with an ExportHole.
//   - Degradation over refusal. Corruption, a wrong MAJOR and a missing money policy fail the
//     whole archive; everything else is a hole with a Reason and the import continues.
package techcardarchive
