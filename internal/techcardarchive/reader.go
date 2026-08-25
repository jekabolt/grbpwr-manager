package techcardarchive

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/encoding/protojson"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE READER IS THE TRUST BOUNDARY OF THE WHOLE FEATURE.
//
// An archive arrives from outside: from a partner, off a disk, out of an instance nobody here
// administers. Everything below therefore treats the ZIP as a claim and not as a fact — the entry
// count, the declared sizes, the digests, the version, even the entry names are things the archive
// says about itself, and each one is checked before anything downstream is allowed to believe it.
//
// Two disciplines run through the file:
//
//   - A REFUSAL IS A REFUSAL, NEVER A TRIM. Nothing here silently drops content to fit under a
//     ceiling: an archive that breaches a limit fails, because a quietly truncated import is
//     indistinguishable from a clean one at the point where the operator looks. The one thing that
//     survives without being read is an entry this server does not know — and it is LISTED
//     (UnknownEntries → reason unknown_entry), which is the opposite of silence.
//   - EVERY NUMBER COMES FROM format.go. The ceilings of FORMAT.md §1.3 live there as constants and
//     are read from there; a reader minting its own limit is how two answers to «is this archive
//     too big» appear, and the loser is whichever check runs second.
// ─────────────────────────────────────────────────────────────────────────────

// The three verdicts a caller has to tell apart. Their split is FORMAT.md §6.3: corruption, a wrong
// MAJOR and a missing money policy fail the WHOLE archive, everything else degrades into a hole.
//
//   - ErrNotFound is the only NON-fatal one: an index row names a file the archive does not carry,
//     which is a hole (media_missing, pattern_invalid, …) and not a reason to abort.
//   - ErrCorrupt means THE BYTES DO NOT HOLD WHAT THEY CLAIM — a sha256 that disagrees with the
//     index or with the entry's own name, an entry that delivers a different number of bytes than
//     it declares, a CRC failure, JSON that will not parse. FORMAT.md §1.2: corruption fails the
//     whole import, never a hole.
//   - ErrRefused means THE BYTES ARE FINE AND THE CONTRACT SAYS NO — a foreign format name, another
//     MAJOR, a missing money_policy, a ceiling of §1.3 breached, an entry name the format forbids.
//
// Callers classify with IsFatal, which fails CLOSED: anything that is not ErrNotFound aborts.
var (
	ErrNotFound = errors.New("techcardarchive: no such entry in the archive")
	ErrCorrupt  = errors.New("techcardarchive: archive is corrupt")
	ErrRefused  = errors.New("techcardarchive: archive refused")
)

// IsFatal answers the one question an import loop asks about every reader error: does this kill the
// import, or is it a hole in the report? It is written as «everything except a missing entry is
// fatal» rather than as a list of fatal codes on purpose — a future error class added here is fatal
// by default, and a caller that has not heard of it aborts instead of importing half a card.
func IsFatal(err error) bool {
	return err != nil && !errors.Is(err, ErrNotFound)
}

// Archive is an OPENED tech-card ZIP: its manifest is parsed and every entry is validated, and
// nothing else has been read. The bodies stay in the ZIP until somebody asks for one — a card with
// a gigabyte of media must not become a gigabyte of RAM because the reader was eager.
//
// Safe for concurrent use exactly as far as the io.ReaderAt handed to OpenArchive is: every
// OpenFile takes its own section of it.
type Archive struct {
	// Manifest is the parsed passport. It is the 1.0 SHAPE of the manifest — read it for the fields
	// this server knows, and never re-marshal it back into anything durable: see ManifestRaw.
	Manifest *Manifest

	// ManifestRaw is manifest.json VERBATIM, and it is what has to be stored (tech_card_import.
	// archive_manifest) or echoed back.
	//
	// The reason is the MINOR rule of FORMAT.md §3 meeting encoding/json: an archive of a newer 1.x
	// carries fields this struct has no member for, json.Unmarshal drops them without a word, and a
	// re-marshal of Manifest would write a manifest that is missing them — under the label «what was
	// in the ZIP at upload». The journal would be lying, quietly and permanently. The bytes below
	// are the only copy that is true.
	//
	// Not to be mutated: it is the archive's memory, not the caller's buffer.
	ManifestRaw []byte

	// UnknownEntries lists every entry name that matches nothing this server knows, in the order the
	// ZIP holds them. This is the MINOR-compatibility rule of FORMAT.md §3 being executed rather
	// than described: a newer exporter may add files, an older reader may not choke on them — and
	// may not swallow them either, so each one becomes a ReasonUnknownEntry line in the report.
	//
	// A name the format FORBIDS (traversal, absolute, backslash, control characters) is not listed
	// here — it refuses the archive. See validateArchiveEntryName.
	UnknownEntries []string

	// entries is the ZIP directory after validation: every name here is one the format permits, it
	// appears exactly once, and its body can be decompressed. Read-only once OpenArchive returns,
	// which is what makes an Archive safe to share as far as its io.ReaderAt is.
	entries map[string]*zip.File
}

