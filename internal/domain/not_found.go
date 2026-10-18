package domain

import "fmt"

// NotFoundError distinguishes confirmed deletion from an unavailable source.
// Read projections must never replace a valid view with a transient 404.
type NotFoundError struct{ Message string }

func (e *NotFoundError) Error() string { return e.Message }
func NotFoundf(format string, args ...any) error {
	return &NotFoundError{Message: fmt.Sprintf(format, args...)}
}
