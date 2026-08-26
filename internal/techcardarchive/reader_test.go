package techcardarchive

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// ─────────────────────────────────────────────────────────────────────────────
// TestOpenArchive* — the gate: what gets in, what is refused, what is merely listed.
// TestArchiveIntegrity* — the streaming half: digests, declared sizes, ceilings.
//
// EVERY ARCHIVE HERE IS BUILT IN CODE, not committed as a fixture blob. A hostile input is only
// worth having if a reviewer can see what makes it hostile, and «media/index.json points at a
// digest the file does not hash to» is a sentence, while a .zip in testdata is 400 opaque bytes.
// The one thing a blob would buy — a fixture later phases reuse — they do not need: the cyclic test
// of Ф3.5 builds its archive with the real writer, which is the only fixture that cannot go stale.
// ─────────────────────────────────────────────────────────────────────────────

// ── fixture builders ─────────────────────────────────────────────────────────

type zipFile struct {
	name string
	body []byte
}

// buildZip writes an honest ZIP: real bodies, real CRCs, real sizes. Names are NOT validated by
// archive/zip, which is what lets the hostile-name cases below exist at all.
func buildZip(t *testing.T, files ...zipFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		require.NoError(t, err)
		_, err = w.Write(f.body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// rawZipEntry is an entry whose HEADER IS DICTATED rather than measured — the forging primitive for
// everything the reader must not take on faith. zip.Writer.CreateRaw copies UncompressedSize64,
// CompressedSize64 and CRC32 into the directory exactly as given, so an entry can claim to be ten
// bytes long, or nine hundred megabytes, or to have a CRC of zero, whatever its body actually is.
// That is the shape a zip bomb and a hand-made archive both have.
type rawZipEntry struct {
	name       string
	method     uint16
	flags      uint16
	body       []byte // already in `method` form: stored bytes, or a deflate stream
	declared   uint64 // UncompressedSize64 as the directory will state it
	compressed uint64 // CompressedSize64; 0 means «as long as body actually is»
	crc        uint32
}

func buildRawZip(t *testing.T, entries ...rawZipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		comp := e.compressed
		if comp == 0 {
			comp = uint64(len(e.body))
		}
		w, err := zw.CreateRaw(&zip.FileHeader{
			Name:               e.name,
			Method:             e.method,
			Flags:              e.flags,
			CRC32:              e.crc,
			UncompressedSize64: e.declared,
			CompressedSize64:   comp,
		})
		require.NoError(t, err)
		_, err = w.Write(e.body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func deflated(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.BestCompression)
	require.NoError(t, err)
	_, err = fw.Write(raw)
	require.NoError(t, err)
	require.NoError(t, fw.Close())
	return buf.Bytes()
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// manifestObject is a valid 1.0 manifest as a mutable JSON object, so a case can break exactly one
// field and nothing else.
func manifestObject() map[string]any {
	return map[string]any{
		"format":         FormatName,
		"format_version": FormatVersion,
		"exported_at":    "2026-08-25T14:00:00Z",
		"exported_by":    "im",
		"source": map[string]any{
			"host":                     "backend.grbpwr.com",
			"tech_card_id":             214,
			"style_number":             "GRB-SS26-014",
			"lock_version":             37,
			"approval_state_at_export": "released",
			"app_version":              "abc1234",
		},
		"money_policy": MoneyPolicyStrippedV1,
		"id_maps": map[string]any{
			"sizes":         map[string]string{"3": "s", "4": "m"},
			"category_path": []string{"clothing", "outerwear", "jacket"},
			"colorways":     map[string]string{"812": "BLK"},
		},
		"contents":     map[string]any{"media": 1, "patterns": 0, "markers": 0, "materials": 0},
		"export_holes": []any{},
	}
}

func jsonBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func cardBytes(t *testing.T) []byte {
	t.Helper()
	b, err := protojson.Marshal(&pb_common.TechCard{
		Id:       214,
		TechCard: &pb_common.TechCardInsert{StyleNumber: "GRB-SS26-014", Name: "jacket"},
	})
	require.NoError(t, err)
	return b
}

// mediaEntry returns the content-addressed entry FORMAT.md §1.1 describes: the file's own name is
// the sha256 of its body.
func mediaEntry(body []byte) zipFile {
	return zipFile{name: DirMedia + shaHex(body) + ".jpg", body: body}
}

var samplePhoto = []byte("not really a jpeg, but it hashes like anything else")

// minimalArchive is the smallest archive the format calls valid, plus one media file so the
// content-addressed path is exercised by the happy case too.
func minimalArchive(t *testing.T) []byte {
	t.Helper()
	photo := mediaEntry(samplePhoto)
	index := jsonBytes(t, []MediaIndexEntry{{
		Ref: 4021, File: photo.name, SHA256: shaHex(samplePhoto), Kind: "TECHNICAL", Width: 10, Height: 20,
	}})
	return buildZip(t,
		zipFile{FileManifest, jsonBytes(t, manifestObject())},
		zipFile{FileCard, cardBytes(t)},
		zipFile{FileMediaIndex, index},
		photo,
	)
}

func openBytes(b []byte) (*Archive, error) {
	return OpenArchive(bytes.NewReader(b), int64(len(b)))
}

func mustOpen(t *testing.T, b []byte) *Archive {
	t.Helper()
	a, err := openBytes(b)
	require.NoError(t, err)
	return a
}

// ── the gate ─────────────────────────────────────────────────────────────────

func TestOpenArchiveHappyPath(t *testing.T) {
	a := mustOpen(t, minimalArchive(t))

	require.Equal(t, FormatName, a.Manifest.Format)
	require.Equal(t, MoneyPolicyStrippedV1, a.Manifest.MoneyPolicy)
	require.Equal(t, "GRB-SS26-014", a.Manifest.Source.StyleNumber)
	require.Equal(t, map[string]string{"3": "s", "4": "m"}, a.Manifest.IDMaps.Sizes)
	require.Equal(t, 1, a.Manifest.Contents.Media)
	require.Empty(t, a.UnknownEntries)

	require.True(t, a.Has(FileCard))
	require.False(t, a.Has(FileSizeChart))

	// The media body comes back whole, and comes back verified: the name encoded its digest.
	got, err := a.ReadFile(DirMedia + shaHex(samplePhoto) + ".jpg")
	require.NoError(t, err)
	require.Equal(t, samplePhoto, got)

	card, err := a.CardJSON()
	require.NoError(t, err)
	require.Equal(t, "GRB-SS26-014", card.GetTechCard().GetStyleNumber())
}

// A missing entry is the ONE non-fatal error: an index row naming a file the archive does not carry
// is a hole in the report, not the end of the import. IsFatal is the line that says so.
func TestOpenArchiveMissingEntryIsNotFatal(t *testing.T) {
	a := mustOpen(t, minimalArchive(t))

	_, err := a.ReadFile(FileSizeChart)
	require.ErrorIs(t, err, ErrNotFound)
	require.False(t, IsFatal(err))

	_, err = a.ReadFile(FileCard + "/../" + FileCard)
	require.ErrorIs(t, err, ErrNotFound)

	require.True(t, IsFatal(fmt.Errorf("%w: x", ErrCorrupt)))
	require.True(t, IsFatal(fmt.Errorf("%w: x", ErrRefused)))
	// Fails closed: an error class nobody has heard of aborts rather than importing half a card.
	require.True(t, IsFatal(errors.New("something new")))
	require.False(t, IsFatal(nil))
}

// The mandatory pair of FORMAT.md §1. A ZIP without them is not one of ours that lost something.
func TestOpenArchiveMandatoryEntries(t *testing.T) {
	t.Run("no manifest", func(t *testing.T) {
		_, err := openBytes(buildZip(t, zipFile{FileCard, cardBytes(t)}))
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), FileManifest)
	})
	t.Run("no card", func(t *testing.T) {
		_, err := openBytes(buildZip(t, zipFile{FileManifest, jsonBytes(t, manifestObject())}))
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), FileCard)
	})
	t.Run("case is not folded", func(t *testing.T) {
		// "Manifest.json" is a different string, so the mandatory manifest is simply absent.
		_, err := openBytes(buildZip(t,
			zipFile{"Manifest.json", jsonBytes(t, manifestObject())},
			zipFile{FileCard, cardBytes(t)},
		))
		require.ErrorIs(t, err, ErrRefused)
	})
	t.Run("not a zip at all", func(t *testing.T) {
		body := []byte("PK\x03\x04 this is a lie, and the rest is prose")
		_, err := openBytes(body)
		require.ErrorIs(t, err, ErrCorrupt)
	})
	t.Run("empty body", func(t *testing.T) {
		_, err := OpenArchive(bytes.NewReader(nil), 0)
		require.ErrorIs(t, err, ErrCorrupt)
	})
	t.Run("nil reader", func(t *testing.T) {
		_, err := OpenArchive(nil, 10)
		require.ErrorIs(t, err, ErrRefused)
	})
}

