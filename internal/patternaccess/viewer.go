// viewer.go is the card-level half of the tokenized pattern read path: one printed QR per
// fabric scope opens the admin SPA's public viewer page /p/{token}, which fetches the JSON
// manifest served here (GET /api/pv/{token}) and lets the operator pick a sheet, switch
// sizes and download. The card token ('c') authorises the MANIFEST only; every file inside
// it is addressed by its own /api/p url minted with ScopePrint — finally the scope's
// declared purpose — so per-file revocation keeps working underneath card revocation.
package patternaccess

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// unboundGroupKey is the pseudo-group for sheets whose binding resolves to nothing —
// «листы без привязки». Always sorted last; the client prints its own label for it.
const unboundGroupKey = "_unbound"

// MintCardViewerToken returns the ScopeCard token for a tech card, ensuring its access
// row exists (epoch 1 on first mint). Nil-receiver-safe and best-effort, exactly like
// FillTechCardPatternURLs: a viewer-token failure must never fail a GetTechCard read —
// the response simply carries no token and the печать falls back to per-sheet QR codes.
func (s *Service) MintCardViewerToken(ctx context.Context, techCardID int) string {
	if s == nil || techCardID <= 0 {
		return ""
	}
	row, err := s.objects.EnsureCardViewer(ctx, techCardID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "mint card viewer token failed",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return ""
	}
	return s.minter.Mint(patterntoken.ScopeCard, int64(techCardID), row.Epoch)
}

// ManifestHandler adapts ServeManifest for the http server wiring.
func (s *Service) ManifestHandler() http.Handler { return http.HandlerFunc(s.ServeManifest) }

// ServeManifest handles GET/HEAD /api/pv/{token}. Same security posture as ServeHTTP:
// the token authenticates by itself, every negative outcome is the same plain 404 (with
// the reason in the sampled audit log only), and the resolution is never cacheable.
func (s *Service) ServeManifest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := chi.URLParam(r, "token")
	ip := middleware.ClientIPFromRequest(r)

	notFound := func(reason string) {
		// Same sampling rationale as ServeHTTP's notFound: unauthenticated endpoint,
		// unmetered denial logging would be an amplifier.
		level := slog.LevelDebug
		if s.deniedSeq.Add(1)%deniedLogSample == 0 {
			level = slog.LevelInfo
		}
		slog.Default().Log(ctx, level, "pattern viewer denied",
			slog.String("reason", reason), slog.String("ip", ip),
			slog.String("ua", r.UserAgent()))
		http.NotFound(w, r)
	}

	if !s.viewerIPLimiter.Allow(ip) {
		notFound("ip rate limited")
		return
	}
	scope, id, epoch, err := s.minter.Parse(token)
	if err != nil {
		notFound("bad token")
		return
	}
	// Only the CARD scope opens a manifest. An object token ('i'/'p') carries a
	// pattern_object_access row id — accepted here it would leak the manifest of the
	// tech card that happens to share the number (the mirror image of the confusion
	// ServeHTTP's allowlist prevents).
	if scope != patterntoken.ScopeCard {
		notFound("wrong token scope")
		return
	}
	// Shares the object endpoint's limiter, but under a "c|"-prefixed key: card ids and
	// object ids overlap numerically, and a shared bucket would let requests against
	// card N eat the budget of object N (object keys are all-digit suffixes, so the
	// prefix cannot collide).
	if !s.tokenLimiter.Allow(ip + "|c|" + strconv.FormatInt(id, 10)) {
		notFound("token rate limited")
		return
	}
	techCardID := int(id)
	row, err := s.objects.GetCardViewer(ctx, techCardID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			notFound("no access row")
		} else {
			slog.Default().ErrorContext(ctx, "pattern viewer lookup failed", slog.String("err", err.Error()))
			notFound("lookup error")
		}
		return
	}
	now := time.Now().UTC()
	switch {
	case row.Epoch != epoch:
		notFound("stale epoch")
		return
	case row.RevokedAt.Valid:
		notFound("revoked")
		return
	case row.ExpiresAt.Valid && now.After(row.ExpiresAt.Time):
		notFound("expired")
		return
	}

	card, err := s.cards.GetPatternViewerManifest(ctx, techCardID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The access row outlived the card (FK cascade races the read); same 404.
			notFound("card gone")
		} else {
			slog.Default().ErrorContext(ctx, "pattern viewer manifest read failed", slog.String("err", err.Error()))
			notFound("manifest error")
		}
		return
	}

	// One ensure pass for every managed file on the card, like the fitting batch fill —
	// per-sheet ensures would put a query behind every sheet of every scan.
	refs := make([]entity.PatternObjectRef, 0, len(card.Sheets))
	for _, sh := range card.Sheets {
		if k, ok := storeutil.PatternObjectKey(sh.URL); ok {
			refs = append(refs, entity.PatternObjectRef{Key: k, Filename: sh.Filename})
		}
	}
	objectRows, err := s.objects.EnsureByKeys(ctx, refs)
	if err != nil {
		slog.Default().ErrorContext(ctx, "pattern viewer ensure objects failed", slog.String("err", err.Error()))
		notFound("ensure error")
		return
	}

	m := s.buildManifest(card, objectRows)

	slog.Default().InfoContext(ctx, "pattern viewer access",
		slog.Int("tech_card_id", techCardID), slog.String("ip", ip),
		slog.String("ua", r.UserAgent()))
	s.noteCardAccess(techCardID)

	// The token url is stable; the manifest behind it must not be cached by shared
	// caches (it embeds freshly minted per-file urls and reflects current revocation).
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m)
}

