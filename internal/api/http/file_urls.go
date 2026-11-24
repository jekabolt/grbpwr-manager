package httpapi

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// FileURLRewriter is what the file url interceptor needs from the bucket (lane X13, private team
// files): the needle every url of the caller team's private files carries, the signer of such urls
// inside any string, and the inverse that cuts our signature off again. *bucket.Bucket implements it.
type FileURLRewriter interface {
	PrivateURLNeedle(ctx context.Context) (string, bool)
	SignFileURLs(ctx context.Context, s string) (string, bool)
	CanonicalFileURLs(s string) (string, bool)
}

// SetFileURLRewriter turns on signed links for private team files on every unary call. Set at boot,
// before Start, and only in teams mode; nil (the default) leaves every call as it is.
func (s *Server) SetFileURLRewriter(r FileURLRewriter) {
	s.fileURLs = r
}

// fileURLUnary signs the private team file urls of a response and un-signs those of a request.
// A pass-through until SetFileURLRewriter is called.
func (s *Server) fileURLUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if s.fileURLs == nil {
			return handler(ctx, req)
		}
		return fileURLCall(ctx, s.fileURLs, req, handler)
	}
}

// signSkip lists the response fields the signer leaves bare. Each one IDENTIFIES a saved row (or is
// part of a sealed source): its value is sealed into canonical bytes (the saved Assembly source seal,
// the semantic sidecar bindings) or compared byte for byte with the stored row, by the server on the
// way back and by the client between reads. A signature differs per team and per window, so a signed
// copy breaks every one of those seals for every team other than #1. The file itself reaches the
// client through download_url / view_url (/api/p) or the authenticated proxy, never this url.
// A message-typed entry leaves its whole subtree as is. The names are checked against the registry
// by a test, so a renamed field fails loudly instead of being signed again.
var signSkip = map[protoreflect.FullName]struct{}{
	// The eight-field saved DXF row identity (snapshot row, complete-source rows, guarded joins).
	"admin.TechCardSavedPatternRowIdentity.url": {},
	// The card's stored pattern url: the post-save receipt, the sidecar persistence.pattern.url and
	// the placement patternUrl are built from it and compared with the saved row.
	"common.TechCardSizePattern.url": {},
	// The sealed parts of the saved Assembly read: complete_source_v1.canonical_json and the topology
	// seal cover these lists in full, free text included.
	"admin.GetTechCardSavedAssemblySnapshotResponse.operations":              {},
	"admin.GetTechCardSavedAssemblySnapshotResponse.selected_seams":          {},
	"admin.GetTechCardSavedAssemblySnapshotResponse.complete_source_v1":      {},
	"admin.GetTechCardSavedAssemblySnapshotResponse.authored_fold_source_v1": {},
}

func skipSigning(fd protoreflect.FieldDescriptor) bool {
	_, ok := signSkip[fd.FullName()]
	return ok
}

// fileURLCall is one call through the rewriter.
//
// The request is canonicalized IN PLACE (it was decoded for this call alone): a signed url the client
// sends back becomes the stored url, so rows never hold a signature and write-side host checks match.
//
// The response is never changed in place: a handler may answer from a cache or a shared value. Only
// when some string carries the caller team's private prefix is it cloned and the clone signed; any
// other response (team #1, no team, no team file in it) is returned as is.
func fileURLCall(ctx context.Context, fx FileURLRewriter, req any, handler grpc.UnaryHandler) (any, error) {
	if m, ok := req.(proto.Message); ok && m != nil {
		// Every field, skip-listed ones included: a tab loaded before the skip list may still send a
		// signed identity url back, and the stored form is what it must compare with.
		rewriteStrings(m.ProtoReflect(), fx.CanonicalFileURLs, nil)
	}
	resp, err := handler(ctx, req)
	if err != nil || resp == nil {
		return resp, err
	}
	needle, ok := fx.PrivateURLNeedle(ctx)
	if !ok {
		return resp, err
	}
	m, ok := resp.(proto.Message)
	if !ok || !anyString(m.ProtoReflect(), func(s string) bool { return strings.Contains(s, needle) }, skipSigning) {
		return resp, err
	}
	out := proto.Clone(m)
	rewriteStrings(out.ProtoReflect(), func(s string) (string, bool) { return fx.SignFileURLs(ctx, s) }, skipSigning)
	return out, nil
}

// rewriteStrings applies f to every string set in m: fields, list elements, map values and nested
// messages at any depth (map keys stay). A field skip reports true for is left as is, subtree and
// all (nil skips nothing). It reports whether anything changed.
func rewriteStrings(m protoreflect.Message, f func(string) (string, bool), skip func(protoreflect.FieldDescriptor) bool) bool {
	changed := false
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if skip != nil && skip(fd) {
			return true
		}
		switch {
		case fd.IsList():
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				switch fd.Kind() {
				case protoreflect.StringKind:
					if s, ok := f(l.Get(i).String()); ok {
						l.Set(i, protoreflect.ValueOfString(s))
						changed = true
					}
				case protoreflect.MessageKind, protoreflect.GroupKind:
					changed = rewriteStrings(l.Get(i).Message(), f, skip) || changed
				}
			}
		case fd.IsMap():
			mp := v.Map()
			switch fd.MapValue().Kind() {
			case protoreflect.StringKind:
				type set struct {
					k protoreflect.MapKey
					s string
				}
				var sets []set
				mp.Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
					if s, ok := f(mv.String()); ok {
						sets = append(sets, set{k, s})
					}
					return true
				})
				for _, x := range sets {
					mp.Set(x.k, protoreflect.ValueOfString(x.s))
					changed = true
				}
			case protoreflect.MessageKind, protoreflect.GroupKind:
				mp.Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
					changed = rewriteStrings(mv.Message(), f, skip) || changed
					return true
				})
			}
		case fd.Kind() == protoreflect.StringKind:
			if s, ok := f(v.String()); ok {
				m.Set(fd, protoreflect.ValueOfString(s))
				changed = true
			}
		case fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind:
			changed = rewriteStrings(v.Message(), f, skip) || changed
		}
		return true
	})
	return changed
}

// anyString reports whether some string set in m (same reach and skip as rewriteStrings) satisfies pred.
func anyString(m protoreflect.Message, pred func(string) bool, skip func(protoreflect.FieldDescriptor) bool) bool {
	found := false
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if skip != nil && skip(fd) {
			return true
		}
		switch {
		case fd.IsList():
			l := v.List()
			for i := 0; i < l.Len() && !found; i++ {
				switch fd.Kind() {
				case protoreflect.StringKind:
					found = pred(l.Get(i).String())
				case protoreflect.MessageKind, protoreflect.GroupKind:
					found = anyString(l.Get(i).Message(), pred, skip)
				}
			}
		case fd.IsMap():
			kind := fd.MapValue().Kind()
			v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				switch kind {
				case protoreflect.StringKind:
					found = pred(mv.String())
				case protoreflect.MessageKind, protoreflect.GroupKind:
					found = anyString(mv.Message(), pred, skip)
				}
				return !found
			})
		case fd.Kind() == protoreflect.StringKind:
			found = pred(v.String())
		case fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind:
			found = anyString(v.Message(), pred, skip)
		}
		return !found
	})
	return found
}
