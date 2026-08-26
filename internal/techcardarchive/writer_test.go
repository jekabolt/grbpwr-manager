package techcardarchive

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// TestWriteArchive* — the export half, measured against the IMPORT half.
//
// The oracle in almost every case below is OpenArchive, three files away: an archive our own reader
// refuses is a failed export, and this is the cheapest place in the world to find that out. The
// round trip is therefore not a nicety — it is the acceptance criterion — and it is checked all the
// way down to the bytes: every index row is opened through OpenFileVerified, which re-hashes the
// body against both the digest in the index and the digest in the name.
//
// What a round trip ALONE cannot see is the second half of every case here: the counters the
// manifest claims (an export that wrote nothing and claimed nothing round-trips perfectly), the
// entry ORDER, and the four refusals that must happen before anything is streamed anywhere.
// ─────────────────────────────────────────────────────────────────────────────

// ── fixtures ─────────────────────────────────────────────────────────────────

var exportStamp = time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC)

// binaryFile builds the content-addressed entry of FORMAT.md §1.1 from real bytes: the name and the
// index digest are the sha256 of the body, exactly as the spool computes them.
func binaryFile(dir string, body []byte, ext string) BinaryFile {
	sum := shaHex(body)
	return BinaryFile{
		Name:   dir + sum + ext,
		SHA256: sum,
		Size:   int64(len(body)),
		Open:   func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil },
	}
}

func strPtr(s string) *string { return &s }

// fullInput is a card with something in every sidecar, including the two cases the counters exist
// to expose: TWO media ids sharing ONE file (dedup), and a hole from each of the two collectors.
func fullInput(t *testing.T) ArchiveInput {
	t.Helper()

	photo := binaryFile(DirMedia, []byte("jpeg bytes of the front flat"), ".jpg")
	sheet := binaryFile(DirPatterns, []byte("0\nSECTION\n2\nENTITIES\n"), ".dxf")
	marker := JSONFile{Name: DirMarkers + "m-1.json", Data: []byte(`{"summary":{"name":"shell 150"}}`)}

	return ArchiveInput{
		ExportedAt: exportStamp,
		ExportedBy: "im",
		Source: Source{
			Host:                  "backend.grbpwr.com",
			TechCardID:            214,
			StyleNumber:           "GRB-SS26-014",
			LockVersion:           37,
			ApprovalStateAtExport: "released",
			AppVersion:            "abc1234",
		},
		IDMaps: IDMaps{
			Sizes:        map[string]string{"3": "s", "4": "m", "5": "l"},
			CategoryPath: []string{"clothing", "outerwear", "jacket"},
			Colorways:    map[string]string{"812": "BLK"},
		},
		Holes: []ExportHole{
			{Entity: EntityMedia, Ref: "media_id=4021", Reason: ReasonMediaObjectMissing, Detail: "404 from bucket"},
			{Entity: EntityMaterial, Ref: "bom_line_key=01J8", Reason: ReasonMaterialNotFound, Detail: "deleted from catalogue"},
		},
		CardJSON: cardBytes(t),
		SizeChart: SizeChart{
			Cells:             []SizeChartCell{{SizeName: "m", Measurement: "chest", Value: "52"}},
			GradeBaseSizeName: "m",
			GradeSteps:        []SizeChartGradeStep{{Measurement: "chest", Step: "2"}},
		},
		Assembly:  []AssemblyLink{{ComponentStyleNumber: "GRB-AUX-0012", Qty: "1", Active: true}},
		Colorways: []ColorwayPayload{{ColorCode: "BLK", BaseSKU: "GRB-SS26-014-BLK"}},
		Materials: []MaterialPassport{{Ref: 8120, Code: "F-WOOL-320", Name: "wool melton 320 g", Unit: "m"}},
		// THREE media ids, ONE file: the same photograph in a sketch slot, a callout and a step.
		// The manifest must count THREE — the import's positive control compares its parsed ROWS
		// with this number, and counting files would make a correct import look like a broken one.
		//
		// Three and not two on purpose: with two rows the fixture would carry two media rows and
		// two files (photo + pattern sheet), the two readings of «contents» would print the same
		// number, and the assertion below would pass under either implementation. MEASURED — the
		// mutation «Media: len(in.Files)» was green until this row was added.
		Media: []MediaIndexEntry{
			{Ref: 4020, File: photo.Name, SHA256: photo.SHA256, Kind: "TECH_CARD_MEDIA_KIND_FRONT", Caption: "front flat", Width: 2400, Height: 3200},
			{Ref: 4022, File: photo.Name, SHA256: photo.SHA256},
			{Ref: 4023, File: photo.Name, SHA256: photo.SHA256},
		},
		Patterns: []PatternIndexEntry{
			{LineKey: "01J8ZC6T", File: sheet.Name, SHA256: sheet.SHA256, SizeName: nil, Version: 3, Name: "перед", Filename: "front_v3.dxf"},
		},
		Markers: []MarkerIndexEntry{
			{File: marker.Name, SizeName: strPtr("m"), MarkerName: "shell 150 cm", BomLineKey: "01J8ZC4Q"},
		},
		MarkerFiles: []JSONFile{marker},
		Files:       []BinaryFile{photo, sheet},
	}
}

