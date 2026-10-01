// Package email defines outbound email contracts.
package email

import "context"

// Sender will deliver email through a future provider adapter.
type Sender interface {
	Send(ctx context.Context, recipient, subject, body string) error
}

// TODO: Define templates and delivery behavior when email workflows are implemented.
