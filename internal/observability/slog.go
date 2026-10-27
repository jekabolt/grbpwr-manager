package observability

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
)

// NewSlogHandler wraps inner so that records at Error and above are also sent to Sentry. inner
// still receives every record exactly as before. Disabled, it returns inner itself.
//
// (sentry-go's own slog module only ships Sentry Logs, not error events, hence this thin handler.)
func NewSlogHandler(inner slog.Handler) slog.Handler {
	if !Enabled() {
		return inner
	}
	return &slogHandler{inner: inner}
}

type slogHandler struct {
	inner  slog.Handler
	attrs  []slog.Attr // WithAttrs so far, keys already group-qualified
	groups []string
}

func (h *slogHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelError || h.inner.Enabled(ctx, l)
}

func (h *slogHandler) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if h.inner.Enabled(ctx, r.Level) {
		err = h.inner.Handle(ctx, r)
	}
	if r.Level >= slog.LevelError && !isReported(ctx) {
		h.capture(ctx, r)
	}
	return err
}

func (h *slogHandler) WithAttrs(as []slog.Attr) slog.Handler {
	next := &slogHandler{inner: h.inner.WithAttrs(as), groups: h.groups}
	next.attrs = append(append([]slog.Attr{}, h.attrs...), qualify(h.groups, as)...)
	return next
}

func (h *slogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &slogHandler{
		inner:  h.inner.WithGroup(name),
		attrs:  h.attrs,
		groups: append(append([]string{}, h.groups...), name),
	}
}

func qualify(groups []string, as []slog.Attr) []slog.Attr {
	if len(groups) == 0 {
		return as
	}
	prefix := strings.Join(groups, ".") + "."
	out := make([]slog.Attr, 0, len(as))
	for _, a := range as {
		out = append(out, slog.Attr{Key: prefix + a.Key, Value: a.Value})
	}
	return out
}

func (h *slogHandler) capture(ctx context.Context, r slog.Record) {
	extra := sentry.Context{}
	var errVal error
	add := func(a slog.Attr) {
		flatten(extra, "", a, &errVal)
	}
	for _, a := range h.attrs {
		add(a)
	}
	recAttrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool { recAttrs = append(recAttrs, a); return true })
	for _, a := range qualify(h.groups, recAttrs) {
		add(a)
	}
	// The gRPC logging interceptor writes every failed call as the same line ("finished call");
	// its method and code tell the issues apart.
	key := []string{"slog", r.Message}
	if m, ok := extra["grpc.method"].(string); ok && m != "" {
		svc, _ := extra["grpc.service"].(string)
		code, _ := extra["grpc.code"].(string)
		key = append(key, "/"+svc+"/"+m, code)
	}
	ok, suppressed := limits.allow("log:"+strings.Join(key[1:], "|"), logEvery, logBudgetPerHour, time.Now())
	if !ok {
		return
	}
	if suppressed > 0 {
		extra["suppressed_repeats"] = suppressed
	}

	hub := hubFrom(ctx)
	client := hub.Client()
	if client == nil {
		return
	}
	level := sentry.LevelError
	if r.Level > slog.LevelError {
		level = sentry.LevelFatal
	}
	var ev *sentry.Event
	if errVal != nil {
		ev = client.EventFromException(errVal, level)
		ev.Message = r.Message
	} else {
		ev = client.EventFromMessage(r.Message, level)
	}
	ev.Logger = "slog"
	ev.Contexts["log"] = extra
	if len(key) > 2 {
		ev.Message = strings.Join(append([]string{r.Message}, key[2:]...), " ")
	}
	// One issue per log line (per gRPC method and code), not per stack or per dynamic error text.
	ev.Fingerprint = key
	hub.CaptureEvent(ev)
}

// flatten writes a into dst under its group-qualified key; the first error value found is kept
// for the event's exception.
func flatten(dst map[string]any, prefix string, a slog.Attr, errVal *error) {
	v := a.Value.Resolve()
	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	if v.Kind() == slog.KindGroup {
		for _, ga := range v.Group() {
			flatten(dst, key, ga, errVal)
		}
		return
	}
	if key == "" {
		return
	}
	if v.Kind() == slog.KindAny {
		if e, ok := v.Any().(error); ok {
			if *errVal == nil {
				*errVal = e
			}
			dst[key] = e.Error()
			return
		}
		dst[key] = fmt.Sprint(v.Any())
		return
	}
	dst[key] = v.String()
}
