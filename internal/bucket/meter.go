package bucket

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/minio/minio-go/v7"
)

// STORAGE CAP (teams wave, lane X11, gap 11). Every byte a team writes to the bucket goes through
// put below — the only PutObject call of the package (TestPutObjectOnlyInPut) — so no upload path
// skips the team's cap. Reads, presigned links and the proxy never touch the meter.
//
//   - known size n: the meter reserves n atomically BEFORE the bytes go out (refused = nothing is
//     written); a failed put gives the reservation back.
//   - stream (size -1): admitted only while the team is under its cap, counted with the real size
//     after the put. The stream paths have their own hard ceilings, so the overshoot is bounded.
//   - delete: the object's size is read (one HEAD) before it is removed and taken off the count.
//   - a meter error that is not «full» lets the upload pass (logged): accounting never stops work;
//     the daily recount (SumByTeam) heals the number.

// ErrStorageFull: the team in ctx is at its storage cap; nothing was written. The text carries a
// token ([storage_full]) no other error is likely to contain: it is the fallback match below.
var ErrStorageFull = errors.New("bucket: team storage cap reached [storage_full]")

// IsStorageFull reports whether err is ErrStorageFull: errors.Is first; the full sentinel text only
// as a fallback, for handlers that flatten the chain (%v, a gRPC status message). An unrelated error
// that merely says «storage full» does not match.
func IsStorageFull(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrStorageFull) {
		return true
	}
	return strings.Contains(err.Error(), ErrStorageFull.Error())
}

// StorageMeter counts the bytes of each team (control plane, internal/storagemeter).
type StorageMeter interface {
	// Reserve admits n more bytes for the team, or answers ErrStorageFull. stream = the size is not
	// known yet (n is 0): admitted while the team is strictly under its cap.
	Reserve(ctx context.Context, teamID, n int64, stream bool) error
	// Add moves the team's count by delta (negative on delete / a failed put), never below zero.
	Add(ctx context.Context, teamID, delta int64) error
}

type meterBox struct{ m StorageMeter }

// SetStorageMeter turns the storage cap on (teams mode only; nil = off, today's behaviour). Safe
// while requests and workers write the bucket.
func (b *Bucket) SetStorageMeter(m StorageMeter) {
	if m == nil {
		b.meter.Store(nil)
		return
	}
	b.meter.Store(&meterBox{m: m})
}

// meterTeam is the meter and the team the bytes of this ctx are counted to; nil = nothing is
// counted (no meter, teams mode off, or no team in ctx).
func (b *Bucket) meterTeam(ctx context.Context) (StorageMeter, int64) {
	box := b.meter.Load()
	if box == nil || b.Config == nil || !b.Config.TeamsMode {
		return nil, 0
	}
	t, ok := tenant.From(ctx)
	if !ok || t.ID <= 0 {
		return nil, 0
	}
	return box.m, t.ID
}

// meterCtx outlives a cancelled request: the bytes are already in (or out of) the bucket.
func meterCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func meterAdd(ctx context.Context, m StorageMeter, team, delta int64) {
	if delta == 0 {
		return
	}
	mctx, cancel := meterCtx(ctx)
	defer cancel()
	if err := m.Add(mctx, team, delta); err != nil {
		slog.Default().ErrorContext(ctx, "storage meter: can't move the count",
			slog.Int64("team", team), slog.Int64("delta", delta), slog.String("err", err.Error()))
	}
}

// put is the package's one write to the bucket. exempt = counted but never refused (the tech-card
// export archive: a download must keep working at the cap).
func (b *Bucket) put(ctx context.Context, key string, r io.Reader, size int64, opts minio.PutObjectOptions, exempt bool) (minio.UploadInfo, error) {
	m, team := b.meterTeam(ctx)
	metered := m != nil
	var reserved int64
	if metered && !exempt {
		n := size
		if n < 0 {
			n = 0
		}
		if err := m.Reserve(ctx, team, n, size < 0); err != nil {
			if errors.Is(err, ErrStorageFull) {
				slog.Default().InfoContext(ctx, "storage cap: upload refused",
					slog.Int64("team", team), slog.Int64("bytes", size))
				return minio.UploadInfo{}, err
			}
			slog.Default().ErrorContext(ctx, "storage meter: reserve failed, upload passes",
				slog.Int64("team", team), slog.String("err", err.Error()))
		} else {
			reserved = n
		}
	}
	// A team's work file (teams mode, {base}/t/…) is private: no public-read, read through a signed url
	// (teamsign.go, lane X13). Team #1 and teams mode off keep the acl their caller set.
	if b.Config.isPrivateTeamKey(key) {
		opts.UserMetadata = withoutPublicACL(opts.UserMetadata)
	}
	info, err := b.Client.PutObject(ctx, b.S3BucketName, key, r, size, opts)
	if err != nil {
		if metered {
			meterAdd(ctx, m, team, -reserved)
		}
		return info, err
	}
	if metered {
		meterAdd(ctx, m, team, info.Size-reserved)
	}
	return info, nil
}