// OpenArchive validates a tech-card ZIP and parses its manifest, reading nothing else.
//
// The order of the checks is the point: everything that can be decided from the ZIP DIRECTORY —
// entry count, declared sizes, names, compression methods, duplicates — is decided before a single
// byte of content is decompressed, and the manifest is the only body read here, because it is what
// decides whether the rest may be read at all.
//
// size is the length of the archive. The 256 MiB body ceiling (MaxUploadedArchiveBytes) is NOT
// applied here and must not be: it belongs to the HTTP import route. An archive produced by our own
// export can legitimately be far larger than that — media is already-compressed jpeg and mp4, so a
// ZIP of it barely shrinks — and reading such a file from the bucket is a legal thing to do.
func OpenArchive(ra io.ReaderAt, size int64) (*Archive, error) {
	if ra == nil {
		return nil, fmt.Errorf("%w: no archive to read", ErrRefused)
	}
	if size <= 0 {
		return nil, fmt.Errorf("%w: archive is %d bytes long", ErrCorrupt, size)
	}

	// The one thing that happens BEFORE any ceiling of ours: archive/zip parses the central
	// directory, so a body claiming millions of entries costs a *zip.File each before MaxZipEntries
	// below gets a word in. It is bounded — the standard library refuses to pre-allocate for a record
	// count the file is too small to hold (reader.go:132) and every entry still needs its 46 bytes on
	// disk, so the cap is the uploaded body divided by 46 — and the import route is the thing that
	// sets that body cap (MaxUploadedArchiveBytes) behind an authenticated write right. Bounded and
	// authenticated, not free: pre-empting it would mean hand-parsing the EOCD, zip64 locator and all,
	// which is more untrusted-input parsing than it saves.
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		// Includes the plain «this upload is not a ZIP at all» case, which is the commonest one a
		// human will hit, so the wording has to survive being shown to them.
		return nil, fmt.Errorf("%w: not a readable ZIP: %v", ErrCorrupt, err)
	}

	a := &Archive{entries: make(map[string]*zip.File)}

	// The zip-bomb pair (FORMAT.md §1.3, format.go): a cap on entries alone is defeated by one entry
	// that inflates to a terabyte, a cap on bytes alone by a million empty entries. Both, or neither.
	if len(zr.File) > MaxZipEntries {
		return nil, fmt.Errorf("%w: %d entries, the ceiling is %d", ErrRefused, len(zr.File), MaxZipEntries)
	}

	var totalDeclared int64
	for _, f := range zr.File {
		name := f.Name
		if err := validateArchiveEntryName(name); err != nil {
			return nil, err
		}
		if err := validateArchiveEntryMethod(f); err != nil {
			return nil, err
		}

		// Per-entry first, then the sum. Per-entry first is not tidiness: 4096 entries each
		// declaring 2^63 bytes would overflow the running total, and rejecting any single entry
		// above the 1 GiB ceiling before adding it keeps the sum inside 4096 GiB, where int64 is
		// nowhere near its edge.
		declared := f.UncompressedSize64
		if declared > uint64(MaxUncompressedBytes) {
			return nil, fmt.Errorf("%w: entry %q declares %d bytes, the ceiling for the whole archive is %d",
				ErrRefused, name, declared, MaxUncompressedBytes)
		}
		totalDeclared += int64(declared)
		if totalDeclared > MaxUncompressedBytes {
			return nil, fmt.Errorf("%w: entries declare more than %d uncompressed bytes in total",
				ErrRefused, MaxUncompressedBytes)
		}

		// A directory entry carries no bytes and FORMAT.md §1.1 says the export writes none. It is
		// neither served nor listed as unknown — there is nothing in it to lose and nothing for an
		// operator to do about a report line naming a folder. It still counts against both ceilings
		// above, because a hostile ZIP does not have to be honest about what a trailing slash means.
		if strings.HasSuffix(name, "/") {
			continue
		}

		// Two entries under one name is a smuggling shape, not a MINOR: whoever verifies reads one
		// copy and whoever consumes reads the other. There is no reading of it that is safe enough
		// to be worth allowing.
		if _, dup := a.entries[name]; dup {
			return nil, fmt.Errorf("%w: entry %q appears twice", ErrRefused, name)
		}
		a.entries[name] = f

		class := classifyArchiveEntry(name)
		if !class.known {
			a.UnknownEntries = append(a.UnknownEntries, name)
			continue
		}
		// The ceiling is checked against the DECLARED size here so an oversized entry dies before
		// anything decompresses it, and again against the bytes that actually arrive in
		// archiveFileReader — a declared size is a claim, and this is the file that stops believing
		// claims.
		if class.ceiling > 0 && declared > uint64(class.ceiling) {
			return nil, fmt.Errorf("%w: entry %q declares %d bytes, its ceiling is %d",
				ErrRefused, name, declared, class.ceiling)
		}
	}

	// FORMAT.md §1: manifest.json and card.json are mandatory, everything else is optional. A ZIP
	// without them is not an archive of ours that lost something — it is not one of ours.
	if _, ok := a.entries[FileManifest]; !ok {
		return nil, fmt.Errorf("%w: no %s", ErrRefused, FileManifest)
	}
	if _, ok := a.entries[FileCard]; !ok {
		return nil, fmt.Errorf("%w: no %s", ErrRefused, FileCard)
	}

	raw, err := a.ReadFile(FileManifest)
	if err != nil {
		return nil, err
	}
	var m Manifest
	// Plain Unmarshal, NOT DisallowUnknownFields: a newer MINOR adds fields and this server must
	// still read it (§3). The fields it does not know survive in ManifestRaw, which is why that
	// field exists.
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%w: %s does not parse: %v", ErrCorrupt, FileManifest, err)
	}
	if err := checkManifestContract(&m); err != nil {
		return nil, err
	}
	a.Manifest = &m
	a.ManifestRaw = raw

	return a, nil
}

