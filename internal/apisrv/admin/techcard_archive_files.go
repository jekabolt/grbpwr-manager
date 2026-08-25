package admin

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф3.1 — MOVING THE BYTES, AND BEING ABLE TO TAKE THEM BACK.
//
// The pictures and the pattern sheets of an archive land in OUR bucket and OUR media table BEFORE
// the write transaction opens, because neither the bucket nor the media table is transactional and
// a card cannot be written pointing at objects that do not exist yet. That order buys one problem
// and this file is the answer to it: if the transaction afterwards does not happen, everything moved
// here is an orphan, and somebody has to take it back. Hence «+ компенсация» in the phase's name.
//
// FOUR THINGS DECIDED HERE, EACH FOR A REASON THAT IS NOT OBVIOUS:
//
//   - THE BYTES ARE READ WHOLE, NEVER STREAMED INTO THE BUCKET. A digest is final only at io.EOF
//     (techcardarchive.Archive.OpenFile says so out loud), so a consumer that pipes an entry
//     straight into PutObject has already handed over almost the whole file by the time the
//     mismatch is known — and a consumer that stops early and closes has verified NOTHING at all.
//     ReadFileVerified is the shape in which that mistake cannot be made: io.ReadAll always reaches
//     EOF, so when these bytes reach a bucket call their digest has already been proved. The three
//     upload entry points of FileStore take a []byte (or a base64 string) anyway, so nothing is lost
//     by it. One file is read, uploaded and released before the next one is opened, because the
//     image path costs another ×1.33 of the payload in its base64 envelope.
//
//   - A DIGEST MISMATCH KILLS THE WHOLE IMPORT; A REFUSED UPLOAD IS A HOLE. FORMAT.md §1.2 and §6.3:
//     corruption is the one thing that is never degraded into a report line, because bytes that do
//     not hold what they claim make every other belief about the archive worthless. A bucket that
//     will not take a legitimate file is the opposite case — one picture is lost, the card is not.
//
//   - THE SUBSTITUTION TOUCHES ONLY NEGATIVE IDS. See tcflSubstituteMediaPlaceholders: the insert
//     that arrives here is ALREADY remapped, so a "old id → new id" pass over it would clear every
//     picture of the import, including the ones this base already had.
//
//   - COMPENSATION DELETES THE MEDIA ROW BEFORE ITS OBJECTS. Deleting the objects and leaving the
//     row would leave a row whose content_hash still matches the archive — and the NEXT import of
//     the same archive would happily reuse it and build a card full of 404s.
//
// Names in this package: `dec`, `tcz*`, `amg*` and `tcimp*` are taken (Ф1.2/Ф1.4/Ф2.3). Everything
// here is `tcfl*` — tech-card files.
// ─────────────────────────────────────────────────────────────────────────────

// tcflMediaObject is one media row this import minted, kept with the object urls that back it so
// compensation can undo BOTH halves in the right order.
type tcflMediaObject struct {
	id int32
	// urls are the row's full-size / compressed / thumbnail objects. Three for a re-encoded
	// picture, ONE repeated three times for an animated GIF or a video — DeleteObjects dedupes by
	// object key, so the list is passed as it stands rather than tidied here.
	urls []string
}

// tcflPlacement is everything this phase PUT SOMEWHERE, plus the two tables the write side needs to
// finish the payload.
//
// It is returned even from a successful run for one reason: the transaction has not happened yet.
// A caller that does not go on to commit MUST call compensate, and a caller that does commit must
// not — the objects are then referenced by a card.
type tcflPlacement struct {
	// mediaByPlaceholder maps the resolver's NEGATIVE stand-in (tcimpMediaPlan.Placeholder) to the
	// media row minted here. It is the only mapping the substitution below consults.
	mediaByPlaceholder map[int32]int32
	// patternByLineKey maps a sheet's stable line_key to the object it was re-uploaded into.
	patternByLineKey map[string]tcflPatternObject

	// media and patterns are the compensation record — see uploadedKeys. A file REUSED by content
	// hash appears in neither: this import did not create it, and deleting it would take a picture
	// away from every other card that already points at it.
	media    []tcflMediaObject
	patterns []string
}