// Manifest is the /api/pv payload: plain snake_case JSON, not protobuf, following the
// ?mode=json hop — the consumer is a public SPA page with no proto runtime.
type Manifest struct {
	TechCardId  int             `json:"tech_card_id"`
	StyleNumber string          `json:"style_number"`
	StyleName   string          `json:"style_name"`
	Sizes       []ManifestSize  `json:"sizes"`
	Groups      []ManifestGroup `json:"groups"`
}

// ManifestSize is one size of the card's range; the viewer builds its graded-DXF size
// tokens from the names.
type ManifestSize struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
}

// ManifestGroup is one fabric scope as displayed: key is the назначение in SERVER
// spelling (lowercase, e.g. "main") when by_purpose, else a BOM line_key (with the
// line's display name in line_name), else the literal "_unbound".
type ManifestGroup struct {
	Key       string          `json:"key"`
	ByPurpose bool            `json:"by_purpose"`
	LineName  string          `json:"line_name"`
	Sheets    []ManifestSheet `json:"sheets"`
}

// ManifestSheet is one выкройка row. size_id 0 = graded/sizeless sheet. view_url and
// download_url are ScopePrint /api/p urls; empty when the stored url is not a managed
// object (the raw url is deliberately NOT exposed on this public surface).
type ManifestSheet struct {
	LineKey     string `json:"line_key"`
	Name        string `json:"name"`
	Filename    string `json:"filename"`
	Ext         string `json:"ext"`
	SizeId      int    `json:"size_id"`
	SizeName    string `json:"size_name"`
	SizeBytes   int64  `json:"size_bytes"`
	Version     int    `json:"version"`
	UploadedAt  string `json:"uploaded_at"`
	ViewUrl     string `json:"view_url"`
	DownloadUrl string `json:"download_url"`
}