// checkManifestContract is the whole-archive gate of FORMAT.md §3 and §4: who wrote this, may this
// server read it, and does it promise its money was cut. All three are refusals rather than holes —
// none of them is a thing an operator can be asked to fix afterwards.
func checkManifestContract(m *Manifest) error {
	if m.Format != FormatName {
		return fmt.Errorf("%w: manifest says format %q, this server reads %q", ErrRefused, m.Format, FormatName)
	}

	major, minor, err := ParseFormatVersion(m.FormatVersion)
	if err != nil {
		return err
	}
	if major != FormatMajor {
		// The words have to say WHICH WAY, because the two have different answers: a newer archive
		// needs a newer server, an older one needs an older export.
		direction := "older than"
		if major > FormatMajor {
			direction = "newer than"
		}
		return fmt.Errorf("%w: archive format %d.%d is %s this server's %d.%d — a MAJOR difference renames fields and moves files",
			ErrRefused, major, minor, direction, FormatMajor, FormatMinor)
	}
	// MINOR is deliberately unchecked in both directions. Backwards: a server reads every MINOR of
	// its own MAJOR. Forwards: a MINOR is additive, so a 1.9 archive read by a 1.0 server loses the
	// additions and nothing else — and the additions it loses are exactly what UnknownEntries,
	// ManifestRaw and protojson's DiscardUnknown are there to keep visible or intact.

	// The flag that sits NEXT TO THE CHECK (FORMAT.md §4): an archive that does not say its money
	// was cut is an archive nobody promised it was. Absence is the case that matters — a hand-made
	// or pre-versioned bundle with costing in it has no reason to carry the string.
	if m.MoneyPolicy != MoneyPolicyStrippedV1 {
		return fmt.Errorf("%w: money_policy is %q, this server imports only %q",
			ErrRefused, m.MoneyPolicy, MoneyPolicyStrippedV1)
	}
	return nil
}

