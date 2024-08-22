package healthcheck

import (
	"errors"
)

// CategoryError provides a custom error type that also contains check category that emitted the error,
// useful when needed to distinguish between errors from multiple categories
type CategoryError struct {
	Category CategoryID
	Err      error
}

// Error satisfies the error interface for CategoryError.
func (e CategoryError) Error() string {
	return e.Err.Error()
}

// IsCategoryError returns true if passed in error is of type CategoryError and belong to the given category
func IsCategoryError(err error, categoryID CategoryID) bool {
	var ce CategoryError
	if errors.As(err, &ce) {
		return ce.Category == categoryID
	}
	return false
}

// SkipError is returned by a check in case this check needs to be ignored.
type SkipError struct {
	Reason string
}

// Error satisfies the error interface for SkipError.
func (e SkipError) Error() string {
	return e.Reason
}

// VerboseSuccess implements the error interface but represents a success with
// a message.
type VerboseSuccess struct {
	Message string
}

// Error satisfies the error interface for VerboseSuccess.  Since VerboseSuccess
// does not actually represent a failure, this returns the empty string.
func (e VerboseSuccess) Error() string {
	return ""
}
