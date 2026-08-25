package techcardarchive

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE WRITER'S ONE CONTRACT: WHAT IT RETURNS WITHOUT AN ERROR, OpenArchive ACCEPTS.
//
// Everything below follows from that sentence. The reader in this package is the trust boundary
// for archives arriving from outside; for archives LEAVING it is the oracle, and it is a cheap one
// because it lives three files away. So every refusal the reader can pronounce — a ceiling of
// FORMAT.md §1.3, a forbidden entry name, a duplicate entry, a missing mandatory file — is checked
// HERE, before a byte is compressed, against the reader's own constants and the reader's own
// validator (validateArchiveEntryName, classifyArchiveEntry are used verbatim, not re-implemented).
//
// The alternative was to write whatever the caller handed over and let the far end discover the
// problem. That is worse than it sounds: the far end is a partner's server, the failure arrives as
// «archive refused» with no way back to the card that produced it, and the operator who exported it
// heard nothing but success. An export that cannot be imported by our own reader is a failed
// export, and this is the file that says so while the operator is still looking.
//
// TWO THINGS THIS FILE DELIBERATELY DOES NOT DO:
//
//   - It does not buffer the archive. Binary files are streamed from the caller's spooled copies
//     through the zip.Writer into whatever io.Writer it was handed (an io.Pipe into the bucket, in
//     the handler). A card with a gigabyte of media must not become a gigabyte of RAM.
//   - It does not INVENT content. Contents counters come from the index lengths it was given, holes
//     come from the two collectors merged by the caller, and nothing here decides that a card «has
//     no media» — an empty index is the caller's statement, not this file's discovery.
// ─────────────────────────────────────────────────────────────────────────────

// Errors the writer produces. They are separate from the reader's three (ErrCorrupt / ErrRefused /
// ErrNotFound) on purpose: those classify an archive somebody handed US, these classify OUR OWN
// input, and a handler mapping them onto gRPC codes needs to tell «this card is an anomaly» from
// «the two halves of the export disagree».
var (
	// ErrArchiveTooLarge — the card genuinely does not fit the format: more entries than §1.3
	// allows, more bytes than the 1 GiB total, or a single file over its own ceiling. The caller
	// maps this onto ResourceExhausted, and the words have to name the card, because the only
	// answers available to a human are «split this card» or «raise the format», never «retry».
	ErrArchiveTooLarge = errors.New("techcardarchive: the card does not fit the archive format")
	// ErrArchiveInconsistent — the ARCHIVE CONTRADICTS ITSELF before it is even written: an index
	// row naming a file nobody supplied, a file nobody's index names, two entries under one name,
	// an entry name the format forbids. Never a data condition — always a defect at the seam
	// between the collectors and this writer, which is exactly the seam two people build in
	// parallel and neither one tests.
	ErrArchiveInconsistent = errors.New("techcardarchive: archive content contradicts its own indexes")
)

