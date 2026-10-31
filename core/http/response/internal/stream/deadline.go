package stream

import (
	"errors"
	"net/http"
	"time"
)

// refreshDeadline preserves genuine causes while tolerating writers that cannot
// replace the server deadline, including wrapped capability errors.
func refreshDeadline(controller *http.ResponseController, timeout time.Duration) error {
	err := controller.SetWriteDeadline(time.Now().Add(timeout))

	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}

	return err
}