func writeToBuffer(t *testing.T, in ArchiveInput) ([]byte, Manifest) {
	t.Helper()
	var buf bytes.Buffer
	m, err := WriteArchive(&buf, in)
	require.NoError(t, err)
	return buf.Bytes(), m
}

// entryNames lists the ZIP directory in the order it is stored — the order the writer chose, not a
// sorted view of it.
func entryNames(t *testing.T, raw []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err)
	out := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	return out
}

// ── the round trip ───────────────────────────────────────────────────────────

// TestWriteArchiveRoundTripsThroughOurOwnReader is the acceptance case: build an archive, open it
// with the reader the import runs, and check that everything the manifest claims is in there and
// hashes to what it says.
func TestWriteArchiveRoundTripsThroughOurOwnReader(t *testing.T) {
	in := fullInput(t)
	raw, written := writeToBuffer(t, in)

	a, err := OpenArchive(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err, "our own reader must accept what our own writer produced")

	// The identity fields are the writer's, never the caller's: ArchiveInput has nowhere to put
	// them, and the far end refuses an archive missing money_policy.
	require.Equal(t, FormatName, a.Manifest.Format)
	require.Equal(t, FormatVersion, a.Manifest.FormatVersion)
	require.Equal(t, MoneyPolicyStrippedV1, a.Manifest.MoneyPolicy)

	require.Equal(t, "im", a.Manifest.ExportedBy)
	require.True(t, exportStamp.Equal(a.Manifest.ExportedAt))
	require.Equal(t, in.Source, a.Manifest.Source)
	require.Equal(t, in.IDMaps.Sizes, a.Manifest.IDMaps.Sizes)
	require.Equal(t, in.IDMaps.CategoryPath, a.Manifest.IDMaps.CategoryPath)
	require.Equal(t, in.IDMaps.Colorways, a.Manifest.IDMaps.Colorways)

	// Both collectors' holes travel. Losing one half is the failure this asserts against: the
	// archive would look complete and would not be.
	require.Equal(t, in.Holes, a.Manifest.ExportHoles)

	// Counters are INDEX ROWS. Two media rows over one file is the case that separates the two
	// readings of "contents".
	require.Equal(t, Contents{Media: 3, Patterns: 1, Markers: 1, Materials: 1}, a.Manifest.Contents,
		"contents counts index rows, not files: media dedup must not shrink the claim")
	require.Len(t, in.Files, 2, "the fixture must actually dedup, or the assertion above proves nothing")
	require.NotEqual(t, len(in.Files), len(in.Media),
		"rows and files must differ here, or 'counts rows' and 'counts files' print the same number")

	// The manifest the writer RETURNED is the manifest it WROTE — the handler shows the operator
	// the returned copy and must not be shown a different archive from the one that shipped.
	require.Equal(t, *a.Manifest, written)

	require.Empty(t, a.UnknownEntries, "an export must not produce entries its own reader cannot name")

	// card.json parses as the proto message the far end expects.
	card, err := a.CardJSON()
	require.NoError(t, err)
	require.Equal(t, "GRB-SS26-014", card.GetTechCard().GetStyleNumber())

	// Every index row resolves to bytes that hash to what the index says — OpenFileVerified checks
	// the digest in the index AND the digest in the name.
	for _, e := range in.Media {
		body, err := a.ReadFileVerified(e.File, e.SHA256)
		require.NoError(t, err, "media entry %s", e.File)
		require.Equal(t, "jpeg bytes of the front flat", string(body))
	}
	for _, e := range in.Patterns {
		body, err := a.ReadFileVerified(e.File, e.SHA256)
		require.NoError(t, err, "pattern entry %s", e.File)
		require.Contains(t, string(body), "ENTITIES")
	}
	for _, e := range in.Markers {
		body, err := a.ReadFile(e.File)
		require.NoError(t, err, "marker entry %s", e.File)
		require.JSONEq(t, `{"summary":{"name":"shell 150"}}`, string(body))
	}

	// The sidecars come back as the same objects that went in.
	var chart SizeChart
	require.NoError(t, json.Unmarshal(mustRead(t, a, FileSizeChart), &chart))
	require.Equal(t, in.SizeChart, chart)

	var assembly []AssemblyLink
	require.NoError(t, json.Unmarshal(mustRead(t, a, FileAssembly), &assembly))
	require.Equal(t, in.Assembly, assembly)

	var colorways []ColorwayPayload
	require.NoError(t, json.Unmarshal(mustRead(t, a, FileColorways), &colorways))
	require.Equal(t, in.Colorways, colorways)

	var materials []MaterialPassport
	require.NoError(t, json.Unmarshal(mustRead(t, a, FileMaterialsIndex), &materials))
	require.Equal(t, in.Materials, materials)

	var media []MediaIndexEntry
	require.NoError(t, json.Unmarshal(mustRead(t, a, FileMediaIndex), &media))
	require.Equal(t, in.Media, media)

	var patterns []PatternIndexEntry
	require.NoError(t, json.Unmarshal(mustRead(t, a, FilePatternsIndex), &patterns))
	require.Equal(t, in.Patterns, patterns)

	var markers []MarkerIndexEntry
	require.NoError(t, json.Unmarshal(mustRead(t, a, FileMarkersIndex), &markers))
	require.Equal(t, in.Markers, markers)
}

