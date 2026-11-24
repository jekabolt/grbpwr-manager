package shop

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// originsTTL bounds how long a newly attached or removed host waits for the CORS allow-list on an
// instance that did not make the change.
const originsTTL = 30 * time.Second

// Origins is the CORS allow-list of the storefront API for team shops: https://{label}.{zone} (the
// platform addresses) and https://{host} for every host of an open shop (custom domains). The host
// list is read from the control plane at most once per originsTTL; a failed read keeps the last list.
type Origins struct {
	zone string
	list func(ctx context.Context) ([]string, error)
	now  func() time.Time

	mu      sync.Mutex
	hosts   map[string]bool
	expires time.Time
}

// NewOrigins builds the allow-list. zone is the shop zone ("" = no platform addresses); list reads
// the open shops' hosts (platformstore.Store.OpenShopHosts).
func NewOrigins(zone string, list func(ctx context.Context) ([]string, error)) *Origins {
	return &Origins{zone: NormHost(zone), list: list, now: time.Now, hosts: map[string]bool{}}
}

// Allowed reports whether origin may call the storefront API as a shop. Only https.
func (o *Origins) Allowed(origin string) bool {
	if o == nil {
		return false
	}
	origin = strings.TrimSpace(origin)
	if !strings.HasPrefix(strings.ToLower(origin), "https://") {
		return false
	}
	h := OriginHost(origin)
	if h == "" {
		return false
	}
	if o.zone != "" && strings.HasSuffix(h, "."+o.zone) {
		label := strings.TrimSuffix(h, "."+o.zone)
		if label != "" && !strings.Contains(label, ".") {
			return true
		}
	}
	return o.current()[h]
}

// Invalidate makes the next Allowed read the host list again.
func (o *Origins) Invalidate() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.expires = time.Time{}
	o.mu.Unlock()
}

func (o *Origins) current() map[string]bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	if now.Before(o.expires) || o.list == nil {
		return o.hosts
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	hosts, err := o.list(ctx)
	if err != nil {
		slog.Default().WarnContext(ctx, "shop: origins list", slog.String("err", err.Error()))
		o.expires = now.Add(originsTTL / 6) // retry soon, keep serving the last list
		return o.hosts
	}
	m := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		m[NormHost(h)] = true
	}
	o.hosts, o.expires = m, now.Add(originsTTL)
	return o.hosts
}
