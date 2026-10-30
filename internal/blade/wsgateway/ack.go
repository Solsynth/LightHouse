package wsgateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/go/proto"
	"srv.solsynth.dev/sosys/blade/logging"
)

const (
	// PacketTypeAck acknowledges one ack-tracked egress frame.
	PacketTypeAck = "session.ack"
	// PacketTypeResume asks for every frame still pending for this
	// account+device. It is the fast path after a reconnect.
	PacketTypeResume = "session.resume"

	// ackedPacketPrefix is the only packet family that gets ack tracking: its
	// client consumers key on the entity id, so re-delivery is idempotent
	// client-side.
	ackedPacketPrefix = "messages."

	ackDefaultRetry       = 3 * time.Second
	ackDefaultMaxAttempts = 20
	// ackMaxResumeReplay caps how many pending frames one session.resume may
	// replay. Without it a client that reconnects in a loop re-receives the
	// whole buffer on every connect, which is far more traffic than the
	// reconciler ever produces.
	ackMaxResumeReplay = 64
	// ackReplayQueueWait bounds how long one replayed frame waits for queue
	// space before it is left pending for the next reconciler pass.
	ackReplayQueueWait   = 2 * time.Second
	ackReconcileInterval = 3 * time.Second
	ackDefaultRetention  = 600 * time.Second
	ackDefaultMaxPending = 256
	// ackPendingWarnRatio triggers the "client not acking" warning.
	ackPendingWarnRatio = 0.9
)

// PendingPacket is one unacknowledged egress frame. Frame holds the exact JSON
// bytes that were sent to the client, so a replay is byte-identical.
type PendingPacket struct {
	AckID     string    `json:"ack_id"`
	DeviceID  string    `json:"device_id"`
	Frame     []byte    `json:"frame"`
	CreatedAt time.Time `json:"created_at"`
	Attempts  int       `json:"attempts"`
	NextTryAt time.Time `json:"next_try_at"`
}

// PacketAckTracker persists unacknowledged egress frames. Implementations must
// be safe for concurrent use and tolerate garbage: an acked or expired ack id
// is a no-op, never an error.
type PacketAckTracker interface {
	// Append stores entry and returns the pending depth for the account.
	Append(ctx context.Context, namespace, accountID string, entry PendingPacket) (int, error)
	// Ack removes the entry for ackID and reports whether it was still pending.
	Ack(ctx context.Context, namespace, accountID, ackID string) (found bool, err error)
	// List returns the pending entries in append (send) order.
	List(ctx context.Context, namespace, accountID string) ([]PendingPacket, error)
	// CountAttempt bumps the attempt counter and schedules the next try.
	CountAttempt(ctx context.Context, namespace, accountID, ackID string, nextTry time.Time) error
	// Drop removes several entries at once.
	Drop(ctx context.Context, namespace, accountID string, ackIDs []string) error
}

// ackTrackerLimits is an optional tracker capability: the reconciler takes its
// bounds from the tracker so the drop and warn decisions match what the
// tracker actually enforces.
type ackTrackerLimits interface {
	Retention() time.Duration
	MaxPending() int
}

// SetAckTracker enables at-least-once delivery for ack-tracked pushes. It is
// optional: without a tracker every push keeps today's fire-and-forget
// behaviour, which is what unit tests and Redis-less replicas get.
func (s *Service) SetAckTracker(tracker PacketAckTracker) { s.acks = tracker }

// SetAckRetryInterval overrides the reconciler backoff (3s by default). Values
// at or below zero are ignored.
func (s *Service) SetAckRetryInterval(interval time.Duration) {
	if interval <= 0 {
		return
	}
	s.ackRetry = interval
}

// SetAckMaxAttempts overrides how many re-sends one pending frame gets before
// it is dropped. Values at or below zero are ignored.
func (s *Service) SetAckMaxAttempts(attempts int) {
	if attempts <= 0 {
		return
	}
	s.ackMaxAttempts = attempts
}

// ackAttemptBudget is the re-send budget for one pending frame.
func (s *Service) ackAttemptBudget() int {
	if s.ackMaxAttempts > 0 {
		return s.ackMaxAttempts
	}
	return ackDefaultMaxAttempts
}

func (s *Service) ackRetryInterval() time.Duration {
	if s.ackRetry > 0 {
		return s.ackRetry
	}
	return ackDefaultRetry
}

func (s *Service) ackLimits() (time.Duration, int) {
	retention := ackDefaultRetention
	maxPending := ackDefaultMaxPending
	if limits, ok := s.acks.(ackTrackerLimits); ok {
		if r := limits.Retention(); r > 0 {
			retention = r
		}
		if m := limits.MaxPending(); m > 0 {
			maxPending = m
		}
	}
	return retention, maxPending
}

