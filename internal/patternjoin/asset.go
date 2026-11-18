// Package patternjoin reads the exact managed writer files used in a saved CAD Join.
package patternjoin

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
)

type ObjectStore interface {
	GetManagedObject(context.Context, string) (io.ReadCloser, int64, error)
}
type Asset struct {
	URL    string
	Size   int64
	SHA256 string
}
type DirectedEdge struct{ Curve, Start, End string }
type Selection struct {
	FixedInterval, MovingInterval        DirectedEdge
	CardID                               int
	FabricPurpose, BOMLineKey, SizeToken string
	SizeID                               int
	Fixed, Moving, Result                string
	FixedEdge, MovingEdge                int
	TranslateX, TranslateY               int64 // millionths of a millimetre, the ordinary writer precision
}

// Binding identifies the two exact objects and native blocks, without authorizing a card write.
type Binding struct {
	Original, Reviewed                     Asset
	Selection                              Selection
	OriginalBodySHA256, ReviewedBodySHA256 string
}

var invalid = errors.New("unsupported saved join pattern")
var chunk = regexp.MustCompile(`^GRBPWR-MANIFEST v1z ([1-9][0-9]{0,3})/([1-9][0-9]{0,3}) ([A-Za-z0-9+/=]{1,252})$`)
var canonicalSHA = regexp.MustCompile(`^[a-f0-9]{64}$`)
var identity = regexp.MustCompile(`^[A-Z]{2,6}$`)

const maxAsset = 4 << 20 // narrow single-rank Join profile, before opening or allocating
const maxManifest = 1 << 20

func ReadBinding(ctx context.Context, store ObjectStore, cfg *bucket.Config, original, reviewed Asset, s Selection) (Binding, error) {
	if s.CardID <= 0 || s.SizeID <= 0 || s.SizeToken == "" || s.FabricPurpose == "" || s.BOMLineKey == "" ||
		!identity.MatchString(s.Fixed) || !identity.MatchString(s.Moving) || !identity.MatchString(s.Result) || s.Fixed == s.Moving || s.Fixed == s.Result || s.Moving == s.Result || original.SHA256 == reviewed.SHA256 {
		return Binding{}, invalid
	}
	before, keyA, err := readObject(ctx, store, cfg, original)
	if err != nil {
		return Binding{}, err
	}
	after, keyB, err := readObject(ctx, store, cfg, reviewed)
	if err != nil {
		return Binding{}, err
	}
	if keyA == keyB {
		return Binding{}, invalid
	}
	a, err := readManifest(before, s, []string{s.Fixed, s.Moving})
	if err != nil {
		return Binding{}, err
	}
	b, err := readManifest(after, s, []string{s.Result})
	if err != nil {
		return Binding{}, err
	}
	if err := checkNativeJoin(a, b, s, original.SHA256); err != nil {
		return Binding{}, err
	}
	return Binding{original, reviewed, s, a.bodySHA, b.bodySHA}, nil
}

