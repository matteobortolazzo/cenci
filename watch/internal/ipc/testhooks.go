package ipc

import "time"

// SetControlResponseDeadlineForTest shortens the reply deadline of
// SendBabysitStop/SendBabysitStatus and returns a function that restores the
// previous value. It exists only so tests in other packages (internal/babysit)
// can drive the held-open, never-replying connection without waiting the
// production 5s; production code must not call it.
func SetControlResponseDeadlineForTest(d time.Duration) (restore func()) {
	original := controlResponseDeadline
	controlResponseDeadline = d
	return func() { controlResponseDeadline = original }
}
