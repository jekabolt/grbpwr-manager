package oauth

import "net/url"

// ErrorKind is a stable provider failure category for HTTP handlers to map to
// their fixed public error keys.
type ErrorKind string

const (
	ErrorCancelled           ErrorKind = "cancelled"
	ErrorTokenInvalid        ErrorKind = "token_invalid"
	ErrorProviderUnreachable ErrorKind = "provider_unreachable"
)

// Error is intentionally free of provider response details and flow secrets.
type Error struct {
	Kind ErrorKind
}

func (e *Error) Error() string { return "oauth: " + string(e.Kind) }

// Is lets callers use errors.Is with the exported category values.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && e.Kind == other.Kind
}

var (
	ErrCancelled           = &Error{Kind: ErrorCancelled}
	ErrTokenInvalid        = &Error{Kind: ErrorTokenInvalid}
	ErrProviderUnreachable = &Error{Kind: ErrorProviderUnreachable}
)

// CallbackError maps an OAuth callback error to a stable category. It accepts
// either request query values or parsed form values and ignores descriptions.
func CallbackError(values url.Values) error {
	switch values.Get("error") {
	case "":
		return nil
	case "access_denied", "user_cancelled", "user_cancelled_authorize":
		return ErrCancelled
	default:
		return ErrProviderUnreachable
	}
}