// ArchiveInput is everything an archive is made of, already collected. The writer owns the
// LAYOUT — names, order, the manifest's derived halves — and nothing else: it does not read a
// database, does not open a bucket, and cannot tell whether a hole is missing from the list.
//
// The split matters at the seam. card.json is built in package admin (it needs the dto converter
// and the API's own money strip) and the sidecars are collected there too (they need the store);
// this package must not import a handler package, so the two halves arrive as data and the writer
// is the one place that sees both.
type ArchiveInput struct {
	// ExportedAt stamps the manifest AND every zip entry's modification time. Zero means «now»:
	// a caller that does not care gets a sane archive, and a test that does care gets a
	// byte-for-byte reproducible one.
	ExportedAt time.Time
	// ExportedBy is the admin username, provenance only (FORMAT.md §2).
	ExportedBy string
	// Source is the exporting instance's passport. Never resolved by an importer — see Source.
	Source Source
	// IDMaps are the dictionaries that survive the trip. Sizes MUST be a superset of every size id
	// appearing anywhere in the archive, marker blobs included (§5.7); the writer cannot verify
	// that — it never parses card.json or a marker blob — which is precisely why the seam note
	// exists and why the caller merges the card's dictionary with the collectors' SizeNames.
	IDMaps IDMaps
	// Holes is the MERGED list: what card.json could not carry plus what the sidecars could not.
	// Merged by the caller because only the caller has both halves; losing one half here would
	// produce an archive that looks complete and is not.
	Holes []ExportHole

	// CardJSON is protojson of common.TechCard with money cut and the instance's own facts
	// scrubbed. Mandatory: FORMAT.md §1 says an archive without it is not one of ours.
	CardJSON []byte

	// SizeChart is written ALWAYS, empty cells included. The other three sidecars are written only
	// when non-empty. §1 makes an empty index and an absent file equally legal, so this is a
	// choice rather than a rule — and the choice is «a reader never has to wonder whether the
	// measurement grid was omitted or lost», while a media/ directory nobody needs is just noise.
	SizeChart SizeChart
	Assembly  []AssemblyLink
	Colorways []ColorwayPayload
	Materials []MaterialPassport

	// Media / Patterns / Markers are the three indexes. Their LENGTHS are the manifest's contents
	// counters — not the number of files, which dedup makes smaller (two media ids with identical
	// bytes share one file). The import's positive control compares its parsed ROWS against these
	// numbers, so counting files here would make a correct import look like a broken one.
	Media    []MediaIndexEntry
	Patterns []PatternIndexEntry
	Markers  []MarkerIndexEntry

	// MarkerFiles are the markers/<slug>-<n>.json blobs, in memory: a layout is capped at 2 MiB on
	// the live save path and there are units of them per card.
	MarkerFiles []JSONFile
	// Files are the content-addressed binaries — media/<sha256>.<ext> and patterns/<sha256>.<ext>.
	// They are STREAMED, never held: the caller spools them to disk and hands over openers.
	Files []BinaryFile
}

// JSONFile is one ready JSON entry: its root-relative name and its bytes.
type JSONFile struct {
	Name string
	Data []byte
}

// BinaryFile is one content-addressed entry the writer streams into the ZIP.
//
// SHA256 and Size are not decoration: the name encodes the digest (§1.1), so the writer VERIFIES
// the bytes against it while streaming, and Size lets the total-bytes ceiling be decided before the
// first gigabyte moves rather than after.
type BinaryFile struct {
	// Name is the root-relative entry name, "media/<sha256>.<ext>".
	Name string
	// SHA256 is the digest the index carries for this file, lower-case hex.
	SHA256 string
	// Size is the byte length the caller measured when it spooled the file.
	Size int64
	// Open returns a fresh reader over the bytes. Called exactly once per file, and the writer
	// closes what it gets.
	Open func() (io.ReadCloser, error)
}

// WriteArchive lays the whole archive into w and returns the manifest it wrote.
//
// The returned Manifest is the caller's copy of what travelled — the handler projects it onto the
// wire so the operator sees WHAT left the building and what was missing from it before the file
// does. It is returned rather than re-derived because a second derivation is a second answer.
//
// ORDER IS FIXED AND IS PART OF THE PRODUCT: manifest.json first, everything else by name. First
// because a consumer streaming the ZIP forward — ours reads the central directory, a partner's may
// not — can decide whether to read the rest before spending any bandwidth on it; the alphabet
// afterwards because «deterministic» has to be a rule somebody can check, and «the order the maps
// happened to iterate in» is not one.
func WriteArchive(w io.Writer, in ArchiveInput) (Manifest, error) {
	m, entries, err := planArchive(in)
	if err != nil {
		return Manifest{}, err
	}

	zw := zip.NewWriter(w)
	for _, e := range entries {
		if err := e.writeTo(zw, m.ExportedAt); err != nil {
			// The half-written ZIP is abandoned deliberately: zw.Close() here would append a valid
			// central directory to a truncated body and produce an archive that OPENS and is
			// missing content. The caller's pipe carries the error instead, and the object is
			// never created.
			return Manifest{}, err
		}
	}
	// Close writes the central directory. Without it there is no ZIP at all — only a stream of
	// deflated blocks — which is why it happens here and not in the caller's defer, where an early
	// return would skip it and an error path would produce it.
	if err := zw.Close(); err != nil {
		return Manifest{}, fmt.Errorf("write archive: close zip: %w", err)
	}
	return m, nil
}

// archiveEntry is one planned entry: a name plus one of the two ways bytes reach the ZIP.
type archiveEntry struct {
	name string
	// data is the whole body for the JSON entries, which are built in memory anyway.
	data []byte
	// file is set instead of data for the content-addressed binaries, which stream.
	file *BinaryFile
	// size is the declared byte length of either shape — what the ceilings are decided against
	// before anything is written.
	size int64
	// store selects zip.Store over zip.Deflate. Set for media and pattern bodies: they are jpeg,
	// mp4 and already-compressed pdf, where deflate spends CPU proportional to the archive and
	// wins single-digit kilobytes. The reader accepts both methods, so this is a free choice.
	store bool
}