// tcflPatternObject is a re-uploaded sheet: the url the card must carry and the byte size the row
// stores next to it.
type tcflPatternObject struct {
	url  string
	size int64
}

// uploadedKeys is every bucket object this placement created, in the order it created them — the
// exact list compensation deletes, and the list a test can hold against "a reuse must not be in it".
//
// They are urls rather than raw object keys because that is how a media or pattern object is
// addressed everywhere in this codebase (media.url, pattern.url); FileStore.DeleteObjects turns
// them into keys itself and refuses any url that is not on a configured bucket host.
func (p *tcflPlacement) uploadedKeys() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.media)*3+len(p.patterns))
	for _, m := range p.media {
		out = append(out, m.urls...)
	}
	out = append(out, p.patterns...)
	return out
}

// tcflPlaceImportFiles moves every file of the archive into this instance and finishes the payload
// with what the move produced: real media ids in place of the resolver's placeholders, real urls on
// the surviving pattern rows.
//
// ORDER: ALL uploads first, substitution afterwards. A substitution interleaved with the uploads
// would leave a half-remapped insert behind any early return, and «half-remapped» is the one state
// nothing downstream is able to recognise.
//
// On its OWN failure it compensates and returns nil: a failure inside this function is this
// function's to clean up, and a caller cannot forget what it was never handed. On success the
// placement travels to the caller, who owes it a compensate() if the transaction does not happen.
func (s *Server) tcflPlaceImportFiles(ctx context.Context, a *techcardarchive.Archive,
	res *resolvedTechCardImport,
) (*tcflPlacement, error) {
	if a == nil || res == nil || res.Insert == nil {
		return nil, fmt.Errorf("tech card import: nothing to place files for")
	}
	p := &tcflPlacement{
		mediaByPlaceholder: map[int32]int32{},
		patternByLineKey:   map[string]tcflPatternObject{},
	}

	if err := s.tcflExecuteMediaPlan(ctx, a, res, p); err != nil {
		p.compensate(ctx, s)
		return nil, err
	}
	if err := s.tcflExecutePatternPlan(ctx, a, res, p); err != nil {
		p.compensate(ctx, s)
		return nil, err
	}

	tcflApplyMediaPlaceholders(res, p)
	tcflApplyPatternObjects(res, p)
	return p, nil
}

// ────────────────────────────── media ──────────────────────────────

