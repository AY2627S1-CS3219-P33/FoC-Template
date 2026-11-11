// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-20
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

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
