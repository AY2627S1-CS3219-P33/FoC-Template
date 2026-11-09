package creditmsg

import "time"

// SetRetryDelay shortens the wait on the retry queue for a test, before the
// topology is declared. The returned function restores the old value. The
// queue argument cannot change on a broker that already has the queue, so a
// test must delete QueueCommandsRetry around it.
func SetRetryDelay(d time.Duration) (restore func()) {
	old := retryDelay
	retryDelay = d
	return func() { retryDelay = old }
}
