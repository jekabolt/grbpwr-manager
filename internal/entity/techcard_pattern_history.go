package entity

import "errors"

// ErrTechCardPatternRevisionCurrent is returned when a history row still names the object attached
// to a live pattern row of the same card. Deleting the retained row in that state would present the
// current sheet as deletable history and is refused as a failed precondition.
var ErrTechCardPatternRevisionCurrent = errors.New("current pattern version cannot be deleted from history")
