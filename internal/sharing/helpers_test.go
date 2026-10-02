package sharing_test

import (
	"context"
	"time"
)

// contextWithTimeout keeps the deadline plumbing out of the test bodies.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
