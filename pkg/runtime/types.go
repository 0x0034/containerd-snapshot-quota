package runtime

import "context"

// Watcher monitors container lifecycle events and triggers quota operations.
type Watcher interface {
	Start(ctx context.Context) error
	Stop() error
	IsRunning() bool
}
