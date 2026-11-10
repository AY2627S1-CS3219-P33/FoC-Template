package messaging

import "context"

// Hooks for the messaging_test package, which can import testfakes (testfakes
// imports this package, so in-package tests cannot).
var (
	TestDatabase  = testDatabase
	InsertMessage = insertMessage
	InsertCommand = insertCommand
	RowStatus     = rowStatus
)

// DispatchOnce claims and handles one batch of one family.
func (d *Dispatcher) DispatchOnce(ctx context.Context, credit bool) (bool, error) {
	return d.dispatch(ctx, credit)
}
