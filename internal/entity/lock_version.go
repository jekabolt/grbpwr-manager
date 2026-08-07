package entity

// LockGuard is an optimistic-lock token that carries PRESENCE, not magnitude: it distinguishes
// «the caller sent no version» from «the caller sent version zero».
//
// Why that distinction is load-bearing (Ф6.5). A freshly created production_run is born at
// lock_version = 0, and 0120:9-10 declared 0 to mean «skip the check». Those two facts together
// left the FIRST edit of EVERY run unguarded — precisely the edit where a collision is most
// likely, because neither tab has saved yet and both are echoing the same 0 they read. The second
// writer silently clobbered the first, and the lock only started working from the second save on.
//
// Presence fixes that without inventing a sentinel (-1 would be the same disease in a new
// costume): an ABSENT token still opts out, a PRESENT token is enforced even when it is zero.
//
// The zero value is the ABSENT token, so a params struct that gains a LockGuard field keeps
// today's behaviour until a call site deliberately supplies one. The fields are unexported for
// exactly that reason: the only way to get a PRESENT token is to say so, via LockVersion.
type LockGuard struct {
	version int
	present bool
}

// LockVersion is a PRESENT token: the caller read this version and expects it to still hold. Zero
// is a legitimate value here — it is what every fresh run reports.
func LockVersion(v int) LockGuard { return LockGuard{version: v, present: true} }

// NoLockVersion is the ABSENT token — the LEGACY last-write-wins path.
//
// It exists for two callers that genuinely have no version to send: a pre-Ф6.5 client that omits
// the wire field entirely, and the deprecated ReceiveProductionRun shim, which synthesizes a
// receipt from the run's stored counts and never saw a client-supplied version.
//
// WHEN THIS CAN GO: once no caller omits the field — i.e. once the deprecated ReceiveProductionRun
// shim is deleted and the admin client's `optional expected_lock_version` has shipped everywhere
// (it already sends the version it read on every run write, so this is a matter of retiring the
// shim, not of changing the client). At that point make the field non-optional-but-required at the
// handler, delete this constructor, and delete the `if !g.Present()` legacy branches that read it.
func NoLockVersion() LockGuard { return LockGuard{} }

// LockGuardFromProto turns a proto3 `optional int32 expected_lock_version` into a token: a field
// the client SET — even to 0 — is present; an unset field is the legacy absent token.
func LockGuardFromProto(v *int32) LockGuard {
	if v == nil {
		return NoLockVersion()
	}
	return LockVersion(int(*v))
}

// Present reports whether the caller supplied a version at all.
func (g LockGuard) Present() bool { return g.present }

// Version is the supplied version. Meaningless unless Present.
func (g LockGuard) Version() int { return g.version }

// Conflicts reports whether the stored version contradicts what the caller expected. An ABSENT
// token never conflicts — that is the legacy opt-out, and it is now the ONLY thing that opts out.
func (g LockGuard) Conflicts(stored int) bool { return g.present && g.version != stored }