// The manifest contract of FORMAT.md §3 and §4 — the three whole-archive refusals that are not
// corruption.
func TestOpenArchiveManifestContract(t *testing.T) {
	withManifest := func(t *testing.T, mutate func(m map[string]any)) error {
		t.Helper()
		m := manifestObject()
		mutate(m)
		_, err := openBytes(buildZip(t,
			zipFile{FileManifest, jsonBytes(t, m)},
			zipFile{FileCard, cardBytes(t)},
		))
		return err
	}

	t.Run("money_policy absent", func(t *testing.T) {
		// The case the flag exists for: a hand-made bundle with costing in it says nothing about
		// money, and «says nothing» must not read as «fine».
		err := withManifest(t, func(m map[string]any) { delete(m, "money_policy") })
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), "money_policy")
	})
	t.Run("money_policy some other word", func(t *testing.T) {
		err := withManifest(t, func(m map[string]any) { m["money_policy"] = "stripped-v2" })
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), "stripped-v2")
	})
	t.Run("foreign format name", func(t *testing.T) {
		err := withManifest(t, func(m map[string]any) { m["format"] = "some-other-tool-archive" })
		require.ErrorIs(t, err, ErrRefused)
	})
	t.Run("MAJOR 2 is newer", func(t *testing.T) {
		err := withManifest(t, func(m map[string]any) { m["format_version"] = "2.0" })
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), "newer than")
	})
	t.Run("MAJOR 0 is older", func(t *testing.T) {
		err := withManifest(t, func(m map[string]any) { m["format_version"] = "0.9" })
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), "older than")
	})
	t.Run("a newer MINOR of our MAJOR is read", func(t *testing.T) {
		require.NoError(t, withManifest(t, func(m map[string]any) { m["format_version"] = "1.9" }))
	})
	t.Run("manifest is not JSON", func(t *testing.T) {
		_, err := openBytes(buildZip(t,
			zipFile{FileManifest, []byte("{not json")},
			zipFile{FileCard, cardBytes(t)},
		))
		require.ErrorIs(t, err, ErrCorrupt)
	})
}