// tcflExecuteMediaPlan uploads every planned file and records the row it produced.
//
// A REUSE IS NOT WORK. Its media row is already in this base — the resolver matched it by
// media.content_hash — and the insert already carries its final id. Nothing is uploaded for it and
// nothing about it is remembered for compensation, which is precisely the property that keeps a
// failed import from deleting a picture that belongs to somebody else's card.
//
// ONE UPLOAD PER PLACEHOLDER, not per plan row. Two source media ids whose bytes are identical
// share one placeholder by construction (the archive stores one file for them), and uploading the
// file twice would store the same picture twice and hand the second copy to the second slot.
func (s *Server) tcflExecuteMediaPlan(ctx context.Context, a *techcardarchive.Archive,
	res *resolvedTechCardImport, p *tcflPlacement,
) error {
	failed := map[int32]bool{}
	lost := 0

	for _, plan := range res.MediaPlan {
		if plan.Action != tcimpMediaUpload {
			continue
		}
		ref := fmt.Sprintf("media_id=%d", plan.SourceID)
		if plan.Placeholder >= 0 {
			// Unreachable through the resolver, which mints a negative stand-in for every upload.
			// Kept because the substitution below can only recognise a negative one: a plan row
			// that arrived without it would be silently unplaceable, and silence is the failure
			// mode this whole phase exists to remove.
			lost++
			tcflHole(res, ref, techcardarchive.ReasonMediaUploadFailed,
				"the import plan carried no placeholder for this picture, so nothing could be substituted for it; the slot was left empty")
			continue
		}
		if _, done := p.mediaByPlaceholder[plan.Placeholder]; done {
			continue
		}
		if failed[plan.Placeholder] {
			lost++
			tcflHole(res, ref, techcardarchive.ReasonMediaUploadFailed,
				"the same file failed to upload for another slot of this card; the slot was left empty")
			continue
		}
		if _, known := tcflExtFamily[tcflEntryExt(plan.File)]; !known {
			// BEFORE the read, not after. An extension outside FORMAT.md §1.1 is not a picture to
			// try decoding «just in case»: the entry is bounded only by the archive-wide gigabyte,
			// and reading it in to find out costs exactly that.
			failed[plan.Placeholder] = true
			lost++
			tcflHole(res, ref, techcardarchive.ReasonMediaUploadFailed,
				fmt.Sprintf("%q is not a picture or a video this server stores; nothing was read or decoded", path.Base(plan.File)))
			continue
		}

		raw, err := a.ReadFileVerified(plan.File, plan.SHA256)
		if err != nil {
			if techcardarchive.IsFatal(err) {
				// Corruption or a refusal of the format. FORMAT.md §1.2: never a hole. Every other
				// belief about this archive rests on its digests, so one that does not hold ends
				// the import instead of costing one picture.
				return fmt.Errorf("tech card import: media file %q: %w", plan.File, err)
			}
			failed[plan.Placeholder] = true
			lost++
			tcflHole(res, ref, techcardarchive.ReasonMediaUploadFailed,
				fmt.Sprintf("the archive would not give up the file for this picture (%s); the slot was left empty", err.Error()))
			continue
		}

		id, urls, err := s.tcflUploadMediaBytes(ctx, plan.File, raw)
		if len(urls) > 0 || id > 0 {
			// Recorded BEFORE the error is looked at. Anything the bucket named back to us exists
			// there whether or not the call as a whole succeeded, and an object nobody remembers
			// is an object nobody can take back.
			p.media = append(p.media, tcflMediaObject{id: id, urls: urls})
		}
		if err != nil {
			if ctx.Err() != nil {
				// A dead context is not a bad file. Left to run, the loop would write one
				// media_upload_failed line per picture and hand back a card with no pictures at
				// all, which reads exactly like an archive whose bucket refused it.
				return fmt.Errorf("tech card import: media upload stopped: %w", err)
			}
			failed[plan.Placeholder] = true
			lost++
			slog.Default().ErrorContext(ctx, "tech card import: media upload failed",
				slog.String("file", plan.File), slog.Int("source_media_id", int(plan.SourceID)),
				slog.String("err", err.Error()))
			tcflHole(res, ref, techcardarchive.ReasonMediaUploadFailed,
				fmt.Sprintf("this instance would not store the file (%s); the slot was left empty and the rest of the card imported", err.Error()))
			continue
		}

		p.mediaByPlaceholder[plan.Placeholder] = id
	}

	if lost > 0 {
		// The resolver counted every PLANNED upload as imported; these did not land. Correcting
		// both columns rather than only adding to `skipped` keeps the tally an account of what
		// happened instead of an account of what was intended.
		res.Counters.AddImported(techcardarchive.EntityMedia, -lost)
		res.Counters.AddSkipped(techcardarchive.EntityMedia, lost)
	}
	return nil
}