func mustRead(t *testing.T, a *Archive, name string) []byte {
	t.Helper()
	b, err := a.ReadFile(name)
	require.NoError(t, err, "entry %s", name)
	return b
}

// TestWriteArchiveOrdersManifestFirstThenAlphabet pins the layout rule. Manifest first so a
// consumer streaming forward can decide whether to read the rest at all; the alphabet afterwards so
// "deterministic" is a rule a reviewer can check rather than a property of map iteration.
func TestWriteArchiveOrdersManifestFirstThenAlphabet(t *testing.T) {
	raw, _ := writeToBuffer(t, fullInput(t))
	names := entryNames(t, raw)

	require.Equal(t, FileManifest, names[0], "manifest.json is written first")

	rest := names[1:]
	sorted := append([]string(nil), rest...)
	sort.Strings(sorted)
	require.Equal(t, sorted, rest, "every entry after the manifest is in name order")

	// And the whole file is reproducible: the same input twice is the same bytes twice.
	again, _ := writeToBuffer(t, fullInput(t))
	require.Equal(t, raw, again, "an archive built twice from one input must be byte-identical")
}

// TestWriteArchiveOmitsEmptySidecars fixes the choice §1 leaves open — an empty index and an absent
// file are equally legal, so somebody had to pick. Picked: nothing empty is written, and
// sizechart.json is written even so, because a reader must never have to wonder whether the
// measurement grid was omitted or lost.
func TestWriteArchiveOmitsEmptySidecars(t *testing.T) {
	raw, m := writeToBuffer(t, ArchiveInput{
		ExportedAt: exportStamp,
		Source:     Source{StyleNumber: "GRB-SS26-014"},
		CardJSON:   cardBytes(t),
	})

	require.Equal(t, []string{FileManifest, FileCard, FileSizeChart}, entryNames(t, raw))
	require.Equal(t, Contents{}, m.Contents)

	a, err := OpenArchive(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err, "a card with nothing in it still exports a readable archive")
	require.Empty(t, a.UnknownEntries)

	// Empty collections travel as [] and {}, not null: the manifest is read by humans and by other
	// people's parsers, and `"export_holes": null` invites a question `[]` does not.
	rawManifest := string(a.ManifestRaw)
	require.Contains(t, rawManifest, `"export_holes": []`)
	require.Contains(t, rawManifest, `"sizes": {}`)
	require.Contains(t, rawManifest, `"category_path": []`)

	var chart SizeChart
	require.NoError(t, json.Unmarshal(mustRead(t, a, FileSizeChart), &chart))
	require.NotNil(t, chart.Cells)
	require.Empty(t, chart.Cells)
}

