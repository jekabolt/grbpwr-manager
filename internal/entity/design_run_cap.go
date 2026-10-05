package entity

import "time"

// ─────────────── THE WALL-CLOCK CAP OF AN IMAGE RUN (owner 05.10, run 148) ───────────────
//
// «нужен какой-то кэп по генерации и ошибку показывать, если не получилось». An image.generate run
// (flat, render, recolor, pattern, freeform — synchronous provider calls) that is not done CAP after
// it first started is closed as failed `timed_out` by the worker's overdue sweep, instead of
// spinning until its 20-minute lease runs out. The worker enforces the same cap on its own provider
// phase, so a live worker fails the run itself first; the sweep is for a worker that died.
//
// A run whose result was DELIVERED and is still landing gets LandingGrace more before it is closed
// — as `landing_failed`, never re-queued: re-queueing a delivered attempt buys the pictures again.

// DesignImageRunCapDefault — the cap when the deployment names none (DESIGN_IMAGE_RUN_CAP).
const DesignImageRunCapDefault = 6 * time.Minute

// Error codes of the overdue sweep and of the worker's own cap.
const (
	DesignErrorCodeTimedOut      = "timed_out"
	DesignErrorCodeLandingFailed = "landing_failed"
)

// DesignRunKindIsCapped — the kinds the wall-clock cap applies to: the image.generate kinds, whose
// provider call is synchronous. Cutout / extend / inpaint (fal queue), 3D and video (long async jobs)
// keep the lease path.
func DesignRunKindIsCapped(kind string) bool {
	return AIPurposeOfRunKind(kind) == AIPurposeImageGenerate
}

// DesignCappedRunKinds — every capped kind, in DesignRunKinds order.
func DesignCappedRunKinds() []string {
	var out []string
	for _, k := range DesignRunKinds() {
		if DesignRunKindIsCapped(k) {
			out = append(out, k)
		}
	}
	return out
}

// DesignOverdueSweep — one pass of the overdue sweep.
type DesignOverdueSweep struct {
	Kinds        []string
	Cap          time.Duration
	LandingGrace time.Duration
}
