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

// ErrStorageFull: the team in ctx is at its storage cap; nothing was written.
var ErrStorageFull = errors.New("bucket: storage full")

// IsStorageFull reports whether err carries ErrStorageFull, also through a %v wrap or a gRPC status
// message (the text is the fallback: several handlers flatten the chain).
func IsStorageFull(err error) bool {
	return err != nil && (errors.Is(err, ErrStorageFull) || strings.Contains(err.Error(), ErrStorageFull.Error()))
}

// StorageMeter counts the bytes of each team (control plane, internal/storagemeter).
type StorageMeter interface {
	// Reserve admits n more bytes for the team, or answers ErrStorageFull. stream = the size is not
	// known yet (n is 0): admitted while the team is strictly under its cap.
	Reserve(ctx context.Context, teamID, n int64, stream bool) error
	// Add moves the team's count by delta (negative on delete / a failed put), never below zero.
	Add(ctx context.Context, teamID, delta int64) error
}

// SetStorageMeter turns the storage cap on (teams mode only; nil = off, today's behaviour).
func (b *Bucket) SetStorageMeter(m StorageMeter) { b.meter = m }

// meterTeam is the team the bytes of this ctx are counted to; false = nothing is counted (no meter,
// teams mode off, or no team in ctx).
func (b *Bucket) meterTeam(ctx context.Context) (int64, bool) {
	if b.meter == nil || b.Config == nil || !b.Config.TeamsMode {
		return 0, false
	}
	t, ok := tenant.From(ctx)
	if !ok || t.ID <= 0 {
		return 0, false
	}
	return t.ID, true
}

// meterCtx outlives a cancelled request: the bytes are already in (or out of) the bucket.
func meterCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (b *Bucket) meterAdd(ctx context.Context, team, delta int64) {
	if delta == 0 {
		return
	}
	mctx, cancel := meterCtx(ctx)
	defer cancel()
	if err := b.meter.Add(mctx, team, delta); err != nil {
		slog.Default().ErrorContext(ctx, "storage meter: can't move the count",
			slog.Int64("team", team), slog.Int64("delta", delta), slog.String("err", err.Error()))
	}
}

// put is the package's one write to the bucket. exempt = counted but never refused (the tech-card
// export archive: a download must keep working at the cap).
func (b *Bucket) put(ctx context.Context, key string, r io.Reader, size int64, opts minio.PutObjectOptions, exempt bool) (minio.UploadInfo, error) {
	team, metered := b.meterTeam(ctx)
	var reserved int64
	if metered && !exempt {
		n := size
		if n < 0 {
			n = 0
		}
		if err := b.meter.Reserve(ctx, team, n, size < 0); err != nil {
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
	info, err := b.Client.PutObject(ctx, b.S3BucketName, key, r, size, opts)
	if err != nil {
		if metered {
			b.meterAdd(ctx, team, -reserved)
		}
		return info, err
	}
	if metered {
		b.meterAdd(ctx, team, info.Size-reserved)
	}
	return info, nil
}

// removeMetered removes one object and takes its size off the team's count.
func (b *Bucket) removeMetered(ctx context.Context, key string) error {
	team, metered := b.meterTeam(ctx)
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
		b.meterAdd(ctx, team, -size)
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
