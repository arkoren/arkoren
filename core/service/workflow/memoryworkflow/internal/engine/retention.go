package engine

import (
	"time"
)

// defaultRetention is how long a finished run stays when the workflow sets no
// [workflow.Builder.Retention].
const defaultRetention = time.Hour

// forgetAfter removes the finished run after its retention, or when the driver
// stops.
func (driver *Engine) forgetAfter(r *run, retention time.Duration) {
	if retention <= 0 {
		retention = defaultRetention
	}

	timer := time.NewTimer(retention)
	defer timer.Stop()

	select {
	case <-timer.C:
		driver.mu.Lock()
		defer driver.mu.Unlock()

		if key := (runKey{
			name: r.name,
			id:   r.id,
		}); driver.runs[key] == r {
			delete(driver.runs, key)
		}
	case <-driver.root.Done():
	}
}