// removeMetered removes one object and takes its size off the team's count.
func (b *Bucket) removeMetered(ctx context.Context, key string) error {
	m, team := b.meterTeam(ctx)
	metered := m != nil
	var size int64
	if metered {
		if st, err := b.Client.StatObject(ctx, b.S3BucketName, key, minio.StatObjectOptions{}); err == nil {
			size = st.Size
		}
	}
	if err := b.Client.RemoveObject(ctx, b.S3BucketName, key, minio.RemoveObjectOptions{}); err != nil {
		return err
	}
	if metered {
		meterAdd(ctx, m, team, -size)
	}
	return nil
}

// StorageTotals is one listing of the bucket split by owner: TeamOne = team #1's legacy keys (under
// the base folder outside {base}/t/, plus the root archive / import segments), ByFileKey = the bytes
// under {base}/t/{file_key}/. Objects outside both (another installation's folder in a shared
// bucket) are not counted.
type StorageTotals struct {
	TeamOne   int64
	ByFileKey map[string]int64
	Objects   int64
}

// SumByTeam lists the whole bucket once (the daily recount; never on a request path).
func (b *Bucket) SumByTeam(ctx context.Context) (StorageTotals, error) {
	out := StorageTotals{ByFileKey: map[string]int64{}}
	base := strings.Trim(strings.TrimSpace(b.BaseFolder), "/")
	for obj := range b.Client.ListObjects(ctx, b.S3BucketName, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			return StorageTotals{}, obj.Err
		}
		out.Objects++
		owner, fk := classifyKey(base, obj.Key)
		switch owner {
		case keyTeamOne:
			out.TeamOne += obj.Size
		case keyTeam:
			out.ByFileKey[fk] += obj.Size
		}
	}
	return out, ctx.Err()
}

type keyOwner int

const (
	keyNobody keyOwner = iota
	keyTeamOne
	keyTeam
)

// classifyKey says whose bytes an object key is (see StorageTotals).
func classifyKey(base, key string) (keyOwner, string) {
	key = strings.TrimLeft(key, "/")
	rest := key
	if base != "" {
		if !strings.HasPrefix(key, base+"/") {
			if strings.HasPrefix(key, ArchiveSegment+"/") || strings.HasPrefix(key, ImportSegment+"/") {
				return keyTeamOne, ""
			}
			return keyNobody, ""
		}
		rest = key[len(base)+1:]
	}
	if strings.HasPrefix(rest, teamSegment+"/") {
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) == 3 && validFileKey(parts[1]) {
			return keyTeam, parts[1]
		}
		return keyNobody, ""
	}
	return keyTeamOne, ""
}

// StorageRoom answers ErrStorageFull when the team in ctx is at its cap — the cheap question asked
// before work that would end in an upload (a paid AI generation, an archive import), so a full team
// neither pays nor waits for a refusal at the put. Nothing is reserved; a meter error answers nil
// (accounting never stops work), and so does a ctx the meter does not count.
func (b *Bucket) StorageRoom(ctx context.Context) error {
	m, team := b.meterTeam(ctx)
	if m == nil {
		return nil
	}
	if err := m.Reserve(ctx, team, 0, true); err != nil {
		if errors.Is(err, ErrStorageFull) {
			return err
		}
		slog.Default().ErrorContext(ctx, "storage meter: room check failed, work passes",
			slog.Int64("team", team), slog.String("err", err.Error()))
	}
	return nil
}