// ── refusals ─────────────────────────────────────────────────────────────────

// TestWriteArchiveRefusesWhatTheReaderWouldRefuse walks the ceilings of §1.3 from the writing side.
// Each of these produces an archive OpenArchive kills on the DIRECTORY pass — no hole, no reason
// code, no way back to the card — so each has to die here instead, while an operator is still
// looking at the card that caused it.
func TestWriteArchiveRefusesWhatTheReaderWouldRefuse(t *testing.T) {
	t.Run("marker file over its own ceiling", func(t *testing.T) {
		in := fullInput(t)
		big := JSONFile{Name: DirMarkers + "m-2.json", Data: bytes.Repeat([]byte("x"), MaxMarkerFileBytes+1)}
		in.MarkerFiles = append(in.MarkerFiles, big)
		in.Markers = append(in.Markers, MarkerIndexEntry{File: big.Name, MarkerName: "huge"})

		_, err := WriteArchive(io.Discard, in)
		require.ErrorIs(t, err, ErrArchiveTooLarge)
		require.Contains(t, err.Error(), big.Name)
	})

	t.Run("sidecar over the card.json ceiling", func(t *testing.T) {
		in := fullInput(t)
		in.Colorways = []ColorwayPayload{{ColorCode: "BLK", BaseSKU: strings.Repeat("s", MaxCardJSONBytes+1)}}

		_, err := WriteArchive(io.Discard, in)
		require.ErrorIs(t, err, ErrArchiveTooLarge)
		require.Contains(t, err.Error(), FileColorways)
	})

	t.Run("more uncompressed bytes than the whole-archive ceiling", func(t *testing.T) {
		in := fullInput(t)
		// Declared sizes only: the refusal has to happen BEFORE anything is opened, which is what
		// makes it useful — the alternative is discovering it with 600 MB already in the pipe.
		for i := 0; i < 2; i++ {
			name := fmt.Sprintf("%s%s.jpg", DirMedia, shaHex([]byte{byte(i)}))
			in.Files = append(in.Files, BinaryFile{
				Name:   name,
				SHA256: shaHex([]byte{byte(i)}),
				Size:   600 * 1024 * 1024,
				Open: func() (io.ReadCloser, error) {
					t.Error("the ceiling must be decided before a single file is opened")
					return nil, fmt.Errorf("must not be reached")
				},
			})
			in.Media = append(in.Media, MediaIndexEntry{Ref: int32(9000 + i), File: name, SHA256: shaHex([]byte{byte(i)})})
		}

		_, err := WriteArchive(io.Discard, in)
		require.ErrorIs(t, err, ErrArchiveTooLarge)
	})

	t.Run("more entries than the zip ceiling", func(t *testing.T) {
		in := fullInput(t)
		for i := 0; i < MaxZipEntries; i++ {
			name := fmt.Sprintf("%sm-%d.json", DirMarkers, 100+i)
			in.MarkerFiles = append(in.MarkerFiles, JSONFile{Name: name, Data: []byte("{}")})
			in.Markers = append(in.Markers, MarkerIndexEntry{File: name, MarkerName: "m"})
		}

		_, err := WriteArchive(io.Discard, in)
		require.ErrorIs(t, err, ErrArchiveTooLarge)
	})
}