// shouldTrack reports whether this push must be stored until acked.
//
// It requires the client to have proven it speaks the ack protocol. Without
// that gate a client that never acks (an un-upgraded build) would get an
// _ack_id it ignores, a stored entry, and every retry of the budget for each
// frame — pure amplification against a client that is behaving exactly as it
// was written to.
func (s *Service) shouldTrack(entry *wsConnection, packet *gen.DyWebSocketPacket) bool {
	if s.acks == nil || entry == nil || packet == nil {
		return false
	}
	if entry.getAccountID() == "" {
		return false
	}
	if !entry.supportsAcks() {
		return false
	}
	return strings.HasPrefix(packet.GetType(), ackedPacketPrefix)
}

// enqueueTracked is the push egress for every target connection. Ack-tracked
// families are persisted (with a fresh _ack_id per connection) and then written
// as JSON, because the protobuf form cannot carry the ack id. Everything else
// goes straight through the untracked proto path.
func (s *Service) enqueueTracked(entry *wsConnection, packet *gen.DyWebSocketPacket, priority bool) error {
	if entry == nil || packet == nil {
		return errors.New("websocket connection and packet are required")
	}
	if !s.shouldTrack(entry, packet) {
		if priority {
			return entry.sendProtoPriority(packet)
		}
		return entry.sendProto(packet)
	}

	accountID := entry.getAccountID()
	frame := Packet{
		Type: packet.GetType(),
		Data: json.RawMessage(packet.GetData()),
		// errorMessage is a wrapper on the proto side, absent when unset.
		ErrorMessage: packet.GetErrorMessage().GetValue(),
		AckID:        uuid.NewString(),
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}

	now := time.Now()
	// Persist before writing: a client that acks instantly must find the entry.
	if _, err := s.acks.Append(context.Background(), entry.namespace, accountID, PendingPacket{
		AckID:     frame.AckID,
		DeviceID:  entry.deviceID,
		Frame:     raw,
		CreatedAt: now,
		NextTryAt: now.Add(s.ackRetryInterval()),
	}); err != nil {
		// Losing the tracker write must not lose the live delivery; it only
		// loses the retry that would have followed.
		logging.Log.Warn().
			Err(err).
			Str("namespace", entry.namespace).
			Str("accountId", accountID).
			Str("deviceId", entry.deviceID).
			Str("ackId", frame.AckID).
			Msg("Failed to persist websocket packet for ack tracking")
	}

	if priority {
		return entry.sendJSONPriority(frame)
	}
	return entry.sendJSON(frame)
}

// sendFrameToDevice replays a stored frame on every live connection of the
// device. With no live connection it is a no-op; the entry stays pending until
// the device reconnects (session.resume) or the retry budget runs out.
func (s *Service) sendFrameToDevice(namespace, deviceID string, frame []byte) error {
	entries := s.getDeviceConnections(namespace, deviceID)
	if len(entries) == 0 {
		return nil
	}
	// The stored bytes are this gateway's own Packet JSON, so decoding keeps
	// the frame byte-identical (Data is a json.RawMessage).
	var packet Packet
	if err := json.Unmarshal(frame, &packet); err != nil {
		return err
	}
	var lastErr error
	for _, entry := range entries {
		// A connection that has not spoken the ack protocol is an
		// un-upgraded client: replaying into it would only repeat frames it
		// cannot clear. Its entries expire on their own.
		if !entry.supportsAcks() {
			continue
		}
		if err := entry.sendJSONReplay(packet); err != nil {
			lastErr = err
			logging.Log.Warn().
				Err(err).
				Str("namespace", namespace).
				Str("accountId", entry.getAccountID()).
				Str("deviceId", deviceID).
				Str("ackId", packet.AckID).
				Msg("Failed to replay websocket packet")
		}
	}
	return lastErr
}

func (s *Service) handleSessionAck(ctx context.Context, namespace, accountID, deviceID string, packet Packet) error {
	if s.acks == nil {
		return nil
	}
	var payload struct {
		AckID string `json:"ack_id"`
	}
	if len(packet.Data) > 0 {
		if err := json.Unmarshal(packet.Data, &payload); err != nil {
			return nil
		}
	}
	ackID := strings.TrimSpace(payload.AckID)
	if ackID == "" {
		return nil
	}

	found, err := s.acks.Ack(ctx, namespace, accountID, ackID)
	if err != nil {
		logging.Log.Warn().
			Err(err).
			Str("namespace", namespace).
			Str("accountId", accountID).
			Str("deviceId", deviceID).
			Str("ackId", ackID).
			Msg("Failed to record websocket packet ack")
		return nil
	}
	logging.Log.Debug().
		Str("namespace", namespace).
		Str("accountId", accountID).
		Str("deviceId", deviceID).
		Str("ackId", ackID).
		Bool("found", found).
		Msg("Received websocket packet ack")
	return nil
}