// writeTo puts one entry into the zip, streaming when it streams and verifying when it can.
func (e archiveEntry) writeTo(zw *zip.Writer, modified time.Time) error {
	hdr := &zip.FileHeader{Name: e.name, Modified: modified, Method: zip.Deflate}
	if e.store {
		hdr.Method = zip.Store
	}
	dst, err := zw.CreateHeader(hdr)
	if err != nil {
		return fmt.Errorf("write archive: create entry %q: %w", e.name, err)
	}

	if e.file == nil {
		if _, err := dst.Write(e.data); err != nil {
			return fmt.Errorf("write archive: write entry %q: %w", e.name, err)
		}
		return nil
	}

	rc, err := e.file.Open()
	if err != nil {
		return fmt.Errorf("write archive: open %q: %w", e.name, err)
	}
	defer rc.Close()

	// The digest is recomputed over the bytes that actually go INTO the zip, not trusted from the
	// index. The spooled copy was hashed when it was written, minutes and one temp directory ago;
	// this is the last moment at which «the name says sha X» can still be made true, and the far
	// end treats a disagreement as corruption of the whole import — a refusal there is unfixable,
	// a refusal here names the card.
	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(dst, h), rc)
	if err != nil {
		return fmt.Errorf("write archive: stream %q: %w", e.name, err)
	}
	if written != e.size {
		return fmt.Errorf("%w: %q was %d bytes when it was spooled and %d bytes when it was written",
			ErrArchiveInconsistent, e.name, e.size, written)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != e.file.SHA256 {
		return fmt.Errorf("%w: %q hashes to %s, the index says %s",
			ErrArchiveInconsistent, e.name, sum, e.file.SHA256)
	}
	return nil
}