// TestWriteArchiveRefusesSelfContradiction covers the seam between the two collectors and this
// writer — the join no single-half test touches, and the one two people building in parallel are
// each certain the other tested.
func TestWriteArchiveRefusesSelfContradiction(t *testing.T) {
	t.Run("an index names a file nobody supplied", func(t *testing.T) {
		in := fullInput(t)
		in.Media = append(in.Media, MediaIndexEntry{
			Ref: 4099, File: DirMedia + shaHex([]byte("never spooled")) + ".jpg", SHA256: shaHex([]byte("never spooled")),
		})

		_, err := WriteArchive(io.Discard, in)
		require.ErrorIs(t, err, ErrArchiveInconsistent)
		require.Contains(t, err.Error(), FileMediaIndex)
	})

	t.Run("a file no index names", func(t *testing.T) {
		in := fullInput(t)
		in.Files = append(in.Files, binaryFile(DirMedia, []byte("orphan bytes"), ".jpg"))

		_, err := WriteArchive(io.Discard, in)
		require.ErrorIs(t, err, ErrArchiveInconsistent)
	})

	t.Run("a marker index names a blob nobody supplied", func(t *testing.T) {
		in := fullInput(t)
		in.Markers = append(in.Markers, MarkerIndexEntry{File: DirMarkers + "m-9.json", MarkerName: "ghost"})

		_, err := WriteArchive(io.Discard, in)
		require.ErrorIs(t, err, ErrArchiveInconsistent)
		require.Contains(t, err.Error(), FileMarkersIndex)
	})

	t.Run("two entries under one name", func(t *testing.T) {
		in := fullInput(t)
		twin := in.MarkerFiles[0]
		twin.Data = []byte(`{"summary":{"name":"a different marker"}}`)
		in.MarkerFiles = append(in.MarkerFiles, twin)
		in.Markers = append(in.Markers, MarkerIndexEntry{File: twin.Name, MarkerName: "twin"})

		_, err := WriteArchive(io.Discard, in)
		require.ErrorIs(t, err, ErrArchiveInconsistent)
	})

	t.Run("an entry name the format forbids", func(t *testing.T) {
		in := fullInput(t)
		// A size name is typed by a person and reaches the marker slug; this is where that stops.
		bad := JSONFile{Name: DirMarkers + "../escape-1.json", Data: []byte("{}")}
		in.MarkerFiles = append(in.MarkerFiles, bad)
		in.Markers = append(in.Markers, MarkerIndexEntry{File: bad.Name, MarkerName: "escape"})

		_, err := WriteArchive(io.Discard, in)
		require.ErrorIs(t, err, ErrArchiveInconsistent)
		require.ErrorIs(t, err, ErrRefused, "the reader's own verdict travels with ours")
	})

	t.Run("no card.json", func(t *testing.T) {
		in := fullInput(t)
		in.CardJSON = nil

		_, err := WriteArchive(io.Discard, in)
		require.ErrorIs(t, err, ErrArchiveInconsistent)
		require.Contains(t, err.Error(), FileCard)
	})
}

