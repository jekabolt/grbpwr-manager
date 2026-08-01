package entity

import "errors"

// ErrDuplicateDevExpense is returned when the development-cost journal is asked to record an expense
// byte-identical to one stamped moments earlier on the same tech card (same kind, note, amount,
// currency, fitting/sample links and incurred date). That is a re-submission — a client retry after a
// timeout, or a double-clicked Add — not a second real cost, and letting it through inflates
// net_after_dev in style economics. A genuinely repeated expense stays recordable: give it a note, a
// date or a different amount, or record it once the short dedupe window has passed.
var ErrDuplicateDevExpense = errors.New("duplicate development expense")