// The half of the version rule that the review flagged: a 1.x manifest carries fields this struct
// has no member for, encoding/json drops them without a word, and a re-marshal of Manifest would
// write a manifest MISSING them — under the label «what was in the ZIP at upload». ManifestRaw is
// the only copy that stays true, and it is what the import row must store.
func TestOpenArchiveManifestRawKeepsUnknownFields(t *testing.T) {
	m := manifestObject()
	m["format_version"] = "1.4"
	m["export_environment"] = "a field 1.4 added and 1.0 never heard of"
	m["contents"] = map[string]any{"media": 0, "patterns": 0, "markers": 0, "materials": 0, "swatches": 7}
	raw := jsonBytes(t, m)

	a := mustOpen(t, buildZip(t,
		zipFile{FileManifest, raw},
		zipFile{FileCard, cardBytes(t)},
	))

	require.Equal(t, raw, a.ManifestRaw, "ManifestRaw must be the bytes of manifest.json verbatim")
	require.Contains(t, string(a.ManifestRaw), "export_environment")
	require.Contains(t, string(a.ManifestRaw), "swatches")

	// And the proof that raw bytes are not a nicety: the round trip through the 1.0 struct silently
	// loses both fields. Storing THAT as «the manifest as uploaded» would be a quiet, permanent lie.
	roundTripped, err := json.Marshal(a.Manifest)
	require.NoError(t, err)
	require.NotContains(t, string(roundTripped), "export_environment")
	require.NotContains(t, string(roundTripped), "swatches")
}

func TestOpenArchiveFormatVersion(t *testing.T) {
	for _, tc := range []struct {
		in           string
		major, minor int
	}{
		{"1.0", 1, 0},
		{"1.14", 1, 14},
		{"0.1", 0, 1},
		{"01.0", 1, 0}, // a leading zero is a formatting habit, not another version
		{"1.00", 1, 0}, // ditto
		{"12.34", 12, 34},
	} {
		major, minor, err := ParseFormatVersion(tc.in)
		require.NoError(t, err, tc.in)
		require.Equal(t, tc.major, major, tc.in)
		require.Equal(t, tc.minor, minor, tc.in)
	}

	for _, bad := range []string{
		"", "1", "1.", ".0", "1.0.0", "v1.0", " 1.0", "1.0 ", "1.-0", "+1.0", "1.0\n",
		"one.zero", "1,0", "9999999999.0", "1.0e1", "-1.0",
	} {
		_, _, err := ParseFormatVersion(bad)
		require.ErrorIs(t, err, ErrRefused, "%q must not parse", bad)
	}
}