// TestWriteArchiveVerifiesBytesAgainstTheirName is the one check that cannot be made before writing:
// the spooled copy was hashed minutes earlier, and the name is a promise about the bytes that
// actually go into the zip. A disagreement is CORRUPTION at the far end — the whole import dies,
// unfixably — so the export dies here instead, and the half-written stream must not be finishable
// into something that opens.
func TestWriteArchiveVerifiesBytesAgainstTheirName(t *testing.T) {
	t.Run("body does not hash to the name", func(t *testing.T) {
		in := fullInput(t)
		// EXACTLY as long as the file it impersonates, and different in every other way. Equal
		// length is the whole point: with a shorter body the length guard fires first, the test
		// goes green with the digest comparison deleted, and the sentinel stands over dead code.
		// MEASURED — the mutation «never compare the sums» was green until the lengths matched.
		const honest = "the bytes the index describes"
		const forged = "THE BYTES THE INDEX DESCRIBES"
		require.Len(t, forged, len(honest))
		liar := binaryFile(DirMedia, []byte(honest), ".jpg")
		liar.Open = func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(forged)), nil
		}
		in.Files = append(in.Files, liar)
		in.Media = append(in.Media, MediaIndexEntry{Ref: 4111, File: liar.Name, SHA256: liar.SHA256})

		var buf bytes.Buffer
		_, err := WriteArchive(&buf, in)
		require.ErrorIs(t, err, ErrArchiveInconsistent)

		// No central directory was appended, so nothing downstream can mistake the abandoned
		// stream for an archive. A writer that closed the zip on the error path would produce a
		// file that opens and is short.
		_, openErr := OpenArchive(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		require.Error(t, openErr, "an abandoned write must not be openable as an archive")
	})

	t.Run("body is not as long as it was spooled", func(t *testing.T) {
		in := fullInput(t)
		body := []byte("measured at nine hundred bytes, delivered short")
		shrunk := binaryFile(DirPatterns, body, ".dxf")
		shrunk.Size = int64(len(body)) + 900
		in.Files = append(in.Files, shrunk)
		in.Patterns = append(in.Patterns, PatternIndexEntry{LineKey: "k", File: shrunk.Name, SHA256: shrunk.SHA256})

		_, err := WriteArchive(io.Discard, in)
		require.ErrorIs(t, err, ErrArchiveInconsistent)
	})

	t.Run("the file cannot be opened at all", func(t *testing.T) {
		in := fullInput(t)
		gone := binaryFile(DirMedia, []byte("spooled and then removed"), ".png")
		gone.Open = func() (io.ReadCloser, error) { return nil, fmt.Errorf("no such file") }
		in.Files = append(in.Files, gone)
		in.Media = append(in.Media, MediaIndexEntry{Ref: 4222, File: gone.Name, SHA256: gone.SHA256})

		_, err := WriteArchive(io.Discard, in)
		require.Error(t, err)
		require.Contains(t, err.Error(), "no such file")
	})
}

// TestWriteArchiveStampsTimeAndSurvivesAZeroStamp: ExportedAt is both the manifest's stamp and every
// entry's modification time, and a caller that supplies none still gets a valid archive rather than
// entries dated 1979.
func TestWriteArchiveStampsTimeAndSurvivesAZeroStamp(t *testing.T) {
	in := fullInput(t)
	raw, _ := writeToBuffer(t, in)
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err)
	for _, f := range zr.File {
		require.True(t, exportStamp.Equal(f.Modified.UTC()), "entry %s carries the export stamp", f.Name)
	}

	before := time.Now().UTC().Add(-time.Second)
	in.ExportedAt = time.Time{}
	_, m := writeToBuffer(t, in)
	require.False(t, m.ExportedAt.IsZero())
	require.True(t, m.ExportedAt.After(before))
	require.Equal(t, time.UTC, m.ExportedAt.Location(), "the manifest stamp is UTC, not the server's zone")
}