// planArchive builds the manifest and the full, validated, ordered entry list WITHOUT writing
// anything.
//
// Everything that can refuse the archive is decided here, before the first byte leaves for the
// bucket. That is the difference between an export that fails and an export that half-succeeds:
// once bytes are streaming into a PutObject there is no way to unsay them except by aborting the
// upload, and the ceilings, the name rules and the index/file agreement are all knowable in
// advance.
func planArchive(in ArchiveInput) (Manifest, []archiveEntry, error) {
	if len(in.CardJSON) == 0 {
		return Manifest{}, nil, fmt.Errorf("%w: no %s to write", ErrArchiveInconsistent, FileCard)
	}

	exportedAt := in.ExportedAt
	if exportedAt.IsZero() {
		exportedAt = time.Now()
	}
	exportedAt = exportedAt.UTC()

	m := Manifest{
		// The three identity fields are the WRITER's, never the caller's. A caller that could set
		// them could ship an archive claiming a format it is not, or — the case that matters —
		// omit money_policy while the money was in fact cut, which the far end reads as «nobody
		// promised anything» and refuses. There is no legitimate export that wants a different
		// value in any of the three.
		Format:        FormatName,
		FormatVersion: FormatVersion,
		MoneyPolicy:   MoneyPolicyStrippedV1,

		ExportedAt: exportedAt,
		ExportedBy: in.ExportedBy,
		Source:     in.Source,
		IDMaps:     in.IDMaps,
		Contents: Contents{
			// Index rows, not files. See the comment on ArchiveInput.Media.
			Media:     len(in.Media),
			Patterns:  len(in.Patterns),
			Markers:   len(in.Markers),
			Materials: len(in.Materials),
		},
		ExportHoles: in.Holes,
	}
	// Empty lists and maps rather than JSON null. Both parse, and the reader does not care — but
	// the manifest is read by humans and by partners' parsers, and `"export_holes": null` invites
	// exactly one question («did the export fail to compute them?») that `[]` does not.
	if m.ExportHoles == nil {
		m.ExportHoles = []ExportHole{}
	}
	if m.IDMaps.Sizes == nil {
		m.IDMaps.Sizes = map[string]string{}
	}
	if m.IDMaps.Colorways == nil {
		m.IDMaps.Colorways = map[string]string{}
	}
	if m.IDMaps.CategoryPath == nil {
		m.IDMaps.CategoryPath = []string{}
	}

	entries := make([]archiveEntry, 0, 8+len(in.MarkerFiles)+len(in.Files))

	// card.json travels as it was built — protojson, compact, not re-encoded. Re-marshalling it
	// through encoding/json here would silently change protojson's spelling of enums, durations and
	// int64s, and the far end parses it as a proto message.
	entries = append(entries, archiveEntry{name: FileCard, data: in.CardJSON, size: int64(len(in.CardJSON))})

	// sizechart.json always; the rest only when they carry something (see ArchiveInput.SizeChart).
	sizeChart := in.SizeChart
	if sizeChart.Cells == nil {
		sizeChart.Cells = []SizeChartCell{}
	}
	jsonEntries := []struct {
		name  string
		value any
		write bool
	}{
		{FileSizeChart, sizeChart, true},
		{FileAssembly, in.Assembly, len(in.Assembly) > 0},
		{FileColorways, in.Colorways, len(in.Colorways) > 0},
		{FileMaterialsIndex, in.Materials, len(in.Materials) > 0},
		{FileMediaIndex, in.Media, len(in.Media) > 0},
		{FilePatternsIndex, in.Patterns, len(in.Patterns) > 0},
		{FileMarkersIndex, in.Markers, len(in.Markers) > 0},
	}
	for _, je := range jsonEntries {
		if !je.write {
			continue
		}
		blob, err := marshalArchiveJSON(je.name, je.value)
		if err != nil {
			return Manifest{}, nil, err
		}
		entries = append(entries, archiveEntry{name: je.name, data: blob, size: int64(len(blob))})
	}

	for _, mf := range in.MarkerFiles {
		entries = append(entries, archiveEntry{name: mf.Name, data: mf.Data, size: int64(len(mf.Data))})
	}
	for i := range in.Files {
		f := &in.Files[i]
		entries = append(entries, archiveEntry{name: f.Name, file: f, size: f.Size, store: true})
	}

	if err := checkIndexesAgainstFiles(in); err != nil {
		return Manifest{}, nil, err
	}

	// The manifest is marshalled LAST of the JSON files and written FIRST of the entries: it counts
	// what the others contain, so it cannot exist before them.
	manifestBlob, err := marshalArchiveJSON(FileManifest, m)
	if err != nil {
		return Manifest{}, nil, err
	}

	// Alphabetical among themselves, manifest.json prepended afterwards — so the rule «manifest
	// first» is not something a name like "aaa.json" could ever beat.
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	entries = append([]archiveEntry{{
		name: FileManifest, data: manifestBlob, size: int64(len(manifestBlob)),
	}}, entries...)

	if err := checkArchiveEntries(entries); err != nil {
		return Manifest{}, nil, err
	}
	return m, entries, nil
}

// marshalArchiveJSON renders one of the writer's own JSON files.
//
// Indented, because every one of these is a file a human opens to find out what a partner sent —
// and deflate returns the whitespace to almost nothing. The ceiling is checked here rather than at
// the end: a 16 MB sidecar is refused by our own reader on the DIRECTORY pass, before any of its
// content is read, which makes it a whole-archive refusal at the far end and therefore something
// that must never be written.
func marshalArchiveJSON(name string, v any) ([]byte, error) {
	blob, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("write archive: encode %s: %w", name, err)
	}
	if len(blob) > MaxCardJSONBytes {
		return nil, fmt.Errorf("%w: %s is %d bytes, the format ceiling is %d",
			ErrArchiveTooLarge, name, len(blob), MaxCardJSONBytes)
	}
	return blob, nil
}