// FORMAT.md §1.1, last line. A forbidden name refuses the ARCHIVE rather than landing in
// UnknownEntries: unknown_entry is the escape hatch for a file a newer MINOR added, and "../" is not
// a newer file. The reader hands entry names onward — to report lines, and to the phase that turns
// pattern and media names into bucket object keys — so a traversal name that got as far as
// UnknownEntries would be a traversal name exported to every consumer downstream.
func TestOpenArchiveHostileNames(t *testing.T) {
	for _, name := range []string{
		"../escape.json",
		"media/../../etc/passwd",
		"a/../../b.json",
		"..",
		"/etc/passwd",
		"/manifest.json",
		`media\evil.jpg`,
		`..\..\windows\system32`,
		"media/\x00hidden.jpg",
		"media/bell\x07.jpg",
		"media/new\nline.jpg",
	} {
		t.Run(strings.ReplaceAll(name, "\x00", "NUL"), func(t *testing.T) {
			_, err := openBytes(buildZip(t,
				zipFile{FileManifest, jsonBytes(t, manifestObject())},
				zipFile{FileCard, cardBytes(t)},
				zipFile{name, []byte("payload")},
			))
			require.ErrorIs(t, err, ErrRefused)
		})
	}

	t.Run("invalid utf-8", func(t *testing.T) {
		_, err := openBytes(buildZip(t,
			zipFile{FileManifest, jsonBytes(t, manifestObject())},
			zipFile{FileCard, cardBytes(t)},
			zipFile{"media/\xff\xfe.jpg", []byte("payload")},
		))
		require.ErrorIs(t, err, ErrRefused)
	})

	t.Run("empty name", func(t *testing.T) {
		_, err := openBytes(buildRawZip(t, rawZipEntry{name: "", method: zip.Store}))
		require.ErrorIs(t, err, ErrRefused)
	})

	// Two entries under one name is a smuggling shape: whoever verifies reads one copy and whoever
	// consumes reads the other.
	t.Run("duplicate names", func(t *testing.T) {
		_, err := openBytes(buildZip(t,
			zipFile{FileManifest, jsonBytes(t, manifestObject())},
			zipFile{FileCard, cardBytes(t)},
			zipFile{FileCard, []byte(`{"techCard":{"styleNumber":"SMUGGLED"}}`)},
		))
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), "twice")
	})
}

// An entry this process cannot honestly read is refused BEFORE the import starts work, not
// discovered halfway through it with a half-resolved card on the table.
func TestOpenArchiveUnreadableEntries(t *testing.T) {
	base := []rawZipEntry{
		{name: FileManifest, method: zip.Store, body: jsonBytes(t, manifestObject()),
			declared: uint64(len(jsonBytes(t, manifestObject())))},
		{name: FileCard, method: zip.Store, body: cardBytes(t), declared: uint64(len(cardBytes(t)))},
	}

	t.Run("encrypted entry", func(t *testing.T) {
		entries := append(append([]rawZipEntry{}, base...), rawZipEntry{
			name: DirMedia + shaHex(samplePhoto) + ".jpg", method: zip.Store,
			flags: 0x1, body: samplePhoto, declared: uint64(len(samplePhoto)),
		})
		_, err := openBytes(buildRawZip(t, entries...))
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), "encrypted")
	})

	t.Run("unsupported compression method", func(t *testing.T) {
		const methodZstd = 93
		entries := append(append([]rawZipEntry{}, base...), rawZipEntry{
			name: FileSizeChart, method: methodZstd, body: []byte("zstd stream"),
			declared: 11,
		})
		_, err := openBytes(buildRawZip(t, entries...))
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), "93")
	})
}