// buildManifest assembles the payload: display-resolved groups, sheets in card order,
// per-file ScopePrint urls from the ensured object rows.
func (s *Service) buildManifest(card *entity.PatternViewerCard, objectRows map[string]entity.PatternObjectAccess) Manifest {
	// sizes[] is the card's RANGE — the viewer builds its graded-DXF block tokens from these
	// names, so a size the grade dropped must not appear here (it would invent a token the
	// files do not carry). A sheet still filed under such a size is named from its own joined
	// dictionary name instead (PatternViewerSheet.SizeName).
	sizes := make([]ManifestSize, 0, len(card.Sizes))
	for _, sz := range card.Sizes {
		sizes = append(sizes, ManifestSize{Id: sz.Id, Name: sz.Name})
	}

	defs, keyOfSheet := viewerGroups(card.BomLines)
	byKey := make(map[string]*ManifestGroup, len(defs))
	groups := make([]*ManifestGroup, 0, len(defs))
	for _, d := range defs {
		g := &ManifestGroup{Key: d.key, ByPurpose: d.byPurpose, LineName: d.lineName}
		byKey[d.key] = g
		groups = append(groups, g)
	}

	for _, sh := range card.Sheets {
		if strings.TrimSpace(sh.URL) == "" {
			// Not a sheet yet — a row without a file has nothing to view or download,
			// and the печать skips it the same way.
			continue
		}
		g, ok := byKey[keyOfSheet(sh)]
		if !ok {
			// viewerGroups only returns keys it defined; this branch is unreachable by
			// construction and exists so a future edit cannot turn it into a nil deref.
			g = byKey[unboundGroupKey]
		}
		var viewURL, downloadURL string
		if k, ok := storeutil.PatternObjectKey(sh.URL); ok {
			if row, ok := objectRows[k]; ok {
				tok := s.minter.Mint(patterntoken.ScopePrint, row.Id, row.Epoch)
				viewURL = s.viewerBaseURL + "/api/p/" + tok
				downloadURL = viewURL + "?dl=1"
			}
		}
		uploadedAt := ""
		if sh.UploadedAt.Valid {
			uploadedAt = sh.UploadedAt.Time.UTC().Format(time.RFC3339)
		}
		g.Sheets = append(g.Sheets, ManifestSheet{
			LineKey:     sh.LineKey,
			Name:        sh.Name,
			Filename:    sh.Filename,
			Ext:         sheetExt(sh.Filename, sh.URL),
			SizeId:      sh.SizeId,
			SizeName:    sh.SizeName,
			SizeBytes:   sh.SizeBytes,
			Version:     sh.Version,
			UploadedAt:  uploadedAt,
			ViewUrl:     viewURL,
			DownloadUrl: downloadURL,
		})
	}

	m := Manifest{
		TechCardId:  card.TechCardId,
		StyleNumber: card.StyleNumber,
		StyleName:   card.StyleName,
		Sizes:       sizes,
		Groups:      make([]ManifestGroup, 0, len(groups)),
	}
	for _, g := range groups {
		// A group with no sheets is omitted: the печать emits one QR per group it can
		// see, so an empty group here would be a QR onto nothing.
		if len(g.Sheets) == 0 {
			continue
		}
		m.Groups = append(m.Groups, *g)
	}
	return m
}

type viewerGroupDef struct {
	key       string
	byPurpose bool
	lineName  string
}