// checkIndexesAgainstFiles verifies that the three indexes and the two file lists describe the same
// archive, in both directions.
//
// Both directions, because they fail differently and both failures are invisible from inside one
// half. An index row naming a file nobody supplied is a hole the export never declared: the import
// reports media_missing for a picture the operator watched «succeed». A file nobody's index names
// is bytes shipped outside the format — harmless to read, but it means a collector produced content
// it did not describe, and the next thing that goes missing that way will be described wrongly
// rather than not at all.
//
// This is the seam between the two collectors and this writer, i.e. exactly the join that no
// single-half test covers.
func checkIndexesAgainstFiles(in ArchiveInput) error {
	binaries := make(map[string]bool, len(in.Files))
	for _, f := range in.Files {
		if f.Open == nil {
			return fmt.Errorf("%w: %q has no way to open its bytes", ErrArchiveInconsistent, f.Name)
		}
		binaries[f.Name] = true
	}
	markers := make(map[string]bool, len(in.MarkerFiles))
	for _, mf := range in.MarkerFiles {
		markers[mf.Name] = true
	}

	referenced := make(map[string]bool, len(binaries))
	for _, e := range in.Media {
		if !binaries[e.File] {
			return fmt.Errorf("%w: %s names %q, which the export did not supply",
				ErrArchiveInconsistent, FileMediaIndex, e.File)
		}
		referenced[e.File] = true
	}
	for _, e := range in.Patterns {
		if !binaries[e.File] {
			return fmt.Errorf("%w: %s names %q, which the export did not supply",
				ErrArchiveInconsistent, FilePatternsIndex, e.File)
		}
		referenced[e.File] = true
	}
	for _, f := range in.Files {
		if !referenced[f.Name] {
			return fmt.Errorf("%w: %q is in the archive and no index names it",
				ErrArchiveInconsistent, f.Name)
		}
	}

	referencedMarkers := make(map[string]bool, len(markers))
	for _, e := range in.Markers {
		if !markers[e.File] {
			return fmt.Errorf("%w: %s names %q, which the export did not supply",
				ErrArchiveInconsistent, FileMarkersIndex, e.File)
		}
		referencedMarkers[e.File] = true
	}
	for _, mf := range in.MarkerFiles {
		if !referencedMarkers[mf.Name] {
			return fmt.Errorf("%w: %q is in the archive and %s does not name it",
				ErrArchiveInconsistent, mf.Name, FileMarkersIndex)
		}
	}
	return nil
}

// checkArchiveEntries runs the READER's own gate over the archive about to be written.
//
// Not a re-implementation of it and deliberately not a similar one: validateArchiveEntryName and
// classifyArchiveEntry are the very functions OpenArchive calls, and MaxZipEntries /
// MaxUncompressedBytes are the very constants it compares against. Two copies of «is this archive
// acceptable» is how an export and an import come to disagree, and the loser is always whichever
// check runs second — which here is the partner's, three days later, with no card to point at.
//
// The one asymmetry: an entry the classifier does not KNOW is allowed through. It is not a
// refusal at the far end — §3 makes it a report line (unknown_entry) and the bytes are still served
// through the index — and the case that produces it is real: a media object whose extension is
// outside the list in §1.1 (.avif and friends reach the bucket verbatim). Refusing the export of a
// whole card over one picture's extension would trade a report line for a dead feature.
func checkArchiveEntries(entries []archiveEntry) error {
	if len(entries) > MaxZipEntries {
		return fmt.Errorf("%w: %d entries, the format ceiling is %d",
			ErrArchiveTooLarge, len(entries), MaxZipEntries)
	}

	seen := make(map[string]bool, len(entries))
	var total int64
	for _, e := range entries {
		if err := validateArchiveEntryName(e.name); err != nil {
			// The reader's own words, wrapped in ours: the name came from a size name or an object
			// key, i.e. from something a person typed, and the export is where that stops.
			return fmt.Errorf("%w: %w", ErrArchiveInconsistent, err)
		}
		if seen[e.name] {
			return fmt.Errorf("%w: two entries would be named %q", ErrArchiveInconsistent, e.name)
		}
		seen[e.name] = true

		if e.size < 0 {
			return fmt.Errorf("%w: %q declares a negative size %d", ErrArchiveInconsistent, e.name, e.size)
		}
		if class := classifyArchiveEntry(e.name); class.ceiling > 0 && e.size > class.ceiling {
			return fmt.Errorf("%w: %s is %d bytes, its format ceiling is %d",
				ErrArchiveTooLarge, e.name, e.size, class.ceiling)
		}
		total += e.size
		if total > MaxUncompressedBytes {
			// The collectors already refuse a card whose BINARIES pass this ceiling, and they must:
			// by the time the writer sees them a gigabyte has been spooled to disk. This is the
			// same number applied to the WHOLE archive — card.json, the sidecars and the marker
			// blobs included — which is the sum the reader actually compares against, and which no
			// single collector can see.
			return fmt.Errorf("%w: the archive holds more than %d uncompressed bytes",
				ErrArchiveTooLarge, MaxUncompressedBytes)
		}
	}
	return nil
}