// The zip-bomb pair. Neither cap works alone: a cap on bytes is defeated by a million empty
// entries, a cap on entries by one entry that inflates to a terabyte.
func TestOpenArchiveLimits(t *testing.T) {
	t.Run("too many entries", func(t *testing.T) {
		files := []zipFile{
			{FileManifest, jsonBytes(t, manifestObject())},
			{FileCard, cardBytes(t)},
		}
		for i := 0; i < 5000; i++ {
			files = append(files, zipFile{fmt.Sprintf("markers/mixed-%d.json", i), []byte("{}")})
		}
		_, err := openBytes(buildZip(t, files...))
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), fmt.Sprint(MaxZipEntries))
	})

	t.Run("exactly at the entry ceiling is fine", func(t *testing.T) {
		files := []zipFile{
			{FileManifest, jsonBytes(t, manifestObject())},
			{FileCard, cardBytes(t)},
		}
		for i := len(files); i < MaxZipEntries; i++ {
			files = append(files, zipFile{fmt.Sprintf("markers/mixed-%d.json", i), []byte("{}")})
		}
		a := mustOpen(t, buildZip(t, files...))
		require.Empty(t, a.UnknownEntries)
	})

	// Declared sizes, not real ones: a directory that CLAIMS a gigabyte has to be refused before
	// anything is decompressed, which is the only moment at which refusing is cheap.
	t.Run("declared uncompressed total over the ceiling", func(t *testing.T) {
		half := uint64(MaxUncompressedBytes/2) + 1
		_, err := openBytes(buildRawZip(t,
			rawZipEntry{name: FileManifest, method: zip.Store, body: jsonBytes(t, manifestObject()),
				declared: uint64(len(jsonBytes(t, manifestObject())))},
			rawZipEntry{name: FileCard, method: zip.Store, body: cardBytes(t), declared: uint64(len(cardBytes(t)))},
			rawZipEntry{name: DirMedia + strings.Repeat("a", 64) + ".jpg", method: zip.Store,
				body: []byte("x"), declared: half},
			rawZipEntry{name: DirMedia + strings.Repeat("b", 64) + ".jpg", method: zip.Store,
				body: []byte("x"), declared: half},
		))
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), "uncompressed bytes in total")
	})

	t.Run("one entry declaring more than the whole archive may hold", func(t *testing.T) {
		_, err := openBytes(buildRawZip(t,
			rawZipEntry{name: FileManifest, method: zip.Store, body: jsonBytes(t, manifestObject()),
				declared: uint64(len(jsonBytes(t, manifestObject())))},
			rawZipEntry{name: FileCard, method: zip.Store, body: cardBytes(t), declared: uint64(len(cardBytes(t)))},
			rawZipEntry{name: DirPatterns + strings.Repeat("c", 64) + ".dxf", method: zip.Store,
				body: []byte("x"), declared: uint64(MaxUncompressedBytes) + 1},
		))
		require.ErrorIs(t, err, ErrRefused)
	})

	// The two ceilings §1.3 names by entry, checked on the DECLARED size so an oversized body dies
	// before it is decompressed.
	t.Run("card.json over its ceiling", func(t *testing.T) {
		_, err := openBytes(buildRawZip(t,
			rawZipEntry{name: FileManifest, method: zip.Store, body: jsonBytes(t, manifestObject()),
				declared: uint64(len(jsonBytes(t, manifestObject())))},
			rawZipEntry{name: FileCard, method: zip.Store, body: cardBytes(t),
				declared: uint64(MaxCardJSONBytes) + 1},
		))
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), FileCard)
	})

	t.Run("marker blob over its ceiling", func(t *testing.T) {
		_, err := openBytes(buildRawZip(t,
			rawZipEntry{name: FileManifest, method: zip.Store, body: jsonBytes(t, manifestObject()),
				declared: uint64(len(jsonBytes(t, manifestObject())))},
			rawZipEntry{name: FileCard, method: zip.Store, body: cardBytes(t), declared: uint64(len(cardBytes(t)))},
			rawZipEntry{name: DirMarkers + "mixed-1.json", method: zip.Store, body: []byte("{}"),
				declared: uint64(MaxMarkerFileBytes) + 1},
		))
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), "mixed-1.json")
	})

	// MaxUploadedArchiveBytes belongs to the HTTP route and must NOT be applied here: an export of a
	// media-heavy card is legitimately larger than the body a browser may POST, and reading one back
	// out of the bucket is a normal thing to do.
	t.Run("the upload ceiling is not the reader's", func(t *testing.T) {
		body := minimalArchive(t)
		a, err := OpenArchive(bytes.NewReader(body), int64(len(body)))
		require.NoError(t, err)
		require.NotNil(t, a.Manifest)
		require.Less(t, int64(len(body)), int64(MaxUploadedArchiveBytes))
	})
}

// The MINOR rule of §3 executed rather than described: an unknown file is neither an error nor a
// silence — it is listed, and the report turns each one into an unknown_entry line.
func TestOpenArchiveUnknownEntries(t *testing.T) {
	photo := mediaEntry(samplePhoto)
	a := mustOpen(t, buildZip(t,
		zipFile{FileManifest, jsonBytes(t, manifestObject())},
		zipFile{FileCard, cardBytes(t)},
		photo,
		zipFile{"swatches/index.json", []byte("[]")}, // a whole directory 1.4 added
		zipFile{DirMedia + shaHex(samplePhoto) + ".avif", []byte("avif")}, // an extension §1.1 does not list
		zipFile{DirMedia + "not-a-digest.jpg", []byte("x")},               // right shape, wrong name
		zipFile{"README.txt", []byte("hello")},
		zipFile{DirMarkers + "index.json.bak", []byte("{}")},
	))

	require.Equal(t, []string{
		"swatches/index.json",
		DirMedia + shaHex(samplePhoto) + ".avif",
		DirMedia + "not-a-digest.jpg",
		"README.txt",
		DirMarkers + "index.json.bak",
	}, a.UnknownEntries)

	// Listed is not withheld. Classification decides what a name IS, never whether its bytes may be
	// had — refusing to serve a file an index points at would turn a newer MINOR into a broken
	// import, which is exactly what §3 forbids.
	got, err := a.ReadFile("README.txt")
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), got)

	// And an unknown media file still verifies against the digest its index row carries.
	_, err = a.ReadFileVerified(DirMedia+shaHex(samplePhoto)+".avif", shaHex([]byte("avif")))
	require.NoError(t, err)
	_, err = a.ReadFileVerified(DirMedia+shaHex(samplePhoto)+".avif", shaHex([]byte("something else")))
	require.ErrorIs(t, err, ErrCorrupt)
}