// viewerGroups reproduces the ADMIN PANEL's display grouping (bom-purpose.ts
// fabricScopes/scopeKeyOfBinding) — the печать prints one QR per client group with a
// caption, and the group this manifest shows behind that QR must have the same
// membership or the caption lies.
//
// Groups come from the card's roll-goods BOM LINES, not from the sheets: one group per
// назначение actually present on those lines, then one group per line that has no
// назначение (keyed by its line_key, labelled with its name), then _unbound. A sheet
// resolves to: its own fabric_purpose IF some line carries it; else the group owning its
// bom_line_key (that line's purpose group when the line has one, else the line's own
// group); else _unbound.
//
// This deliberately does NOT match entity.ResolveFabricScope, whose purpose wins even
// when no line carries it. That rule is right for STORAGE buckets (scope_key, the 0280
// size index — a bucket must not move when somebody clears a назначение) and wrong here,
// where the question is «what does the operator see on the patterns panel»: the panel
// shows a purpose no line carries as unbound, and this endpoint must agree with the
// panel, not with the index. Do not "fix" this into calling ResolveFabricScope.
//
// Order: purpose groups at their first line's position, then unsorted-line groups in BOM
// order, then _unbound — deterministic because BOM order is; it only decides which group
// the viewer preselects when the QR carries no ?g.
func viewerGroups(lines []entity.PatternViewerBomLine) ([]viewerGroupDef, func(entity.PatternViewerSheet) string) {
	// Purposes are a closed lowercase vocabulary (chk_bom_item_purpose / chk_tcsp_fabric_purpose),
	// but every OTHER resolver in the codebase folds case anyway (entity.ResolveFabricScope), and
	// an unfolded comparison here would send a stray "Main" to _unbound while the rest of the
	// system resolves it — a disagreement invisible until it happens. Fold on both sides and key
	// the group by the folded value, which is also the spelling the печать writes into ?g.
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

	purposes := make(map[string]bool, len(lines))
	// Line keys are uppercased ULIDs on entry; fold case on the LOOKUP side only
	// (sheetSetDiff precedent) and keep the stored spelling as the group key.
	lineByKey := make(map[string]entity.PatternViewerBomLine, len(lines))
	for _, l := range lines {
		lk := strings.ToUpper(strings.TrimSpace(l.LineKey))
		if _, dup := lineByKey[lk]; lk != "" && !dup {
			lineByKey[lk] = l
		}
	}
	for _, l := range lines {
		// A line with no line_key is INVISIBLE to the panel (bom-purpose.ts fabricScopes skips
		// it), so it must not conjure a group here either: the печать would caption those sheets
		// «без привязки» while the manifest filed them under a purpose. The write path has minted
		// keys since 0260 and 0159 back-filled the rest, but uniq_bom_line_key still permits
		// several NULLs, so this stays a guard rather than an assumption.
		if strings.TrimSpace(l.LineKey) == "" {
			continue
		}
		if p := norm(l.Purpose); p != "" && !purposes[p] {
			purposes[p] = true
		}
	}
	// Purpose groups come out in the VOCABULARY's order, not in BOM order, because that is the
	// order the panel and the печать use (bom-purpose.ts bomPurposeOrder, which is this same
	// list). Order decides only which group the viewer preselects when a url carries no ?g —
	// but agreeing costs one loop, and a divergence here is one more thing to reason about.
	var defs []viewerGroupDef
	for _, p := range entity.BomPurposeOrder {
		if purposes[string(p)] {
			defs = append(defs, viewerGroupDef{key: string(p), byPurpose: true})
		}
	}
	// A purpose this build does not know (a card written by a newer server) still has to show
	// its sheets rather than vanish from the manifest that claims to list them — same rule, and
	// the same alphabetical tail, as the panel's unknown-purpose branch.
	unknown := make([]string, 0)
	for p := range purposes {
		if !entity.ValidTechCardBomPurposes[entity.TechCardBomPurpose(p)] {
			unknown = append(unknown, p)
		}
	}
	sort.Strings(unknown)
	for _, p := range unknown {
		defs = append(defs, viewerGroupDef{key: p, byPurpose: true})
	}
	seenLine := make(map[string]bool, len(lines))
	for _, l := range lines {
		lk := strings.TrimSpace(l.LineKey)
		if lk == "" || norm(l.Purpose) != "" || seenLine[strings.ToUpper(lk)] {
			continue
		}
		seenLine[strings.ToUpper(lk)] = true
		defs = append(defs, viewerGroupDef{key: lk, lineName: l.Name})
	}
	defs = append(defs, viewerGroupDef{key: unboundGroupKey})

	keyOf := func(sh entity.PatternViewerSheet) string {
		if p := norm(sh.FabricPurpose); p != "" {
			if purposes[p] {
				return p
			}
			return unboundGroupKey
		}
		if lk := strings.TrimSpace(sh.BomLineKey); lk != "" {
			l, ok := lineByKey[strings.ToUpper(lk)]
			if !ok {
				return unboundGroupKey
			}
			if p := norm(l.Purpose); p != "" {
				return p
			}
			return strings.TrimSpace(l.LineKey)
		}
		return unboundGroupKey
	}
	return defs, keyOf
}

// sheetExt derives the lowercase extension (no dot) the viewer branches its renderer on
// (pdf → <object>, dxf → SVG contours). Filename first — it is the upload's human name —
// with the object url's path as the legacy fallback.
func sheetExt(filename, rawURL string) string {
	if e := strings.TrimPrefix(path.Ext(filename), "."); e != "" {
		return strings.ToLower(e)
	}
	if u, err := url.Parse(rawURL); err == nil {
		if e := strings.TrimPrefix(path.Ext(u.Path), "."); e != "" {
			return strings.ToLower(e)
		}
	}
	return ""
}
