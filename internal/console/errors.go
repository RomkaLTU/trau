package console

import (
	"errors"
	"fmt"
)

// ActionableError wraps an underlying error with a human-readable explanation
// and an optional next-step suggestion. It is used at CLI boundaries so the
// final error message can tell the user what failed and what to do about it.
type ActionableError struct {
	Context    string
	Cause      error
	Suggestion string
}

func (e *ActionableError) Error() string {
	if e.Context != "" {
		return fmt.Sprintf("%s: %v", e.Context, e.Cause)
	}
	return e.Cause.Error()
}

// Unwrap returns the wrapped error so errors.Is/As still work.
func (e *ActionableError) Unwrap() error { return e.Cause }

// Actionable wraps cause with context and an optional fix-it hint.
func Actionable(cause error, context, suggestion string) error {
	if cause == nil {
		return nil
	}
	return &ActionableError{Context: context, Cause: cause, Suggestion: suggestion}
}

// FormatActionable renders an error for the terminal, including any suggestion.
func FormatActionable(err error) string {
	if err == nil {
		return ""
	}
	var a *ActionableError
	if errors.As(err, &a) && a.Suggestion != "" {
		return fmt.Sprintf("%v\n  → %s", a, a.Suggestion)
	}
	return err.Error()
}