// Every name FORMAT.md §1 defines has to be recognised, or the report fills with unknown_entry lines
// about files the export itself wrote.
func TestOpenArchiveKnownNamesAreRecognised(t *testing.T) {
	digest := strings.Repeat("0123456789abcdef", 4)
	for _, name := range []string{
		FileManifest, FileCard, FileSizeChart, FileAssembly, FileColorways,
		FileMaterialsIndex, FileMediaIndex, FilePatternsIndex, FileMarkersIndex,
		DirMedia + digest + ".jpg", DirMedia + digest + ".png",
		DirMedia + digest + ".webp", DirMedia + digest + ".mp4",
		DirMedia + digest + ".jpeg", DirMedia + digest + ".gif", DirMedia + digest + ".webm",
		DirPatterns + digest + ".dxf", DirPatterns + digest + ".pdf",
		DirMarkers + "mixed-1.json", DirMarkers + "s-12.json", DirMarkers + "3xl-1.json",
		DirMarkers + "one-size-fits-all-4.json",
	} {
		require.True(t, classifyArchiveEntry(name).known, "%q must be a known name", name)
	}

	for _, name := range []string{
		"card.JSON", "cards.json", "media/index.json.gz",
		DirMedia + strings.ToUpper(digest) + ".jpg", // §1.1 says lower-case hex
		DirMedia + digest + ".avif",                 // never reaches the bucket: not in mimeTypeToFileExtension
		DirMedia + digest[:63] + ".jpg",
		DirMedia + digest + ".jpg.exe",
		DirPatterns + digest + ".jpg",
		DirMarkers + "mixed.json", DirMarkers + "mixed-.json", DirMarkers + "-1.json",
		DirMarkers + "MIXED-1.json", DirMarkers + "mixed-1.txt",
	} {
		require.False(t, classifyArchiveEntry(name).known, "%q must not be a known name", name)
	}
}

