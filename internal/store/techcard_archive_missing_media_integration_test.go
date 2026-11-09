package store

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/jpk"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestTechCardArchiveImportDropsMissingMedia: a picture the archive cannot deliver is DROPPED, never
// fatal and never left as a dangling reference. The card imports, the slot (or the size chart
// picture) is gone, and the report carries one skipped line naming the source media id.
//
// Four ways a picture goes missing:
//   - the file is absent from the zip while media/index.json still lists it;
//   - neither the index entry nor the file is there (only card.json names the id);
//   - the size chart picture points at media whose file is absent;
//   - the picture matched a row this base already held, and that row was deleted between the commit's resolve
//     and the card's transaction (vanished mid-import).
func TestTechCardArchiveImportDropsMissingMedia(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	rig := newRTRig(t, ctx)
	// The rig's bucket behind a hook, so a test can act inside the commit's upload phase.
	hooked := &mmHookedBucket{FileStore: newRTBucket(t, rig.store.Media(), rig.objs)}
	srv, err := admin.New(rig.store, hooked, nil, nil, nil, nil, nil, nil, nil, nil,
		entity.LabelAddress{}, "", "", jpk.Taxpayer{}, decimal.Zero)
	require.NoError(t, err)
	rig.srv = srv
	fx := rtBuildMaximalCard(t, rig)
	zipA, _ := rig.export(t, fx.cardID)
	var archA *techcardarchive.Archive
	archA, err = techcardarchive.OpenArchive(bytes.NewReader(zipA), int64(len(zipA)))
	require.NoError(t, err)
	require.Empty(t, archA.Manifest.ExportHoles)

	index := rtMediaIndex(t, archA)
	entryOf := func(mediaID int) techcardarchive.MediaIndexEntry {
		for _, e := range index {
			if int(e.Ref) == mediaID {
				return e
			}
		}
		t.Fatalf("media/index.json has no entry for media %d", mediaID)
		return techcardarchive.MediaIndexEntry{}
	}
	cardA, err := archA.CardJSON()
	require.NoError(t, err)
	slotsA := len(rtCardMediaIDs(cardA))

	mTech, mMood, mOp := fx.mediaIDs[0], fx.mediaIDs[1], fx.mediaIDs[3]

	// slotsOf exports the imported card and counts its distinct pictures: the only honest view of
	// what landed, since every id was remapped on the way in.
	slotsOf := func(t *testing.T, cardID int) int {
		t.Helper()
		z, _ := rig.export(t, cardID)
		a, err := techcardarchive.OpenArchive(bytes.NewReader(z), int64(len(z)))
		require.NoError(t, err)
		require.Empty(t, a.Manifest.ExportHoles, "the imported card must not point at media that is not there")
		c, err := a.CardJSON()
		require.NoError(t, err)
		return len(rtCardMediaIDs(c))
	}

	t.Run("file absent from zip, index still lists it", func(t *testing.T) {
		file := entryOf(mMood).File
		z := mmRewriteZip(t, zipA, func(name string, body []byte) ([]byte, bool) {
			return body, name != file
		})
		cardID, rep := rig.importArchive(t, z)
		rtRequireReportReason(t, rep, string(techcardarchive.EntityMedia), fmt.Sprintf("media_id=%d", mMood),
			string(techcardarchive.ReasonMediaMissing))
		require.Equal(t, slotsA-1, slotsOf(t, cardID))
		chart, err := rig.store.TechCards().GetStyleSizeChart(ctx, cardID)
		require.NoError(t, err)
		require.NotNil(t, chart.Picture, "an unrelated missing picture must not take the size chart picture")
	})

	t.Run("neither index entry nor file", func(t *testing.T) {
		gone := entryOf(mTech)
		z := mmRewriteZip(t, zipA, func(name string, body []byte) ([]byte, bool) {
			switch name {
			case gone.File:
				return nil, false
			case techcardarchive.FileMediaIndex:
				var idx []techcardarchive.MediaIndexEntry
				require.NoError(t, json.Unmarshal(body, &idx))
				kept := idx[:0]
				for _, e := range idx {
					if e.Ref != gone.Ref {
						kept = append(kept, e)
					}
				}
				out, err := json.Marshal(kept)
				require.NoError(t, err)
				return out, true
			}
			return body, true
		})
		cardID, rep := rig.importArchive(t, z)
		rtRequireReportReason(t, rep, string(techcardarchive.EntityMedia), fmt.Sprintf("media_id=%d", mTech),
			string(techcardarchive.ReasonMediaMissing))
		require.Equal(t, slotsA-1, slotsOf(t, cardID))
	})

	t.Run("size chart picture points at missing media", func(t *testing.T) {
		file := entryOf(fx.pictureMedia).File
		z := mmRewriteZip(t, zipA, func(name string, body []byte) ([]byte, bool) {
			return body, name != file
		})
		cardID, rep := rig.importArchive(t, z)
		rtRequireReportReason(t, rep, string(techcardarchive.EntityMedia), fmt.Sprintf("media_id=%d", fx.pictureMedia),
			string(techcardarchive.ReasonMediaMissing))
		chart, err := rig.store.TechCards().GetStyleSizeChart(ctx, cardID)
		require.NoError(t, err)
		require.Nil(t, chart.Picture, "a picture with no image must be dropped, not written with a dead id")
		require.NotEmpty(t, chart.Cells, "the chart itself must still import")
		require.Equal(t, slotsA, slotsOf(t, cardID))
	})

	t.Run("reused picture vanishes mid-import", func(t *testing.T) {
		// Hash-carrying copies of two pictures, so the commit's resolver plans to REUSE them instead
		// of uploading; nothing else references these rows. They are deleted from inside the first
		// upload of the commit: after the plan is fixed and before the card's transaction, the
		// window a parallel import being taken back opens.
		read := func(mediaID int) []byte {
			raw, err := archA.ReadFile(entryOf(mediaID).File)
			require.NoError(t, err)
			return raw
		}
		// Earlier imports left hash-carrying rows for every picture of this archive; forget them, so
		// the only reuse targets are the two copies below and every other picture uploads (which is
		// what fires the hook).
		for _, e := range index {
			_, err := testDB.ExecContext(ctx, "UPDATE media SET content_hash = NULL WHERE content_hash = ?", e.SHA256)
			require.NoError(t, err)
		}
		opCopy := rtSeedMedia(t, ctx, rig.store, rig.objs, "mm-op-copy", read(mOp), true)
		picCopy := rtSeedMedia(t, ctx, rig.store, rig.objs, "mm-pic-copy", read(fx.pictureMedia), true)

		importID, _ := rig.upload(t, zipA)
		var once sync.Once
		fired := false
		hooked.before = func() {
			once.Do(func() {
				fired = true
				_, err := testDB.ExecContext(ctx, "DELETE FROM media WHERE id IN (?, ?)", opCopy, picCopy)
				require.NoError(t, err)
			})
		}
		defer func() { hooked.before = nil }()

		cardID, rep := rig.commit(t, importID)
		require.True(t, fired, "the commit uploaded nothing, so the vanishing never happened")
		for _, src := range []int{mOp, fx.pictureMedia} {
			rtRequireReportReason(t, rep, string(techcardarchive.EntityMedia), fmt.Sprintf("media_id=%d", src),
				string(techcardarchive.ReasonMediaUploadFailed))
		}
		// The commit drops the picture itself; the store's own ref check (archive_row_invalid) is the
		// last line of defence and must not be what caught it.
		for _, l := range rep.GetLines() {
			require.NotEqual(t, "size_chart.picture", l.GetRef(),
				"the vanished picture reached the store's write: %+v", l)
		}
		chart, err := rig.store.TechCards().GetStyleSizeChart(ctx, cardID)
		require.NoError(t, err)
		require.Nil(t, chart.Picture, "a vanished size chart image must take the picture with it")
		require.NotEmpty(t, chart.Cells, "the chart itself must still import")
		require.Equal(t, slotsA-1, slotsOf(t, cardID))
	})
}

// mmHookedBucket runs before (when set) ahead of every verbatim image upload.
type mmHookedBucket struct {
	dependency.FileStore
	before func()
}

func (b *mmHookedBucket) UploadContentImageVerbatim(ctx context.Context, raw []byte, folder, name string) (*pb_common.MediaFull, error) {
	if b.before != nil {
		b.before()
	}
	return b.FileStore.UploadContentImageVerbatim(ctx, raw, folder, name)
}

// mmRewriteZip copies an archive entry by entry; edit returns the new body and whether to keep it.
func mmRewriteZip(t *testing.T, src []byte, edit func(name string, body []byte) ([]byte, bool)) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(src), int64(len(src)))
	require.NoError(t, err)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		out, keep := edit(f.Name, body)
		if !keep {
			continue
		}
		hdr := f.FileHeader
		w, err := zw.CreateHeader(&zip.FileHeader{Name: hdr.Name, Method: hdr.Method, Modified: hdr.Modified})
		require.NoError(t, err)
		_, err = w.Write(out)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
