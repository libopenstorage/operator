package component

// Code is the error code for errors in component lifecycle
type Code string

const (
	// ErrCritical code for a critical error
	ErrCritical Code = "Critical"
)

// Error error returned for component operations
type Error struct {
	errorCode     Code
	originalError error
}

// NewError creates a new custom component Error instance
func NewError(code Code, err error) error {
	return &Error{errorCode: code, originalError: err}
}

// Code returns the error code for the component error
func (e *Error) Code() Code {
	return e.errorCode
}

// Error returns the error message for the component error
func (e *Error) Error() string {
	return e.originalError.Error()
}