// tcflUploadMediaBytes stores one file and returns the media row it minted plus the objects behind
// it.
//
// The route is decided by the BYTES first and by the entry's extension only where the bytes say
// nothing (see tcflMediaRoute). An extension outside FORMAT.md §1.1 is refused before any of that:
// it is not something to guess about, and handing an unknown payload to the image decoder to «see
// what happens» is how an import spends a decompression bomb's worth of memory finding out.
func (s *Server) tcflUploadMediaBytes(ctx context.Context, entry string, raw []byte) (int32, []string, error) {
	family, mime, ok := tcflMediaRoute(tcflEntryExt(entry), raw)
	if !ok {
		return 0, nil, fmt.Errorf("%q is not a picture or a video this server stores", path.Base(entry))
	}

	var (
		full *pb_common.MediaFull
		err  error
	)
	if family == tcflFamilyVideo {
		full, err = s.bucket.UploadContentVideo(ctx, raw, s.bucket.GetBaseFolder(), bucket.GetMediaName(), mime)
	} else {
		full, err = s.bucket.UploadContentImage(ctx, tcflDataURI(mime, raw), s.bucket.GetBaseFolder(), bucket.GetMediaName())
	}
	// The urls travel back even on an error: an upload that stored two variants and then failed on
	// the third has already put bytes in the bucket, and the caller records what it is given before
	// it decides what the error means.
	urls := tcflMediaURLs(full.GetMedia())
	if err != nil {
		return 0, urls, err
	}
	if full.GetId() <= 0 {
		return 0, urls, fmt.Errorf("the upload stored the file but minted no media row")
	}
	return full.GetId(), urls, nil
}

// tcflMediaURLs lists a row's three variant urls. Empty ones are dropped, duplicates are not: a
// video (and an animated GIF's full-size/compressed pair) stores ONE object under several urls, and
// DeleteObjects already collapses them by object key.
func tcflMediaURLs(mi *pb_common.MediaItem) []string {
	out := make([]string, 0, 3)
	for _, v := range []*pb_common.MediaInfo{mi.GetFullSize(), mi.GetCompressed(), mi.GetThumbnail()} {
		if u := v.GetMediaUrl(); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// ────────────────────────────── patterns ──────────────────────────────

// tcflExecutePatternPlan re-uploads every sheet the archive carries a file for.
//
// A sheet that does not make it is DROPPED from the payload, not left behind with an empty url:
// ConvertPbTechCardInsertToEntity requires a url on a managed host, so a surviving blank row would
// take the entire import down with it — one lost sheet costing the whole card. That is the same
// reasoning resolvePatterns applies to a sheet with no file at all, one step later.
func (s *Server) tcflExecutePatternPlan(ctx context.Context, a *techcardarchive.Archive,
	res *resolvedTechCardImport, p *tcflPlacement,
) error {
	for _, plan := range res.PatternPlan {
		key := strings.TrimSpace(plan.LineKey)
		ref := fmt.Sprintf("line_key=%s", key)
		if key == "" {
			// Unreachable through the resolver, which plans only keyed sheets. It is still a LINE
			// rather than a `continue`: a sheet is matched back onto its payload row by that key,
			// so one without it is a sheet nothing can be attached to — and nothing here is
			// allowed to disappear without saying so.
			tcflPatternHole(res, fmt.Sprintf("filename=%s", plan.Filename),
				"the sheet arrived without a line_key, so nothing could be bound to it; it was not imported")
			continue
		}
		if _, done := p.patternByLineKey[key]; done {
			continue
		}

		if ext := tcflEntryExt(plan.File); ext != "dxf" && ext != "pdf" {
			// Same rule as the media side: an extension outside §1.1 is a hole on the spot rather
			// than a payload handed to a sniffer to argue with.
			tcflPatternHole(res, ref,
				fmt.Sprintf("%q is not a DXF or a PDF; the sheet was not imported", path.Base(plan.File)))
			continue
		}

		raw, err := a.ReadFileVerified(plan.File, plan.SHA256)
		if err != nil {
			if techcardarchive.IsFatal(err) {
				return fmt.Errorf("tech card import: pattern file %q: %w", plan.File, err)
			}
			tcflPatternHole(res, ref,
				fmt.Sprintf("the archive would not give up the file for this sheet (%s); the sheet was not imported", err.Error()))
			continue
		}

		url, size, err := s.bucket.UploadPatternFile(ctx, raw, bucket.GetMediaName())
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("tech card import: pattern upload stopped: %w", err)
			}
			slog.Default().ErrorContext(ctx, "tech card import: pattern upload failed",
				slog.String("file", plan.File), slog.String("line_key", key),
				slog.String("err", err.Error()))
			tcflPatternHole(res, ref,
				fmt.Sprintf("this instance would not store the sheet (%s); the sheet was not imported", err.Error()))
			continue
		}

		p.patternByLineKey[key] = tcflPatternObject{url: url, size: size}
		p.patterns = append(p.patterns, url)
	}
	return nil
}