// FORMAT.md §1.1: directory entries are not written. One that turns up carries no bytes, so it is
// neither served nor put in the operator's report — but it still counts against both ceilings,
// because a hostile ZIP need not be honest about what a trailing slash means.
func TestOpenArchiveDirectoryEntriesAreIgnored(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{DirMedia, DirPatterns, "swatches/"} {
		_, err := zw.Create(name)
		require.NoError(t, err)
	}
	for _, f := range []zipFile{{FileManifest, jsonBytes(t, manifestObject())}, {FileCard, cardBytes(t)}} {
		w, err := zw.Create(f.name)
		require.NoError(t, err)
		_, err = w.Write(f.body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	a := mustOpen(t, buf.Bytes())
	require.Empty(t, a.UnknownEntries)
	require.False(t, a.Has(DirMedia))
	_, err := a.ReadFile(DirMedia)
	require.ErrorIs(t, err, ErrNotFound)
}

// ── integrity ────────────────────────────────────────────────────────────────

// FORMAT.md §1.2: a digest that disagrees is corruption and fails the whole import, never a hole.
func TestArchiveIntegrityDigest(t *testing.T) {
	// The file's name asserts one digest; its body hashes to another. This is also the case
	// archive/zip cannot catch on its own — see the CRC subtest below.
	lying := zipFile{name: DirMedia + shaHex([]byte("the bytes we promised")) + ".jpg", body: []byte("the bytes we shipped")}
	a := mustOpen(t, buildZip(t,
		zipFile{FileManifest, jsonBytes(t, manifestObject())},
		zipFile{FileCard, cardBytes(t)},
		lying,
	))

	t.Run("the name's digest is checked even when the caller brings none", func(t *testing.T) {
		_, err := a.ReadFile(lying.name)
		require.ErrorIs(t, err, ErrCorrupt)
		require.True(t, IsFatal(err))
	})

	t.Run("the index's digest is checked", func(t *testing.T) {
		honest := mediaEntry(samplePhoto)
		arc := mustOpen(t, buildZip(t,
			zipFile{FileManifest, jsonBytes(t, manifestObject())},
			zipFile{FileCard, cardBytes(t)},
			honest,
		))
		_, err := arc.ReadFileVerified(honest.name, shaHex([]byte("a different photo")))
		require.ErrorIs(t, err, ErrCorrupt)

		ok, err := arc.ReadFileVerified(honest.name, shaHex(samplePhoto))
		require.NoError(t, err)
		require.Equal(t, samplePhoto, ok)

		// Upper case is a spelling of the same digest, not a different one.
		ok, err = arc.ReadFileVerified(honest.name, strings.ToUpper(shaHex(samplePhoto)))
		require.NoError(t, err)
		require.Equal(t, samplePhoto, ok)
	})

	t.Run("index and name disagreeing is corruption before a byte is read", func(t *testing.T) {
		honest := mediaEntry(samplePhoto)
		arc := mustOpen(t, buildZip(t,
			zipFile{FileManifest, jsonBytes(t, manifestObject())},
			zipFile{FileCard, cardBytes(t)},
			honest,
		))
		_, err := arc.OpenFileVerified(honest.name, shaHex([]byte("elsewhere")))
		require.ErrorIs(t, err, ErrCorrupt)
		require.Contains(t, err.Error(), "its own name says")
	})

	t.Run("a caller's digest that is not a digest", func(t *testing.T) {
		_, err := a.OpenFileVerified(lying.name, "not-a-sha")
		require.ErrorIs(t, err, ErrCorrupt)
	})

	// archive/zip skips its CRC check entirely when the header's CRC32 is zero, taking it as
	// «unset» — so a hand-made archive gets past the standard library for free, and the sha256 is
	// the only thing standing between it and the import.
	t.Run("zero CRC gets past archive/zip and not past the digest", func(t *testing.T) {
		body := []byte("tampered")
		name := DirMedia + shaHex([]byte("original")) + ".jpg"
		arc := mustOpen(t, buildRawZip(t,
			rawZipEntry{name: FileManifest, method: zip.Store, body: jsonBytes(t, manifestObject()),
				declared: uint64(len(jsonBytes(t, manifestObject())))},
			rawZipEntry{name: FileCard, method: zip.Store, body: cardBytes(t), declared: uint64(len(cardBytes(t)))},
			rawZipEntry{name: name, method: zip.Store, body: body, declared: uint64(len(body)), crc: 0},
		))

		// The proof that the standard library really is asleep here: the same entry read through
		// archive/zip alone comes back clean.
		zr, err := zip.NewReader(bytes.NewReader(buildRawZip(t,
			rawZipEntry{name: name, method: zip.Store, body: body, declared: uint64(len(body)), crc: 0},
		)), int64(len(buildRawZip(t,
			rawZipEntry{name: name, method: zip.Store, body: body, declared: uint64(len(body)), crc: 0},
		))))
		require.NoError(t, err)
		rc, err := zr.File[0].Open()
		require.NoError(t, err)
		plain, err := io.ReadAll(rc)
		require.NoError(t, err, "archive/zip accepts a zero CRC without complaint")
		require.Equal(t, body, plain)
		require.NoError(t, rc.Close())

		_, err = arc.ReadFile(name)
		require.ErrorIs(t, err, ErrCorrupt)
	})
}

// A body that is not as long as the directory says it is: the archive does not agree with itself.
func TestArchiveIntegrityDeclaredSize(t *testing.T) {
	manifest := jsonBytes(t, manifestObject())

	t.Run("delivers fewer bytes than declared", func(t *testing.T) {
		body := []byte("short")
		name := DirPatterns + shaHex(body) + ".dxf"
		a := mustOpen(t, buildRawZip(t,
			rawZipEntry{name: FileManifest, method: zip.Store, body: manifest, declared: uint64(len(manifest))},
			rawZipEntry{name: FileCard, method: zip.Store, body: cardBytes(t), declared: uint64(len(cardBytes(t)))},
			rawZipEntry{name: name, method: zip.Store, body: body, declared: 4096},
		))
		_, err := a.ReadFile(name)
		require.ErrorIs(t, err, ErrCorrupt)
	})

	// The bomb: a directory entry claiming ten bytes over a deflate stream that inflates to
	// megabytes. Whichever guard fires — archive/zip's inner one or ours — the verdict must be
	// corruption and the caller must not receive the flood.
	t.Run("delivers more bytes than declared", func(t *testing.T) {
		payload := bytes.Repeat([]byte("A"), 4<<20)
		stream := deflated(t, payload)
		require.Less(t, len(stream), 64<<10, "the point of the case is that a small entry inflates")

		name := DirPatterns + shaHex(payload) + ".dxf"
		a := mustOpen(t, buildRawZip(t,
			rawZipEntry{name: FileManifest, method: zip.Store, body: manifest, declared: uint64(len(manifest))},
			rawZipEntry{name: FileCard, method: zip.Store, body: cardBytes(t), declared: uint64(len(cardBytes(t)))},
			rawZipEntry{name: name, method: zip.Deflate, body: stream, declared: 10},
		))
		got, err := a.ReadFile(name)
		require.ErrorIs(t, err, ErrCorrupt)
		require.Less(t, len(got), len(payload))
	})
}

// The streaming guards, tested on the type itself rather than through a ZIP.
//
// Two of them cannot be reached through archive/zip at all — its own body reader trips first — and a
// guard whose only test goes green because SOMEBODY ELSE'S check fired is a guard nobody is testing.
// These are the contract of archiveFileReader: never more bytes than declared, never fewer, never a
// digest that disagrees, and never a stream that resumes after saying no.
func TestArchiveIntegrityReaderGuards(t *testing.T) {
	newReader := func(body []byte, declared int64, want string) *archiveFileReader {
		r := &archiveFileReader{
			rc:   io.NopCloser(bytes.NewReader(body)),
			name: "media/x.jpg",
			size: declared,
			want: want,
		}
		if want != "" {
			r.h = sha256.New()
		}
		return r
	}

	t.Run("more bytes than declared", func(t *testing.T) {
		r := newReader(bytes.Repeat([]byte("z"), 1000), 10, "")
		_, err := io.ReadAll(r)
		require.ErrorIs(t, err, ErrCorrupt)
		require.Contains(t, err.Error(), "delivers more")
	})

	t.Run("fewer bytes than declared", func(t *testing.T) {
		r := newReader([]byte("four"), 4096, "")
		_, err := io.ReadAll(r)
		require.ErrorIs(t, err, ErrCorrupt)
		require.Contains(t, err.Error(), "delivers 4")
	})

	t.Run("the digest is what says no at EOF", func(t *testing.T) {
		body := []byte("payload")
		r := newReader(body, int64(len(body)), shaHex([]byte("other payload")))
		_, err := io.ReadAll(r)
		require.ErrorIs(t, err, ErrCorrupt)
		require.Contains(t, err.Error(), "hashes to")
	})

	t.Run("an empty entry still hashes", func(t *testing.T) {
		r := newReader(nil, 0, shaHex(nil))
		out, err := io.ReadAll(r)
		require.NoError(t, err)
		require.Empty(t, out)
	})

	t.Run("the error is sticky", func(t *testing.T) {
		r := newReader(bytes.Repeat([]byte("z"), 1000), 10, "")
		_, first := io.ReadAll(r)
		require.ErrorIs(t, first, ErrCorrupt)
		n, second := r.Read(make([]byte, 8))
		require.Zero(t, n, "a stream that failed verification hands out no more bytes")
		require.ErrorIs(t, second, ErrCorrupt)
	})

	// VERIFICATION IS FINAL ONLY AT EOF, and that is a property callers have to know about rather
	// than a bug to paper over: a consumer streaming these bytes into a bucket has already written
	// most of them when the digest fails, so it must not COMMIT until Read returns io.EOF.
	t.Run("a stream abandoned early verifies nothing", func(t *testing.T) {
		body := []byte("the first bytes are innocent, the rest are not")
		r := newReader(body, int64(len(body)), shaHex([]byte("something else entirely")))
		head := make([]byte, 8)
		n, err := r.Read(head)
		require.NoError(t, err, "the mismatch is not knowable yet")
		require.Equal(t, 8, n)
		require.NoError(t, r.Close())
	})
}

// card.json is read with DiscardUnknown — the MINOR rule of §3, and the same mode release snapshots
// and marker blobs are already read with in this codebase.
func TestArchiveIntegrityCardJSON(t *testing.T) {
	t.Run("unknown fields are discarded, not refused", func(t *testing.T) {
		card := `{"id":214,"techCard":{"styleNumber":"GRB-SS26-014"},"somethingV14AddedLater":{"a":1}}`
		a := mustOpen(t, buildZip(t,
			zipFile{FileManifest, jsonBytes(t, manifestObject())},
			zipFile{FileCard, []byte(card)},
		))
		got, err := a.CardJSON()
		require.NoError(t, err)
		require.Equal(t, "GRB-SS26-014", got.GetTechCard().GetStyleNumber())
	})

	t.Run("card.json that will not parse is corruption", func(t *testing.T) {
		a := mustOpen(t, buildZip(t,
			zipFile{FileManifest, jsonBytes(t, manifestObject())},
			zipFile{FileCard, []byte(`{"techCard": "this is not a message"}`)},
		))
		_, err := a.CardJSON()
		require.ErrorIs(t, err, ErrCorrupt)
	})

	t.Run("each call hands back its own message", func(t *testing.T) {
		a := mustOpen(t, minimalArchive(t))
		first, err := a.CardJSON()
		require.NoError(t, err)
		second, err := a.CardJSON()
		require.NoError(t, err)
		first.GetTechCard().StyleNumber = "MUTATED"
		require.Equal(t, "GRB-SS26-014", second.GetTechCard().GetStyleNumber())
	})
}
