// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service notification business logic.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Package notification holds the notification aggregate and use cases. A
// notification is one user-facing fact about an errand, addressed to exactly
// one recipient (an Auth0 subject). The message layer turns an errand event
// into zero or more Pending notifications; the HTTP layer reads a recipient's
// own notifications back.
package notification

import "time"

// Notification is a stored, user-facing fact delivered to one recipient.
type Notification struct {
	// ID is the service-assigned identifier used to mark one read.
	ID string
	// Recipient is the Auth0 subject the notification belongs to.
	Recipient string
	// ErrandID ties the notification to the errand it is about.
	ErrandID string
	// Type is the source routing key, e.g. errand.delivered.
	Type  string
	Title string
	Body  string
	// CreatedAt is the event's occurredAt, not the row's insert time, so the
	// order a user sees matches the order things happened.
	CreatedAt time.Time
	// ReadAt is set once the recipient has marked it read; nil while unread.
	ReadAt *time.Time
}

// Pending is a notification the message layer wants stored. It carries no ID
// or read state: the store assigns the ID and every Pending starts unread.
type Pending struct {
	Recipient string
	ErrandID  string
	Type      string
	Title     string
	Body      string
	CreatedAt time.Time
}