// ParseFormatVersion splits "MAJOR.MINOR" (FORMAT.md §3). Exported because the refusal it feeds is
// shown to a human, and because the export writes the same string from the same pair.
//
// Strict on shape and forgiving on spelling: exactly two decimal parts, nothing else — no "1", no
// "1.0.0", no "v1.0", no whitespace, no sign — while "01.0" and "1.00" parse, because a leading
// zero is a formatting habit and not a different version.
func ParseFormatVersion(s string) (major, minor int, err error) {
	maj, min, ok := strings.Cut(s, ".")
	if !ok {
		return 0, 0, fmt.Errorf("%w: format_version %q is not MAJOR.MINOR", ErrRefused, s)
	}
	major, err = parseVersionPart(maj)
	if err == nil {
		minor, err = parseVersionPart(min)
	}
	if err != nil {
		return 0, 0, fmt.Errorf("%w: format_version %q is not MAJOR.MINOR", ErrRefused, s)
	}
	return major, minor, nil
}

func parseVersionPart(s string) (int, error) {
	// Digits only, and few of them: strconv.Atoi accepts a leading sign and would turn "1.-0" into a
	// version, and an unbounded digit run into an overflow argument nobody wants to have.
	if s == "" || len(s) > 9 {
		return 0, errors.New("empty or over-long version part")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, errors.New("non-digit in version part")
		}
	}
	return strconv.Atoi(s)
}

// ─────────────────────────────────────────────────────────────────────────────
// Entry names
// ─────────────────────────────────────────────────────────────────────────────

