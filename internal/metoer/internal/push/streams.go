package push

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/metoer/internal/model"
)

// SopStream is an unbounded, non-blocking notification queue, mirroring the
// C# Channel.CreateUnbounded<SnNotification>() stream entries.
type SopStream struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []*model.Notification
	closed bool
}

func newSopStream() *SopStream {
	s := &SopStream{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// TryWrite appends a notification (the C# TryWrite on an unbounded channel
// always succeeds unless completed).
func (s *SopStream) TryWrite(n *model.Notification) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.items = append(s.items, n)
	s.cond.Signal()
	return true
}

// TryRead pops the next notification without blocking.
func (s *SopStream) TryRead() (*model.Notification, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return nil, false
	}
	n := s.items[0]
	s.items = s.items[1:]
	return n, true
}

// WaitToRead blocks until a notification is available, the stream is
// completed, or ctx is done (the C# WaitToReadAsync(ct) + TryRead loop).
// Returns true when data is available, false on completion/cancellation.
func (s *SopStream) WaitToRead(ctx context.Context) bool {
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.cond.Broadcast()
			s.mu.Unlock()
		case <-stop:
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.items) == 0 && !s.closed {
		s.cond.Wait()
		select {
		case <-ctx.Done():
			return false
		default:
		}
	}
	return len(s.items) > 0
}

// Complete marks the stream completed (the C# Writer.TryComplete).
func (s *SopStream) Complete() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		s.cond.Broadcast()
	}
}

// sopStreamEntry is the C# SopStreamSubscription record.
type sopStreamEntry struct {
	DeviceId string
	Stream   *SopStream
}

// SopStreams is the static in-process registry (ConcurrentDictionary of
// ConcurrentDictionaries) of live SOP streams.
type SopStreams struct {
	mu      sync.RWMutex
	streams map[uuid.UUID]map[uuid.UUID]*sopStreamEntry
}

// NewSopStreams creates the registry.
func NewSopStreams() *SopStreams {
	return &SopStreams{streams: map[uuid.UUID]map[uuid.UUID]*sopStreamEntry{}}
}

// Subscribe registers a stream for an account/device and returns the stream
// id plus the reader (SubscribeSopStream).
func (s *SopStreams) Subscribe(accountID uuid.UUID, deviceID string) (uuid.UUID, *SopStream) {
	streamID := uuid.New()
	entry := &sopStreamEntry{DeviceId: deviceID, Stream: newSopStream()}
	s.mu.Lock()
	defer s.mu.Unlock()
	accountStreams, ok := s.streams[accountID]
	if !ok {
		accountStreams = map[uuid.UUID]*sopStreamEntry{}
		s.streams[accountID] = accountStreams
	}
	accountStreams[streamID] = entry
	return streamID, entry.Stream
}

// Unsubscribe removes and completes a stream (UnsubscribeSopStream).
func (s *SopStreams) Unsubscribe(accountID, streamID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	accountStreams, ok := s.streams[accountID]
	if !ok {
		return
	}
	if entry, ok := accountStreams[streamID]; ok {
		delete(accountStreams, streamID)
		entry.Stream.Complete()
	}
	if len(accountStreams) == 0 {
		delete(s.streams, accountID)
	}
}

// Broadcast writes the notification to every live stream of the account
// (BroadcastSopStream; non-blocking).
func (s *SopStreams) Broadcast(accountID uuid.UUID, n *model.Notification) {
	s.mu.RLock()
	accountStreams, ok := s.streams[accountID]
	if !ok {
		s.mu.RUnlock()
		return
	}
	streams := make([]*SopStream, 0, len(accountStreams))
	for _, entry := range accountStreams {
		streams = append(streams, entry.Stream)
	}
	s.mu.RUnlock()
	for _, stream := range streams {
		stream.TryWrite(n)
	}
}

// ConnectedDeviceIds returns the normalized device ids of live streams for
// the account (GetConnectedSopWebSocketDeviceIds).
func (s *SopStreams) ConnectedDeviceIds(accountID uuid.UUID) map[string]struct{} {
	s.mu.RLock()
	accountStreams, ok := s.streams[accountID]
	if !ok {
		s.mu.RUnlock()
		return map[string]struct{}{}
	}
	out := make(map[string]struct{}, len(accountStreams))
	for _, entry := range accountStreams {
		out[NormalizeSopDeviceId(entry.DeviceId)] = struct{}{}
	}
	s.mu.RUnlock()
	return out
}
