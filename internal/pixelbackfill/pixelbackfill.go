// Package pixelbackfill fills media.pixel_preview (0488) for rows uploaded before the preview was
// generated at upload. It downloads each row's thumbnail and runs the SAME helper the upload path
// runs (bucket.PixelPreviewFromBytes), so an old row ends up with exactly what a new upload gets.
//
// Idempotent: the work list is "pixel_preview IS NULL" and the write is "… WHERE pixel_preview IS
// NULL", so a second run only retries rows that failed, and an upload racing the backfill wins.
package pixelbackfill

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
)

// maxThumbnailBytes caps one download. Thumbnails are ≤1080 px WebP, far below this.
const maxThumbnailBytes = 25 << 20

// Fetcher downloads one URL.
type Fetcher func(ctx context.Context, url string) ([]byte, error)

// Options steer one run.
type Options struct {
	Batch  int  // rows per page (default 50)
	Limit  int  // stop after this many candidate rows (0 = all)
	DryRun bool // download and compute, write nothing
	// Log receives one line per failed row (may be nil).
	Log func(format string, args ...any)
}

// Stats are the counts a run prints.
type Stats struct {
	Seen    int // candidate rows looked at
	Written int // rows updated (or, in dry-run, that would be)
	Skipped int // computed, but the row was filled meanwhile (upload or a parallel run)
	Failed  int // download or decode failed; row stays NULL
}

func (s Stats) String() string {
	return fmt.Sprintf("seen %d, written %d, skipped %d, failed %d", s.Seen, s.Written, s.Skipped, s.Failed)
}

// Run backfills the media store reachable through ctx (one team's database).
func Run(ctx context.Context, media dependency.Media, fetch Fetcher, o Options) (Stats, error) {
	var st Stats
	if o.Batch <= 0 {
		o.Batch = 50
	}
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	afterID := 0
	for {
		if o.Limit > 0 && st.Seen >= o.Limit {
			return st, nil
		}
		page := o.Batch
		if o.Limit > 0 && o.Limit-st.Seen < page {
			page = o.Limit - st.Seen
		}
		rows, err := media.ListMediaMissingPixelPreview(ctx, afterID, page)
		if err != nil {
			return st, err
		}
		if len(rows) == 0 {
			return st, nil
		}
		for _, r := range rows {
			if err := ctx.Err(); err != nil {
				return st, err
			}
			afterID = r.Id
			st.Seen++
			raw, err := fetch(ctx, r.Thumbnail)
			if err != nil {
				st.Failed++
				logf("media %d: download: %v", r.Id, err)
				continue
			}
			pp, err := bucket.PixelPreviewFromBytes(raw)
			if err != nil {
				st.Failed++
				logf("media %d: %v", r.Id, err)
				continue
			}
			if o.DryRun {
				st.Written++
				continue
			}
			ok, err := media.SetMediaPixelPreview(ctx, r.Id, pp)
			if err != nil {
				return st, err
			}
			if ok {
				st.Written++
			} else {
				st.Skipped++
			}
		}
	}
}

// HTTPFetcher downloads over plain HTTP(S) with a per-request timeout and a size cap.
func HTTPFetcher(timeout time.Duration) Fetcher {
	client := &http.Client{Timeout: timeout}
	return func(ctx context.Context, url string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("http %d", resp.StatusCode)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxThumbnailBytes+1))
		if err != nil {
			return nil, err
		}
		if len(raw) > maxThumbnailBytes {
			return nil, fmt.Errorf("thumbnail larger than %d bytes", maxThumbnailBytes)
		}
		return raw, nil
	}
}