// ────────────────────────────── finishing the payload ──────────────────────────────

// tcflApplyMediaPlaceholders substitutes the minted media ids into the insert and removes the rows
// whose picture never arrived.
//
// ⚠️ THIS IS NOT RemapIntFieldsDeep, AND IT MUST NOT BE. The brief for this phase asked for a
// «source id → new id» map run through that helper; doing so would empty EVERY picture of the
// import. The insert handed over here is already remapped: a reused picture carries its FINAL id in
// this base and an upload carries a negative placeholder — the source's own numbers are gone from
// the tree entirely. RemapIntFieldsDeep clears any non-zero value it does not find in the mapping,
// so a placeholder-keyed mapping would find neither and would wipe both. Hence a pass of its own,
// whose whole rule is: touch negatives, leave everything else exactly where it is.
//
// A placeholder still negative after the pass is an upload that failed. Its slot is cleared and the
// row is dropped by the same gesture that drops a slot with no bytes at all (dropEmptyMediaRows);
// the media_upload_failed line was written when the upload failed, so nothing is reported twice.
func tcflApplyMediaPlaceholders(res *resolvedTechCardImport, p *tcflPlacement) {
	orphans := map[int64]bool{}
	tcflSubstituteMediaPlaceholders(res.Insert.ProtoReflect(), p.mediaByPlaceholder,
		func(field string, placeholder int64) {
			orphans[placeholder] = true
			slog.Default().Warn("tech card import: media placeholder left unplaced",
				slog.String("field", field), slog.Int64("placeholder", placeholder))
		})
	// The same gesture, not a second copy of it: a row whose media FK is now 0 is unwritable, and
	// there must be exactly one answer in this package to what happens to it.
	(&tcimpResolver{out: res}).dropEmptyMediaRows()
}

// tcflApplyPatternObjects writes the new url onto every sheet that was re-uploaded and drops the
// rest. After this the AUTHORITY on which sheets travel is Insert.Patterns; PatternPlan stays as
// the resolver left it, because it is a record of what was planned and not of what happened.
func tcflApplyPatternObjects(res *resolvedTechCardImport, p *tcflPlacement) {
	ins := res.Insert
	kept := ins.Patterns[:0]
	dropped := 0
	for _, row := range ins.Patterns {
		if row == nil {
			continue
		}
		key := strings.TrimSpace(row.GetLineKey())
		obj, ok := p.patternByLineKey[key]
		if !ok || obj.url == "" {
			// The report line was written where the sheet was lost, so this only counts. The log
			// is the belt: a row dropped here that the upload loop never saw would mean the plan
			// and the payload disagree about which sheets exist, and that must not be inaudible.
			dropped++
			slog.Default().Warn("tech card import: pattern row dropped for want of an object",
				slog.String("line_key", key))
			continue
		}
		row.Url = obj.url
		row.SizeBytes = obj.size
		kept = append(kept, row)
	}
	ins.Patterns = kept

	if dropped > 0 {
		res.Counters.AddImported(techcardarchive.EntityPattern, -dropped)
		res.Counters.AddSkipped(techcardarchive.EntityPattern, dropped)
	}
}

// ────────────────────────────── compensation ──────────────────────────────