func (s *Service) handleSessionResume(ctx context.Context, namespace, accountID, deviceID string) error {
	if s.acks == nil {
		return nil
	}
	entries, err := s.acks.List(ctx, namespace, accountID)
	if err != nil {
		logging.Log.Warn().
			Err(err).
			Str("namespace", namespace).
			Str("accountId", accountID).
			Str("deviceId", deviceID).
			Msg("Failed to list pending websocket packets")
		return nil
	}

	// Cap one resume: a client that reconnects in a loop would otherwise
	// re-receive the entire buffer on every connect. The overflow is not lost —
	// it stays pending and the reconciler keeps retrying it on its own schedule.
	budget := ackMaxResumeReplay
	replayed := 0
	skipped := 0
	for _, entry := range entries {
		if entry.DeviceID != deviceID {
			continue
		}
		if replayed >= budget {
			skipped++
			continue
		}
		// Resume never clears an entry: only an ack does, which is what makes
		// the reconciler's re-sends harmless.
		if err := s.sendFrameToDevice(namespace, deviceID, entry.Frame); err != nil {
			continue
		}
		replayed++
	}
	logging.Log.Debug().
		Str("namespace", namespace).
		Str("accountId", accountID).
		Str("deviceId", deviceID).
		Int("pending", len(entries)).
		Int("replayed", replayed).
		Int("deferred", skipped).
		Msg("Replayed pending websocket packets on session resume")
	return nil
}

// RunAckReconciler re-sends unacknowledged frames until they are acked or their
// retry budget is exhausted. It returns when ctx is cancelled.
func (s *Service) RunAckReconciler(ctx context.Context) {
	if s.acks == nil {
		return
	}
	ticker := time.NewTicker(ackReconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileAcksOnce(ctx)
		}
	}
}

type ackAccountRef struct {
	namespace string
	accountID string
}

// reconcileAcksOnce drives exactly one reconciliation pass. It is exported in
// package so tests can run a pass without the goroutine and its ticker.
func (s *Service) reconcileAcksOnce(ctx context.Context) {
	if s.acks == nil {
		return
	}
	retention, maxPending := s.ackLimits()
	now := time.Now()

	for _, ref := range s.pendingAccounts() {
		entries, err := s.acks.List(ctx, ref.namespace, ref.accountID)
		if err != nil {
			logging.Log.Warn().
				Err(err).
				Str("namespace", ref.namespace).
				Str("accountId", ref.accountID).
				Msg("Failed to list pending websocket packets for reconciliation")
			continue
		}
		if len(entries) == 0 {
			continue
		}
		if maxPending > 0 && float64(len(entries)) >= ackPendingWarnRatio*float64(maxPending) {
			logging.Log.Warn().
				Str("namespace", ref.namespace).
				Str("accountId", ref.accountID).
				Int("pending", len(entries)).
				Int("maxPending", maxPending).
				Msg("Websocket client is not acknowledging packets")
		}

		expired := make([]string, 0)
		for _, entry := range entries {
			if entry.Attempts >= s.ackAttemptBudget() || now.Sub(entry.CreatedAt) >= retention {
				expired = append(expired, entry.AckID)
				logging.Log.Warn().
					Str("namespace", ref.namespace).
					Str("accountId", ref.accountID).
					Str("deviceId", entry.DeviceID).
					Str("ackId", entry.AckID).
					Int("attempts", entry.Attempts).
					Msg("Dropping websocket packet after retry budget")
				continue
			}
			if entry.NextTryAt.After(now) {
				continue
			}
			// No live connection makes this a no-op; the attempt still counts,
			// which is what bounds the buffer while a device stays offline.
			_ = s.sendFrameToDevice(ref.namespace, entry.DeviceID, entry.Frame)
			if err := s.acks.CountAttempt(ctx, ref.namespace, ref.accountID, entry.AckID, now.Add(s.ackRetryInterval())); err != nil {
				logging.Log.Warn().
					Err(err).
					Str("namespace", ref.namespace).
					Str("accountId", ref.accountID).
					Str("ackId", entry.AckID).
					Msg("Failed to record websocket packet retry attempt")
			}
		}
		if len(expired) > 0 {
			if err := s.acks.Drop(ctx, ref.namespace, ref.accountID, expired); err != nil {
				logging.Log.Warn().
					Err(err).
					Str("namespace", ref.namespace).
					Str("accountId", ref.accountID).
					Int("count", len(expired)).
					Msg("Failed to drop expired websocket packets")
			}
		}
	}
}

// pendingAccounts snapshots the (namespace, account) pairs this replica has
// live connections for. Pending entries for accounts that are fully offline are
// left to key expiry, which bounds the work done per pass.
func (s *Service) pendingAccounts() []ackAccountRef {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[ackAccountRef]struct{}, len(s.connections))
	refs := make([]ackAccountRef, 0, len(s.connections))
	for key := range s.connections {
		ref := ackAccountRef{namespace: key.namespace, accountID: key.accountID}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}
