package push

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// PushSubRemovalRequest mirrors PushSubRemovalRequest.
type PushSubRemovalRequest struct {
	SubId uuid.UUID
}

// FlushBuffer mirrors FlushBufferService: per-type lock-guarded buffers with
// drain-to-working-queue semantics; on handler error the drained items are
// re-enqueued and the error rethrown.
type FlushBuffer[T any] struct {
	mu    sync.Mutex
	items []T
}

// NewFlushBuffer creates an empty buffer.
func NewFlushBuffer[T any]() *FlushBuffer[T] {
	return &FlushBuffer[T]{}
}

// Enqueue adds an item (the C# Enqueue<T>).
func (b *FlushBuffer[T]) Enqueue(item T) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items = append(b.items, item)
}

// Flush drains the buffer and calls handler with the working queue; on
// handler error the items are re-enqueued and the error is returned (the C#
// FlushAsync<T>).
func (b *FlushBuffer[T]) Flush(ctx context.Context, handler func(context.Context, []T) error) error {
	b.mu.Lock()
	if len(b.items) == 0 {
		b.mu.Unlock()
		return nil
	}
	working := b.items
	b.items = nil
	b.mu.Unlock()

	if err := handler(ctx, working); err != nil {
		b.mu.Lock()
		b.items = append(b.items, working...)
		b.mu.Unlock()
		return err
	}
	return nil
}

// GetPendingCount mirrors GetPendingCount<T>.
func (b *FlushBuffer[T]) GetPendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}