// compensate takes back everything this placement moved, and NOTHING it merely pointed at.
//
// The model is cleanupObjects in files_upload.go: best effort, on a context that outlives the
// cancelled one, and loud — a stray object changes nothing a caller can act on, but it is worth a
// line in the log so it can be swept rather than lost.
//
// THE ROW GOES BEFORE ITS OBJECTS, and if the row will not go the objects stay. A media row whose
// objects have been deleted is worse than both halves surviving: it still carries the archive's
// content_hash, so the next import of the same archive would «reuse» it and build a card whose
// every picture is a 404 — silently, because reuse is the healthy path.
//
// A reuse is not here at all (see tcflExecuteMediaPlan), which is what makes it impossible for a
// failed import to delete a picture another card is using.
func (p *tcflPlacement) compensate(ctx context.Context, s *Server) {
	if p == nil || s == nil {
		return
	}
	for _, m := range p.media {
		cctx, cancel := tcflCleanupContext(ctx)
		// id == 0 is the half-finished upload: objects in the bucket, no row minted. There is
		// nothing to delete first, and the objects are exactly what must go.
		if m.id > 0 {
			if err := s.repo.Media().DeleteMediaById(cctx, int(m.id)); err != nil {
				slog.Default().ErrorContext(cctx, "tech card import: orphaned media row after a failed import",
					slog.Int("media_id", int(m.id)), slog.Any("urls", m.urls), slog.String("err", err.Error()))
				cancel()
				continue
			}
		}
		if len(m.urls) > 0 {
			if err := s.bucket.DeleteObjects(cctx, m.urls...); err != nil {
				slog.Default().ErrorContext(cctx, "tech card import: orphaned media objects after a failed import",
					slog.Any("urls", m.urls), slog.String("err", err.Error()))
			}
		}
		cancel()
	}
	if len(p.patterns) > 0 {
		cctx, cancel := tcflCleanupContext(ctx)
		if err := s.bucket.DeleteObjects(cctx, p.patterns...); err != nil {
			slog.Default().ErrorContext(cctx, "tech card import: orphaned pattern objects after a failed import",
				slog.Any("urls", p.patterns), slog.String("err", err.Error()))
		}
		cancel()
	}
}

// tcflCleanupContext detaches from a context that is very likely already cancelled — the commonest
// reason to be compensating at all — and bounds the attempt. One budget per item rather than one
// for the whole sweep: a shared deadline spent on the first slow object would abandon the rest
// quietly, and a cleanup that stops halfway without saying so is the shape of orphan nobody finds.
func tcflCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}

// ────────────────────────────── the substitution walk ──────────────────────────────

// tcflSubstituteMediaPlaceholders rewrites every media FK in the tree that is NEGATIVE, and only
// those. See tcflApplyMediaPlaceholders for why "only those" is the whole point.
//
// The three cases, and why each is what it is:
//
//   - 0 is untouched. Across the tech-card contract 0 means unset, and callout.media_id = 0 is
//     documented as «not anchored to a picture». Substituting it would invent a reference.
//   - a POSITIVE id is untouched. It is a row of THIS base, put there by the resolver's reuse
//     branch; there is nothing to translate and translating it is how an import loses the pictures
//     it did not have to upload.
//   - a NEGATIVE id is a placeholder. Found in the mapping it becomes the minted row; missing, it
//     is reported and cleared, because a negative id left in the payload would break the foreign
//     key and take the whole card with it.
//
// Traversal is the discipline of RemapIntFieldsDeep — the same map / list / message branches, the
// same field-name list (techcardarchive.MediaFieldNames, which is kept honest by the field-list
// guard in walk_test.go and is where `media_ids` and `swatch_media_id` come from).
func tcflSubstituteMediaPlaceholders(m protoreflect.Message, mapping map[int32]int32,
	onOrphan func(field string, placeholder int64),
) {
	if m == nil || !m.IsValid() {
		return
	}
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		name := string(fd.Name())
		if techcardarchive.MediaFieldNames[name] && tcflIsIntKind(fd) && !fd.IsMap() {
			if fd.IsList() {
				tcflSubstituteList(v.List(), fd, name, mapping, onOrphan)
				return true
			}
			old := v.Int()
			if old >= 0 {
				return true
			}
			nv, ok := mapping[int32(old)]
			if !ok {
				tcflReportOrphan(onOrphan, name, old)
				m.Clear(fd)
				return true
			}
			m.Set(fd, tcflIntValue(fd, int64(nv)))
			return true
		}
		switch {
		case fd.IsMap():
			if k := fd.MapValue().Kind(); k != protoreflect.MessageKind && k != protoreflect.GroupKind {
				return true
			}
			v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				tcflSubstituteMediaPlaceholders(mv.Message(), mapping, onOrphan)
				return true
			})
		case fd.IsList() && tcflIsMessageKind(fd):
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				tcflSubstituteMediaPlaceholders(l.Get(i).Message(), mapping, onOrphan)
			}
		case tcflIsMessageKind(fd):
			tcflSubstituteMediaPlaceholders(v.Message(), mapping, onOrphan)
		}
		return true
	})
}

