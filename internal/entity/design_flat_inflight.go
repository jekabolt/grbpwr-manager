package entity

import (
	"errors"
	"fmt"
)

// ErrDesignFlatRunInFlight — a flat run of this card is still pending or running, and a second one
// is refused (M8, 07.10): two tabs pressing GENERATE on one card booked two paid flat runs, the
// client's in-tab lock sees only its own tab. Checked by the store INSIDE StartRun's SERIALIZABLE
// transaction, before the day's reservation: the refusal reserves and charges nothing, and two
// concurrent presses cannot both pass it (the loser deadlocks, is retried, and sees the winner).
var ErrDesignFlatRunInFlight = errors.New("design: flat_run_in_flight")

// DesignFlatRunInFlightError carries the run that holds the card; errors.Is matches
// ErrDesignFlatRunInFlight.
type DesignFlatRunInFlightError struct {
	RunID  int
	Status string
}

func (e *DesignFlatRunInFlightError) Error() string {
	return fmt.Sprintf("%s: flat run %d of this card is %s", ErrDesignFlatRunInFlight, e.RunID, e.Status)
}

func (e *DesignFlatRunInFlightError) Unwrap() error { return ErrDesignFlatRunInFlight }
