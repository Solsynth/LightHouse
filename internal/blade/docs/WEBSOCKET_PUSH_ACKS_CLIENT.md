# WebSocket push delivery acks — client integration

What a client must implement to receive `messages.*` pushes at-least-once, and
what happens if it does not. Server behaviour lives in
[`WEBSOCKET_GATEWAY.md`](./WEBSOCKET_GATEWAY.md#ack-tracking--at-least-once-delivery).

## TL;DR

| You want | You must do |
|---|---|
| Today's best-effort pushes (do nothing) | Nothing. The gateway detects the absence of the handshake and never tracks you. |
| At-least-once `messages.*` | Send `session.resume` on every connect; send `session.ack` for every frame carrying `_ack_id`; make re-delivery idempotent. |

The gateway decides per **connection**, not per deployment, so old and new clients
can share one gateway with no flag day.

## Backward compatibility

A client that sends neither control packet is **never** tracked:

- no `_ack_id` is stamped on its frames — the payload is byte-identical to what
  it received before this feature existed;
- nothing is written to Redis for it;
- nothing is replayed into it, even when pending entries exist for its
  account+device from an earlier ack-capable session.

Two consequences worth knowing:

- **Silent downgrade is safe.** If a user rolls back to an older build, the old
  build connects, sends nothing, and is served best effort. Pending entries left
  by the previous build stay in Redis, stop being replayed into the downgraded
  connection, and expire after `ackRetentionSeconds`.
- **Unknown packet types are only an error for the sender.** An old gateway
  receiving `session.ack`/`session.resume` answers with one `error` frame and
  keeps the connection open. A client on this protocol talking to an
  un-upgraded gateway should therefore ignore `error` frames whose message
  mentions an unprocessable packet rather than treating them as fatal.

The capability is per connection and is re-established on every reconnect: a
fresh socket starts untracked until its own `session.resume` arrives.

## Required client behaviour

### 1. Resume on every connect

Send this immediately after the socket is ready, before anything else:

```json
{"type":"session.resume","data":null}
```

On **every** connection generation, not only reconnects. The gateway derives
account and device from the connection, so there is no cursor to persist. A push
that landed while the app was offline is pending for the new connection too, and
a reconnect-only resume would leave that window uncovered.

### 2. Ack every stamped frame

`_ack_id` is a **sibling of `data`** on the frame envelope, not a field inside the
payload:

```json
{"type":"messages.new","data":{"id":"..."},"_ack_id":"5f0d1c2e-..."}
```

After handing the frame to the application, answer:

```json
{"type":"session.ack","data":{"ack_id":"5f0d1c2e-..."}}
```

Rules:

- **Ack repeats too.** A repeated `_ack_id` means the gateway never saw the
  previous ack. De-duplicating acks client-side strands the frame in the retry
  buffer until it expires.
- **Never ack a frame without `_ack_id`.** `ping`, `pong`, `error`, and every
  untracked family carry none.
- **Ack after the side effects are durable**, not on receipt, if the consumer can
  fail. Acking early turns at-least-once into at-most-once for that consumer.
- No throttling or batching is needed: an ack is ~60 bytes and the server's
  per-account cap bounds the volume.

### 3. Make re-delivery idempotent

This is a precondition, not a nicety. Three independent paths can deliver the
same frame twice: the reconciler retry, a `session.resume` replay, and a replay
racing a retry inside one reconcile interval.

Per consumer:

| Consumer | Requirement |
|---|---|
| Message store | Upsert by message id; a repeat must update the row, not insert a second |
| Sound / haptics / toast | Fire only when the id was **not** already present locally |
| Unread counters | Skip when the id was already counted; keep a bounded seen-id set |
| Event bus / list append | Guard on id, or on "row was absent before the upsert" |

Every counter guarded this way is also reconciled from REST (room open, summary
sync, `GET /messager/chat/unread`), so a skipped duplicate can never drift
permanently — and an id that falls out of a bounded window costs at most one
duplicate increment that the next sync corrects.

### 4. Expect a bounded number of replays

Both replay paths are capped, so a client must not depend on an unbounded
resume:

- one `session.resume` replays at most **64** pending frames. Anything beyond
  that stays pending and arrives later through the reconciler, in send order, at
  the retry interval — so a large backlog drains over several seconds rather
  than all at once.
- one frame is re-sent at most **`ackMaxAttempts`** (20) times before it is
  dropped. A client offline longer than the retry horizon misses it; the REST
  catch-up on room open is the recovery for that window.

This is why the resume is a *fast path*, not a guaranteed full sync. A client that
needs a hard guarantee about what it missed must reconcile over REST, exactly as
it does today.

### 5. Do not assume ordering across retries

Frames are stored and replayed in send order, but a retry of frame N can arrive
after frame N+1 was delivered live. Consumers must tolerate an older message
arriving late: apply by id, not by arrival position.

## Reference implementation (Dart, `web_socket_channel`)

```dart
// Read the stamp from the envelope BEFORE the generated model parses it:
// the model drops unknown keys.
String? ackIdForFrame(Map<String, dynamic> decoded) {
  final ackId = decoded['_ack_id'];
  if (ackId is! String) return null;
  final trimmed = ackId.trim();
  return trimmed.isEmpty ? null : trimmed;
}

// On a successful connect, every generation:
void onConnected() {
  send(jsonEncode({'type': 'session.resume', 'data': null}));
}

// In the inbound listener:
final decoded = jsonDecode(raw) as Map<String, dynamic>;
final ackId = ackIdForFrame(decoded);
final packet = WebSocketPacket.fromJson(decoded);
dispatch(packet);                       // upsert by id, guarded side effects
if (ackId != null) {
  send(jsonEncode({
    'type': 'session.ack',
    'data': {'ack_id': ackId},
  }));
}
```

If one consumer can fail after ingest (for example an E2EE decrypt path), give it
an explicit `ack(ackId)` hook and skip the automatic ack for that family — an
additive change, no protocol change.

## Timing a client can rely on

| Quantity | Default | Config key |
|---|---|---|
| Retry interval per unacked frame | 3s | `ackRetrySeconds` |
| Retry horizon (entry dropped after) | 600s | `ackRetentionSeconds` |
| Max re-sends per frame before drop | 20 | `ackMaxAttempts` |
| Max frames replayed by one `session.resume` | 64 | compiled in (`ackMaxResumeReplay`) |
| Per-account pending cap, oldest evicted first | 256 | `ackMaxPending` |

A client that is offline longer than the horizon loses the frame — that is a
bounded gap by design, not a bug. Recovery for that window is the normal
REST catch-up the app already does on room open.

## Verifying an integration

1. Connect, send `session.resume`, and push a `messages.new` through the gateway.
   The frame must carry `_ack_id`.
2. `redis-cli HLEN ws:ack:index:<namespace>:<accountId>` must be `1`.
3. Send the ack. The same key must read `0` within a second. (`LLEN` on
   `ws:ack:pending:` trails by one reconciler pass, because an acked element is
   pruned lazily.)
4. Push again and **do not** ack. The same frame, with the same `_ack_id`, must
   arrive again after `ackRetrySeconds`.
5. Background and foreground the app. Exactly one `session.resume` per
   connection generation, and exactly one `session.ack` per stamped frame.
6. Re-deliver a frame deliberately. Sound, unread counters and toasts must not
   fire twice.

For step 2/3, an empty pending list is the answer to "did the client receive it".

## Known limits

- **Cross-replica loss.** A push lost inside core NATS between replicas is still
  lost; the tracker covers queue-full, write-error, offline-device and
  replica-restart drops only.
- **Retention is the replay window.** Raising `ackRetentionSeconds` widens it;
  `ackMaxPending` is the memory bound.
- **Ordering.** See "Do not assume ordering across retries" above.
- **Only `messages.*` is tracked.** Other families keep best effort and never
  carry `_ack_id`, so a client that acks every stamped frame needs no change if
  another family is added later — but that family's consumers do need an
  idempotency audit.