// tcflSubstituteList is the repeated case (TechCardDetail.media_ids). An unplaced placeholder is
// DROPPED from the list rather than written as 0: these lists have no «unset» slot semantics, so a
// 0 in them would fabricate a reference to row 0.
func tcflSubstituteList(l protoreflect.List, fd protoreflect.FieldDescriptor, name string,
	mapping map[int32]int32, onOrphan func(field string, placeholder int64),
) {
	kept := make([]int64, 0, l.Len())
	for i := 0; i < l.Len(); i++ {
		old := l.Get(i).Int()
		if old >= 0 {
			kept = append(kept, old)
			continue
		}
		nv, ok := mapping[int32(old)]
		if !ok {
			tcflReportOrphan(onOrphan, name, old)
			continue
		}
		kept = append(kept, int64(nv))
	}
	l.Truncate(0)
	for _, v := range kept {
		l.Append(tcflIntValue(fd, v))
	}
}

func tcflReportOrphan(onOrphan func(field string, placeholder int64), name string, old int64) {
	if onOrphan != nil {
		onOrphan(name, old)
	}
}

func tcflIsMessageKind(fd protoreflect.FieldDescriptor) bool {
	k := fd.Kind()
	return k == protoreflect.MessageKind || k == protoreflect.GroupKind
}

func tcflIsIntKind(fd protoreflect.FieldDescriptor) bool {
	switch fd.Kind() {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return true
	default:
		return false
	}
}

func tcflIntValue(fd protoreflect.FieldDescriptor, v int64) protoreflect.Value {
	switch fd.Kind() {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(int32(v))
	default:
		return protoreflect.ValueOfInt64(v)
	}
}

// ────────────────────────────── routing and small change ──────────────────────────────

const (
	tcflFamilyImage = "image"
	tcflFamilyVideo = "video"
)

// tcflExtFamily is FORMAT.md §1.1's media list, split into the two upload paths this server has.
// A file whose extension is not in it never reaches a decoder.
var tcflExtFamily = map[string]struct {
	family string
	mime   string
}{
	"jpg":  {tcflFamilyImage, "image/jpeg"},
	"jpeg": {tcflFamilyImage, "image/jpeg"},
	"png":  {tcflFamilyImage, "image/png"},
	"webp": {tcflFamilyImage, "image/webp"},
	"gif":  {tcflFamilyImage, "image/gif"},
	"mp4":  {tcflFamilyVideo, "video/mp4"},
	"webm": {tcflFamilyVideo, "video/webm"},
}