func readObject(ctx context.Context, store ObjectStore, cfg *bucket.Config, a Asset) ([]byte, string, error) {
	if len(a.URL) > 2048 || a.Size < 1 || a.Size > maxAsset || !canonicalSHA.MatchString(a.SHA256) {
		return nil, "", invalid
	}
	u, err := url.Parse(a.URL)
	if err != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return nil, "", invalid
	}
	key, err := bucket.ManagedObjectKeyFromURL(cfg, a.URL)
	if err != nil {
		return nil, "", invalid
	}
	prefix := "tech-card-patterns/"
	if base := strings.Trim(cfg.BaseFolder, "/"); base != "" {
		prefix = base + "/" + prefix
	}
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, ".dxf") || u.Path != "/"+key {
		return nil, "", invalid
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." || strings.Contains(part, "\\") {
			return nil, "", invalid
		}
	}
	r, n, err := store.GetManagedObject(ctx, key)
	if err != nil {
		return nil, "", err
	}
	defer r.Close()
	if n != a.Size {
		return nil, "", invalid
	}
	raw, err := io.ReadAll(io.LimitReader(r, n+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(raw)) != n || digest(raw) != a.SHA256 {
		return nil, "", invalid
	}
	return raw, key, nil
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type nativeFile struct {
	bodySHA, dialect      string
	blocks                map[string]*block
	sizeName, sourceLabel string
}
type manifest struct {
	V, TechCardID    int
	Generator, Units string
	CutLayerIsFinal  bool
	Scope            struct{ FabricPurpose, BOMLineKey string }
	Sizes            []struct {
		Name, SourceLabel string
		Token             string
		SizeID            int
		Rank              *int
	}
	Pieces []struct{ Identity string }
	Blocks []struct {
		Block, Identity, SizeToken string
		SizeID                     int
	}
	Gate struct{ Passed bool }
}

func readManifest(raw []byte, s Selection, ids []string) (*nativeFile, error) {
	rest := raw
	var encoded bytes.Buffer
	total := 0
	for i := 1; total == 0 || i <= total; i++ {
		code, next, ok := bytes.Cut(rest, []byte("\n"))
		if !ok || string(bytes.TrimSuffix(code, []byte("\r"))) != "999" {
			return nil, invalid
		}
		line, next, ok := bytes.Cut(next, []byte("\n"))
		if !ok || len(line) > 300 {
			return nil, invalid
		}
		m := chunk.FindSubmatch(bytes.TrimSuffix(line, []byte("\r")))
		if m == nil {
			return nil, invalid
		}
		n, _ := strconv.Atoi(string(m[1]))
		count, _ := strconv.Atoi(string(m[2]))
		if n != i || n > count || count > 1024 || (i != 1 && count != total) {
			return nil, invalid
		}
		total = count
		encoded.Write(m[3])
		rest = next
	}
	if bytes.Contains(rest, []byte("GRBPWR-MANIFEST")) {
		return nil, invalid
	}
	comp, err := base64.StdEncoding.Strict().DecodeString(encoded.String())
	if err != nil {
		return nil, invalid
	}
	stream := bytes.NewReader(comp)
	d := flate.NewReader(stream)
	p, err := io.ReadAll(io.LimitReader(d, maxManifest+1))
	closeErr := d.Close()
	if err != nil || closeErr != nil || len(p) > maxManifest || stream.Len() != 0 || !utf8.Valid(p) {
		return nil, invalid
	}
	if err = unambiguousJSON(p); err != nil {
		return nil, err
	}
	var m manifest
	if err = json.Unmarshal(p, &m); err != nil || m.V != 1 || m.Generator != "grbpwr-admin pattern-import (worker)" || m.Units != "mm" || !m.CutLayerIsFinal || !m.Gate.Passed || m.TechCardID != s.CardID ||
		m.Scope.FabricPurpose != s.FabricPurpose || m.Scope.BOMLineKey != s.BOMLineKey || len(m.Sizes) != 1 || m.Sizes[0].Token != s.SizeToken || m.Sizes[0].SizeID != s.SizeID || m.Sizes[0].Rank == nil || *m.Sizes[0].Rank != 0 || len(m.Pieces) != len(ids) || len(m.Blocks) != len(ids) {
		return nil, invalid
	}
	for _, id := range ids {
		pieces, blocks := 0, 0
		for _, q := range m.Pieces {
			if q.Identity == id {
				pieces++
			}
		}
		for _, q := range m.Blocks {
			if q.Identity == id && q.SizeID == s.SizeID && q.SizeToken == s.SizeToken && q.Block == id+"_"+s.SizeToken {
				blocks++
			}
		}
		if pieces != 1 || blocks != 1 {
			return nil, invalid
		}
	}
	blocks, dialect, err := inventory(rest, ids, s)
	if err != nil {
		return nil, err
	}
	if err := verifyManifestGeometry(p, blocks, s); err != nil {
		return nil, err
	}
	return &nativeFile{digest(rest), dialect, blocks, m.Sizes[0].Name, m.Sizes[0].SourceLabel}, nil
}

// encoding/json accepts duplicate and case-folded struct keys. Decline both before decoding.
func unambiguousJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func(int) error
	read = func(depth int) error {
		if depth > 64 {
			return invalid
		}
		t, err := d.Token()
		if err != nil {
			return invalid
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		if delim != '[' && delim != '{' {
			return invalid
		}
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				k, err := d.Token()
				name, ok := k.(string)
				if err != nil || !ok {
					return invalid
				}
				name = strings.ToLower(name)
				if seen[name] {
					return fmt.Errorf("%w: duplicate field", invalid)
				}
				seen[name] = true
			}
			if err := read(depth + 1); err != nil {
				return err
			}
		}
		_, err = d.Token()
		if err != nil {
			return invalid
		}
		return nil
	}
	if err := read(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid
	}
	return nil
}
