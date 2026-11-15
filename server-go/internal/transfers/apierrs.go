package transfers

import (
	"errors"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// statusOf is an error's HTTP status, 0 for one without.
func statusOf(err error) int {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// messageOf is an error's text for a person.
func messageOf(err error) string {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}