// tcflMediaRoute answers ONE question — image path or video path — and it asks the bytes first.
//
// The extension is a claim an archive makes about itself and this is untrusted input, so it decides
// only two things: WHETHER the file may be uploaded at all (an extension outside §1.1 is refused
// here, before any decoder sees the payload) and what to do when the magic numbers say nothing.
//
// It is a router, not a validator. The bucket paths sniff again and refuse a payload that does not
// match — decodeImage ignores the declared type entirely and uploadVideoObj requires the sniffed
// type to equal the declared one — so a file that lies about itself ends as a hole, not as a wrong
// object. An `ftyp` box is deliberately left AMBIGUOUS: it is mp4, and it is also HEIC and AVIF, so
// the extension is the better of the two witnesses there.
func tcflMediaRoute(ext string, raw []byte) (family, mime string, ok bool) {
	byExt, known := tcflExtFamily[ext]
	if !known {
		return "", "", false
	}
	if f, m := tcflSniffMedia(raw); f != "" {
		return f, m, true
	}
	return byExt.family, byExt.mime, true
}

// tcflSniffMedia recognises the container from its magic bytes, mirroring the bucket's own
// sniffImageType / sniffVideoType (which are unexported, and which this must not diverge from
// enough to matter: it only chooses a route, and both of them run again inside).
//
// "" means "these bytes name no container I know" — including every `ftyp` file, see above.
func tcflSniffMedia(raw []byte) (family, mime string) {
	switch {
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return tcflFamilyImage, "image/jpeg"
	case len(raw) >= 8 && string(raw[:8]) == "\x89PNG\r\n\x1a\n":
		return tcflFamilyImage, "image/png"
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return tcflFamilyImage, "image/webp"
	case len(raw) >= 6 && (string(raw[:6]) == "GIF87a" || string(raw[:6]) == "GIF89a"):
		return tcflFamilyImage, "image/gif"
	case len(raw) >= 4 && raw[0] == 0x1A && raw[1] == 0x45 && raw[2] == 0xDF && raw[3] == 0xA3:
		return tcflFamilyVideo, "video/webm"
	default:
		return "", ""
	}
}

// tcflEntryExt is the lower-case extension of a ZIP entry name, without the dot. The name has
// already been through the reader's own validation (no traversal, no backslash, no control
// characters), so this only has to read it.
func tcflEntryExt(entry string) string {
	return strings.ToLower(strings.TrimPrefix(path.Ext(entry), "."))
}

// tcflDataURI wraps raw bytes in the "data:<mime>;base64,<payload>" envelope UploadContentImage
// parses. Built in one allocation on purpose: the envelope already costs ×1.33 of the picture, and
// growing a string into it would briefly cost twice that.
func tcflDataURI(mime string, raw []byte) string {
	var b strings.Builder
	b.Grow(len("data:") + len(mime) + len(";base64,") + base64.StdEncoding.EncodedLen(len(raw)))
	b.WriteString("data:")
	b.WriteString(mime)
	b.WriteString(";base64,")
	b.WriteString(base64.StdEncoding.EncodeToString(raw))
	return b.String()
}

// tcflHole records a media hole on the resolved import. The code is always media_upload_failed and
// never media_object_missing: the two are opposite ends of the same journey — the EXPORT could not
// read the object (§7), and here the bytes WERE in the archive and this instance would not take
// them. An operator told the wrong one goes looking on the wrong machine.
func tcflHole(res *resolvedTechCardImport, ref string, reason techcardarchive.Reason, detail string) {
	res.Holes = append(res.Holes, techcardarchive.ImportHole{
		Entity: techcardarchive.EntityMedia,
		Ref:    ref,
		Status: techcardarchive.StatusSkipped,
		Reason: reason,
		Detail: detail,
	})
}

func tcflPatternHole(res *resolvedTechCardImport, ref, detail string) {
	res.Holes = append(res.Holes, techcardarchive.ImportHole{
		Entity: techcardarchive.EntityPattern,
		Ref:    ref,
		Status: techcardarchive.StatusSkipped,
		Reason: techcardarchive.ReasonPatternInvalid,
		Detail: detail,
	})
}