// validateArchiveEntryName enforces the last line of FORMAT.md §1.1: no entry name may contain
// "..", a leading "/", or a backslash — plus the two the prose takes for granted, that a name is
// non-empty text and not a place to hide control bytes.
//
// A forbidden name REFUSES THE ARCHIVE and is not filed under UnknownEntries, and the difference is
// the whole reason this function is separate from classifyArchiveEntry. unknown_entry is the escape
// hatch for a FILE THIS SERVER DOES NOT KNOW — a newer MINOR being read by an older reader. A name
// carrying "../" is not a newer file; it is a broken or hostile archive, and the reader hands entry
// names onward — the import turns them into report lines, and the file-upload phase turns pattern
// and media names into object keys. A traversal name that reached UnknownEntries would be a
// traversal name exported to every consumer downstream. It stops here.
func validateArchiveEntryName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: the archive holds an entry with an empty name", ErrRefused)
	case !utf8.ValidString(name):
		return fmt.Errorf("%w: entry name is not valid UTF-8", ErrRefused)
	case strings.Contains(name, ".."):
		return fmt.Errorf("%w: entry name %q contains %q", ErrRefused, name, "..")
	case strings.HasPrefix(name, "/"):
		return fmt.Errorf("%w: entry name %q is absolute", ErrRefused, name)
	case strings.Contains(name, `\`):
		return fmt.Errorf("%w: entry name %q contains a backslash", ErrRefused, name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: entry name %q contains a control character", ErrRefused, name)
		}
	}
	return nil
}

// validateArchiveEntryMethod refuses an entry this process cannot honestly read: an encrypted one,
// or one compressed with a method archive/zip has no decompressor for.
//
// Upfront and whole-archive rather than lazily per file, because the alternative is discovering it
// in the middle of the import, after a card has been half-resolved, and having to decide there
// whether a body nobody can decompress is a hole. It is not a hole — it is an archive this server
// cannot read, and that is a sentence best said before any work is done.
func validateArchiveEntryMethod(f *zip.File) error {
	const flagEncrypted = 0x1
	if f.Flags&flagEncrypted != 0 {
		return fmt.Errorf("%w: entry %q is encrypted", ErrRefused, f.Name)
	}
	if f.Method != zip.Store && f.Method != zip.Deflate {
		return fmt.Errorf("%w: entry %q uses compression method %d, this server reads only stored and deflated entries",
			ErrRefused, f.Name, f.Method)
	}
	return nil
}

// archiveEntryClass is what the reader knows about one entry name from the name alone.
type archiveEntryClass struct {
	// known is false for a name matching nothing in FORMAT.md §1 — the unknown_entry case.
	known bool
	// sha is the digest ENCODED IN THE NAME for the content-addressed entries (media/, patterns/),
	// empty for everything else. FORMAT.md §1.1 names binary files by the sha256 of their content,
	// which makes the name a second, independent copy of the digest the index carries — and this
	// reader checks the body against it even when the caller supplies no expectation of its own.
	sha string
	// ceiling is the §1.3 limit for this entry, 0 when the entry has none of its own and is bounded
	// only by its declared size and the archive-wide total.
	ceiling int64
}

// classifyArchiveEntry maps a name onto the tree of FORMAT.md §1. It decides only what a name is —
// never whether it may be opened. An unknown name is still served by OpenFile: the classification
// exists for the report and for the ceilings, and refusing to hand over bytes an index points at
// would turn a newer MINOR into a broken import, which is precisely what §3 forbids.
func classifyArchiveEntry(name string) archiveEntryClass {
	switch name {
	case FileManifest:
		// FORMAT.md §1.3 gives no row for manifest.json or for the sidecar JSONs, yet the manifest
		// is read WHOLE into memory here and the sidecars are read whole by the resolver. Rather
		// than mint a new number (see the header of this file), they take the ceiling of the other
		// top-level JSON, card.json. A DIVERGENCE FROM THE DOCUMENT, deliberately in the safe
		// direction, and one §1.3 should absorb.
		return archiveEntryClass{known: true, ceiling: MaxCardJSONBytes}
	case FileCard:
		return archiveEntryClass{known: true, ceiling: MaxCardJSONBytes}
	case FileSizeChart, FileAssembly, FileColorways,
		FileMaterialsIndex, FileMediaIndex, FilePatternsIndex, FileMarkersIndex:
		return archiveEntryClass{known: true, ceiling: MaxCardJSONBytes}
	}

	if sha, ok := contentAddressedName(name, DirMedia, mediaExtensions); ok {
		return archiveEntryClass{known: true, sha: sha}
	}
	if sha, ok := contentAddressedName(name, DirPatterns, patternExtensions); ok {
		return archiveEntryClass{known: true, sha: sha}
	}
	if isMarkerBlobName(name) {
		return archiveEntryClass{known: true, ceiling: MaxMarkerFileBytes}
	}
	return archiveEntryClass{}
}

// The extensions FORMAT.md §1.1 lists, exactly. An extension outside them is not an error and not a
// refusal — the file simply becomes an unknown_entry, and if an index points at it the bytes are
// still served and still verified against that index's digest. That is what makes the list safe to
// keep short: being wrong about it costs a report line, not an import.
var (
	mediaExtensions   = []string{"jpg", "jpeg", "png", "webp", "gif", "mp4", "webm"}
	patternExtensions = []string{"dxf", "pdf"}
)

// contentAddressedName recognises "<dir>/<64 lower-case hex>.<ext>" and returns the digest the name
// asserts about its own content.
func contentAddressedName(name, dir string, exts []string) (string, bool) {
	if !strings.HasPrefix(name, dir) {
		return "", false
	}
	rest := name[len(dir):]
	dot := strings.IndexByte(rest, '.')
	if dot < 0 {
		return "", false
	}
	sha, ext := rest[:dot], rest[dot+1:]
	if !isLowerHex64(sha) {
		return "", false
	}
	for _, e := range exts {
		if ext == e {
			return sha, true
		}
	}
	return "", false
}

func isLowerHex64(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// isMarkerBlobName recognises "markers/<slug>-<n>.json" (FORMAT.md §1.1) — and recognising it is ALL
// it does. The name is display sugar: a reader locates a marker through markers/index.json, never
// by parsing the file name, so nothing is extracted from the slug here on purpose.
func isMarkerBlobName(name string) bool {
	if !strings.HasPrefix(name, DirMarkers) || !strings.HasSuffix(name, ".json") {
		return false
	}
	stem := name[len(DirMarkers) : len(name)-len(".json")]
	dash := strings.LastIndexByte(stem, '-')
	if dash <= 0 || dash == len(stem)-1 {
		return false
	}
	slug, counter := stem[:dash], stem[dash+1:]
	for i := 0; i < len(slug); i++ {
		c := slug[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	for i := 0; i < len(counter); i++ {
		if counter[i] < '0' || counter[i] > '9' {
			return false
		}
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// Reading
// ─────────────────────────────────────────────────────────────────────────────

// Has reports whether the archive carries an entry under that exact name. Exact: names are not
// case-folded and not cleaned, because the ZIP directory is not a filesystem and "Manifest.json" is
// simply a different string from the one the format defines.
func (a *Archive) Has(name string) bool {
	_, ok := a.entries[name]
	return ok
}

// OpenFile returns a streaming reader over one entry, verifying as it goes.
//
// Three guards ride along, and all three land at io.EOF or not at all:
//
//   - the body must be exactly as long as the ZIP directory declares it to be;
//   - it must survive archive/zip's own CRC check, WHICH IT MAY SKIP: a header with CRC32 zero is
//     taken as «unset» and not checked at all (archive/zip reader.go), so the digest below is the
//     part that a hand-made archive cannot walk past;
//   - if the NAME encodes a sha256 — media/<sha>.<ext>, patterns/<sha>.<ext> — the body must hash to
//     it. The digest in the name and the digest in the index are two independent copies of the same
//     truth, and this reader spends the first of them even when the caller brings nothing.
//
// VERIFICATION IS FINAL ONLY AT io.EOF. A consumer that streams these bytes somewhere — into a
// bucket, say — has necessarily already handed most of them over by the time the mismatch is known,
// so it must not COMMIT what it wrote until Read has returned io.EOF. A consumer that stops early
// and closes has verified nothing at all; ReadFile is the shape that cannot make that mistake.
func (a *Archive) OpenFile(name string) (io.ReadCloser, error) {
	return a.openFile(name, "")
}

// OpenFileVerified is OpenFile with the digest the caller read out of an index (media/index.json,
// patterns/index.json — FORMAT.md §1.2), which is the authority.
//
// When the name carries a digest too, the two are compared BEFORE a byte is decompressed and a
// disagreement is corruption on the spot: an index pointing at a file whose own name contradicts it
// is an archive that does not agree with itself, and the cheapest moment to say so is before the
// work.
func (a *Archive) OpenFileVerified(name, wantSHA256 string) (io.ReadCloser, error) {
	return a.openFile(name, strings.ToLower(strings.TrimSpace(wantSHA256)))
}

func (a *Archive) openFile(name, want string) (io.ReadCloser, error) {
	f, ok := a.entries[name]
	if !ok {
		// The one non-fatal error in the file. An index row naming a file the archive does not carry
		// is a hole (media_missing and friends), not a reason to abandon the card.
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}

	class := classifyArchiveEntry(name)
	if want != "" && !isLowerHex64(want) {
		return nil, fmt.Errorf("%w: %q is not a sha256 for entry %q", ErrCorrupt, want, name)
	}
	if want != "" && class.sha != "" && want != class.sha {
		return nil, fmt.Errorf("%w: the index says entry %q is %s, its own name says %s",
			ErrCorrupt, name, want, class.sha)
	}
	if want == "" {
		want = class.sha
	}

	// A restatement of the check OpenArchive already made over every known entry, and unreachable
	// through it today. It stays so that the ceiling is a property of THE READ, enforced where the
	// bytes are actually handed out, rather than a property of one loop somewhere else that a future
	// constructor could forget to run.
	declared := f.UncompressedSize64
	if class.ceiling > 0 && declared > uint64(class.ceiling) {
		return nil, fmt.Errorf("%w: entry %q declares %d bytes, its ceiling is %d",
			ErrRefused, name, declared, class.ceiling)
	}

	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: entry %q will not open: %v", ErrCorrupt, name, err)
	}
	r := &archiveFileReader{rc: rc, name: name, size: int64(declared), want: want}
	if want != "" {
		r.h = sha256.New()
	}
	return r, nil
}

// ReadFile reads one entry whole, and it is the shape that CANNOT skip verification, because
// io.ReadAll always reaches EOF.
//
// What bounds it, exactly: the entry's declared size, which the reader below will not let the body
// exceed — and that declared size is bounded in turn by the entry's §1.3 ceiling for the JSON files
// the format defines, and only by the archive-wide 1 GiB for everything else. So ReadFile on
// manifest.json, card.json, a sidecar or a marker blob is bounded by a small number; ReadFile on a
// media file, a pattern sheet or an unknown entry is bounded by the archive, and a caller who does
// not know how big those are should be streaming them through OpenFile instead.
func (a *Archive) ReadFile(name string) ([]byte, error) {
	return a.readFile(name, "")
}

// ReadFileVerified is ReadFile with the index's digest — see OpenFileVerified.
func (a *Archive) ReadFileVerified(name, wantSHA256 string) ([]byte, error) {
	return a.readFile(name, wantSHA256)
}

func (a *Archive) readFile(name, want string) ([]byte, error) {
	rc, err := a.openFile(name, strings.ToLower(strings.TrimSpace(want)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// CardJSON parses card.json into the readable tech-card model. The writable payload the import
// works on is the TechCard field inside it.
//
// DiscardUnknown is the MINOR rule of FORMAT.md §3 in one option, and it is the same mode release
// snapshots and marker layout blobs are already read with in this codebase — a document written by a
// newer server stays readable after a rollback. It is also the reason the 16 MB ceiling above
// matters: DiscardUnknown means protojson will not refuse anything for being strange, so the size is
// what has to refuse it for being enormous.
//
// Not cached: each call parses again and hands back a message the caller owns, rather than a shared
// one two phases could mutate into disagreement.
func (a *Archive) CardJSON() (*pb_common.TechCard, error) {
	raw, err := a.ReadFile(FileCard)
	if err != nil {
		return nil, err
	}
	var card pb_common.TechCard
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &card); err != nil {
		return nil, fmt.Errorf("%w: %s does not parse: %v", ErrCorrupt, FileCard, err)
	}
	return &card, nil
}

// archiveFileReader is the streaming half of «целостность»: it counts, it hashes, and it refuses to
// let a body be longer or shorter than the archive said it would be.
type archiveFileReader struct {
	rc   io.ReadCloser
	name string
	h    hash.Hash // nil when nothing is expected of the digest
	want string    // lower-case hex, "" when nothing is expected
	size int64     // the declared uncompressed size: the exact number of bytes that must arrive
	n    int64
	err  error // sticky: a stream that failed verification never resumes
}

func (r *archiveFileReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.rc.Read(p)
	if n > 0 {
		r.n += int64(n)
		if r.n > r.size {
			// The bomb case the ZIP directory cannot be trusted about: an entry that claims to be
			// small and keeps inflating.
			//
			// archive/zip carries the same check on its own body reader and, being the INNER
			// reader, it normally fires one moment earlier — so against a real ZIP this branch is
			// belt to that reader's braces, and the overflow arrives below as a read error instead.
			// It stays because it is what makes THIS type's contract true — «never more bytes than
			// declared» — for any reader it is wrapped around, and because a guarantee that is only
			// an implementation detail of somebody else's package is not a guarantee. It is tested
			// directly rather than through a ZIP for exactly that reason.
			return 0, r.fail(fmt.Errorf("%w: entry %q declares %d bytes and delivers more",
				ErrCorrupt, r.name, r.size))
		}
		if r.h != nil {
			r.h.Write(p[:n])
		}
	}

	switch {
	case err == nil:
		return n, nil
	case !errors.Is(err, io.EOF):
		// The counters go into the words and the underlying error is not interpreted. archive/zip
		// answers a body that outgrows its declared size, one that falls short of it and one that
		// fails its CRC with three different sentinels, and translating them into confident prose
		// here would mean asserting which of its internal branches fired. «Died after n of size
		// bytes, and here is what it said» is diagnosable without guessing.
		return n, r.fail(fmt.Errorf("%w: entry %q will not read after %d of %d declared bytes: %v",
			ErrCorrupt, r.name, r.n, r.size, err))
	}

	// EOF: everything the entry promised has to be true right now, and this is the last moment it
	// can be checked.
	if r.n != r.size {
		return 0, r.fail(fmt.Errorf("%w: entry %q declares %d bytes and delivers %d",
			ErrCorrupt, r.name, r.size, r.n))
	}
	if r.h != nil {
		if got := hex.EncodeToString(r.h.Sum(nil)); got != r.want {
			return 0, r.fail(fmt.Errorf("%w: entry %q hashes to %s, the archive says %s",
				ErrCorrupt, r.name, got, r.want))
		}
	}
	r.err = io.EOF
	return n, io.EOF
}

// fail makes the error sticky and returns it. Zero bytes go out with it on purpose: the caller is
// getting a verdict, not data, and a Read that hands back both invites a consumer to keep the tail
// of a file that just failed its digest.
func (r *archiveFileReader) fail(err error) error {
	r.err = err
	return err
}

func (r *archiveFileReader) Close() error { return r.rc.Close() }
