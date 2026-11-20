// Package schemacompat decides which team databases a binary may serve (X3, tmp/plans/teams/lanes/X3-design.md).
//
// A team migration is additive and needed by default: older code survives it, and the code of its
// release needs it. Two declarations change that, both in the migration file:
//   - breaking: older code does not survive it (DROP / RENAME / CHANGE). The file name contains
//     "_breaking_" and the file carries "-- compat: breaking <reason>". A database that has it can
//     only be served by a binary that embeds it.
//   - lazy: "-- compat: lazy <reason>": the code of its release does not need it yet (an index, a
//     backfill, seeded reference rows); a team behind only lazy migrations stays open.
//
// The gate is then a window instead of equality: binary B serves team T when T's version is at
// least B's Need (its newest non-lazy migration) and T's min code (newest breaking migration
// applied to T) is at most B's Latest. Ids compare in sql-migrate's own order.
package schemacompat

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// BreakingMarker is the file-name marker of a breaking migration. It sits in the id, so it is
// also visible in gorp_migrations of every database the migration reached.
const BreakingMarker = "_breaking_"

const headerPrefix = "-- compat:"

var numberPrefix = regexp.MustCompile(`^(\d+)_`)

// Compare orders two migration ids the way sql-migrate does (Migration.Less): numeric prefixes
// numerically, numbered before unnumbered, then the plain string. -1, 0 or 1.
func Compare(a, b string) int {
	an, aok := idNumber(a)
	bn, bok := idNumber(b)
	switch {
	case aok && bok && an != bn:
		if an < bn {
			return -1
		}
		return 1
	case aok && !bok:
		return -1
	case !aok && bok:
		return 1
	}
	return strings.Compare(a, b)
}

func idNumber(id string) (int64, bool) {
	m := numberPrefix.FindStringSubmatch(id)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	return n, err == nil
}

// Number is the numeric prefix of id (0 when it has none).
func Number(id string) int64 {
	n, _ := idNumber(id)
	return n
}

// IsBreaking reports whether migration id is declared breaking by its name.
func IsBreaking(id string) bool { return strings.Contains(id, BreakingMarker) }

// Header is what a migration declares in "-- compat:" comment lines.
type Header struct {
	Breaking, Lazy bool
	Reason         string
}

// ParseHeader reads the "-- compat: breaking|lazy <reason>" lines of a migration body. A reason is
// required; an unknown word, both words, or a repeated line is an error.
func ParseHeader(body string) (Header, error) {
	var h Header
	seen := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, headerPrefix) {
			continue
		}
		if seen {
			return Header{}, fmt.Errorf("more than one %q line", headerPrefix)
		}
		seen = true
		word, reason, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, headerPrefix)), " ")
		reason = strings.TrimSpace(reason)
		switch word {
		case "breaking":
			h.Breaking = true
		case "lazy":
			h.Lazy = true
		default:
			return Header{}, fmt.Errorf("%s %q: want breaking or lazy", headerPrefix, word)
		}
		if reason == "" {
			return Header{}, fmt.Errorf("%s %s needs a reason", headerPrefix, word)
		}
		h.Reason = reason
	}
	return h, nil
}

// Migration is one embedded migration: its id and declarations.
type Migration struct {
	ID string
	Header
}

// Set is what a binary knows about its own migrations.
type Set struct {
	// Latest is the newest embedded migration ("" = none: nothing but team #1 is served).
	Latest string
	// Need is the newest non-lazy migration: a team below it is not served.
	Need string
	// Breaking lists the breaking migrations, oldest first.
	Breaking []string
}

// Build checks the declarations of migrations and returns their Set. A file name and its header
// must agree on breaking; breaking and lazy together is refused.
func Build(ms []Migration) (Set, error) {
	sorted := append([]Migration(nil), ms...)
	sort.SliceStable(sorted, func(i, j int) bool { return Compare(sorted[i].ID, sorted[j].ID) < 0 })
	var s Set
	for _, m := range sorted {
		if IsBreaking(m.ID) != m.Breaking {
			return Set{}, fmt.Errorf("migration %s: the name marker %q and the %q header disagree", m.ID, BreakingMarker, headerPrefix+" breaking")
		}
		if m.Breaking && m.Lazy {
			return Set{}, fmt.Errorf("migration %s: breaking and lazy at once", m.ID)
		}
		s.Latest = m.ID
		if !m.Lazy {
			s.Need = m.ID
		}
		if m.Breaking {
			s.Breaking = append(s.Breaking, m.ID)
		}
	}
	return s, nil
}

// MinCodeAt is the newest breaking migration at or below version: the oldest code that can serve
// a database at that version must embed it. "" when there is none.
func (s Set) MinCodeAt(version string) string {
	out := ""
	for _, b := range s.Breaking {
		if version != "" && Compare(b, version) <= 0 {
			out = b
		}
	}
	return out
}

// Ahead reports whether version is newer than anything this binary embeds (a rollback).
// A version without a numeric prefix is never ahead: it is not a migration id, so the fleet
// migrates the database and records a real one.
func (s Set) Ahead(version string) bool {
	if _, ok := idNumber(version); !ok {
		return false
	}
	return s.Latest != "" && Compare(version, s.Latest) > 0
}

// Servable reports whether this binary may serve a database at version with minCode. Team #1 is
// decided by the caller (always served).
func (s Set) Servable(version, minCode string) bool {
	if _, ok := idNumber(version); !ok || s.Latest == "" {
		return false // no version, or not a migration id: not served until the fleet records one
	}
	if Compare(version, s.Need) < 0 {
		return false // behind a migration this code needs
	}
	if minCode != "" && Compare(minCode, s.Latest) > 0 {
		return false // a breaking migration this code predates
	}
	return true
}

// Raise returns the larger of two min codes ("" is the smallest).
func Raise(cur, next string) string {
	if next == "" || (cur != "" && Compare(cur, next) >= 0) {
		return cur
	}
	return next
}

// AheadOnly reports whether the migrations applied to a database that this binary does not embed
// (unknown) are only newer, additive ones: every unknown id sorts after the binary's newest known
// id and none is breaking. Then a rollback binary may skip them instead of refusing to boot
// (sql-migrate's «unknown migration in database»). ok is false when there is nothing unknown.
func AheadOnly(known, applied []string) (unknown []string, ok bool) {
	newest := ""
	have := make(map[string]bool, len(known))
	for _, k := range known {
		have[k] = true
		if newest == "" || Compare(k, newest) > 0 {
			newest = k
		}
	}
	for _, a := range applied {
		if !have[a] {
			unknown = append(unknown, a)
		}
	}
	if len(unknown) == 0 {
		return nil, false
	}
	for _, u := range unknown {
		if IsBreaking(u) || (newest != "" && Compare(u, newest) <= 0) {
			return unknown, false
		}
	}
	return unknown, true
}
