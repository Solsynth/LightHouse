package e2eectl

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// fakeEventBus records websocket pushes so the smoke test can assert the
// NATS payload shape without a live NATS broker.
type fakeEventBus struct {
	pushes []pushRecord
}

type pushRecord struct {
	target  string
	event   string
	payload any
}

func (f *fakeEventBus) PublishSessionRevoked(context.Context, []auth.SessionRevokedEvent) error {
	return nil
}
func (f *fakeEventBus) PublishWS(_ context.Context, target, event string, payload any) error {
	f.pushes = append(f.pushes, pushRecord{target: target, event: event, payload: payload})
	return nil
}

// smokeDSN mirrors config.example.toml.
const smokeDSN = "host=localhost port=5432 user=postgres password=postgres dbname=dyson_stargate sslmode=disable"

func strPtr(s string) *string { return &s }

func TestServiceSmoke(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), smokeDSN)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	ctx = context.Background()

	// Seed two accounts (FK targets).
	seed := func(name string) string {
		id := uuid.NewString()
		now := time.Now().UTC()
		if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, name, nick, language, region, is_superuser, created_at, updated_at)
			VALUES ($1, $2, $2, 'en', 'US', false, $3, $3)`, id, name, now); err != nil {
			t.Fatalf("seed account: %v", err)
		}
		return id
	}
	alice := seed("e2ee_smoke_" + uuid.NewString()[:8])
	bob := seed("e2ee_smoke_" + uuid.NewString()[:8])
	defer pool.Exec(ctx, `DELETE FROM accounts WHERE id = ANY($1)`, []string{alice, bob})

	st := store.New(pool)
	bus := &fakeEventBus{}
	s := NewService(st, bus, nil, nil)

	deviceA := "dev-" + uuid.NewString()[:8]
	deviceB := "dev-" + uuid.NewString()[:8]
	groupID := "grp-" + uuid.NewString()[:8]

	// 1. Publish key packages (Alice's device, Bob's device).
	kp, err := s.PublishMlsKeyPackage(ctx, alice, deviceA, strPtr("Alice phone"), []byte("kp-alice-1"), DefaultMlsCiphersuite, nil)
	if err != nil {
		t.Fatalf("publish alice kp: %v", err)
	}
	if kp.AccountId != alice || kp.DeviceId != deviceA || !bytes.Equal(kp.KeyPackage, []byte("kp-alice-1")) {
		t.Fatalf("published kp mismatch: %+v", kp)
	}
	if _, err := s.PublishMlsKeyPackage(ctx, bob, deviceB, strPtr("Bob phone"), []byte("kp-bob-1"), DefaultMlsCiphersuite, nil); err != nil {
		t.Fatalf("publish bob kp: %v", err)
	}

	// 2. Consuming read claims exactly once.
	pkgs, err := s.ListMlsDeviceKeyPackages(ctx, alice, &bob, true)
	if err != nil {
		t.Fatalf("consume alice kp: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].DeviceId != deviceA || !bytes.Equal(pkgs[0].KeyPackage, []byte("kp-alice-1")) {
		t.Fatalf("consume result mismatch: %+v", pkgs)
	}
	again, err := s.ListMlsDeviceKeyPackages(ctx, alice, &bob, true)
	if err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("key package claimed twice: %+v", again)
	}

	// 3. KP status after consumption (0 < 3 -> needs more).
	status, err := s.MlsKeyPackageStatus(ctx, alice)
	if err != nil {
		t.Fatalf("kp status: %v", err)
	}
	if !status.NeedsMoreKps || len(status.DevicesNeedingKps) != 1 || status.DevicesNeedingKps[0].DeviceId != deviceA {
		t.Fatalf("kp status mismatch: %+v", status)
	}

	// 4. Bootstrap the group (idempotent replay).
	state, err := s.BootstrapMlsGroup(ctx, alice, groupID, 0, 1, map[string]any{"client": "smoke"})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if state.MlsGroupId != groupID || state.Epoch != 0 {
		t.Fatalf("bootstrap state mismatch: %+v", state)
	}
	replay, err := s.BootstrapMlsGroup(ctx, alice, groupID, 5, 9, nil)
	if err != nil {
		t.Fatalf("bootstrap replay: %v", err)
	}
	if replay.Epoch != 0 || replay.StateVersion != 1 {
		t.Fatalf("bootstrap replay must return existing state: %+v", replay)
	}

	// 5. Memberships (Bob's device joins at epoch 0; Alice's device added).
	if _, err := s.AddMlsDeviceMembership(ctx, bob, deviceB, groupID, 0); err != nil {
		t.Fatalf("add bob membership: %v", err)
	}
	if _, err := s.AddMlsDeviceMembership(ctx, alice, deviceA, groupID, 0); err != nil {
		t.Fatalf("add alice membership: %v", err)
	}
	member, err := s.IsMlsGroupMember(ctx, alice, deviceA, groupID)
	if err != nil || !member {
		t.Fatalf("alice should be member: %v %v", member, err)
	}

	// 6. Strict epoch+1 commit.
	if _, err := s.CommitMlsGroup(ctx, alice, groupID, 2, "member_add", map[string]any{"reason": "add"}); err == nil {
		t.Fatalf("commit epoch 2 must fail")
	}
	if _, err := s.CommitMlsGroup(ctx, alice, groupID, 1, "member_add", map[string]any{"reason": "add"}); err != nil {
		t.Fatalf("commit epoch 1: %v", err)
	}

	// 7. Fanout a group message (Alice -> group; Bob's device must get it).
	msg := []byte("hello mls")
	envelopes, err := s.FanoutMlsMessageToGroup(ctx, alice, deviceA, groupID, msg, nil, nil, strPtr("cm-1"), map[string]any{"k": "v"}, envelopeTypeMlsApplication)
	if err != nil {
		t.Fatalf("fanout message: %v", err)
	}
	if len(envelopes) != 1 || envelopes[0].RecipientDeviceId == nil || *envelopes[0].RecipientDeviceId != deviceB {
		t.Fatalf("fanout must target bob device only: %+v", envelopes)
	}
	if envelopes[0].Sequence != 1 {
		t.Fatalf("expected sequence 1, got %d", envelopes[0].Sequence)
	}

	// 8. Fanout again -> monotonic sequence per device.
	envelopes2, err := s.FanoutMlsMessageToGroup(ctx, alice, deviceA, groupID, []byte("second"), nil, nil, nil, nil, envelopeTypeMlsApplication)
	if err != nil {
		t.Fatalf("fanout second: %v", err)
	}
	if envelopes2[0].Sequence != 2 {
		t.Fatalf("expected sequence 2, got %d", envelopes2[0].Sequence)
	}

	// 9. Pending fetch returns both envelopes byte-identical, in order.
	pending, err := s.GetPendingEnvelopesByDevice(ctx, bob, deviceB, 100)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 2 || pending[0].Sequence != 1 || pending[1].Sequence != 2 {
		t.Fatalf("pending mismatch: %+v", pending)
	}
	if !bytes.Equal(pending[0].Ciphertext, msg) {
		t.Fatalf("ciphertext not byte-identical")
	}

	// 10. Real-time push fired for the recipient device with the envelope
	// payload shape.
	var envelopePush bool
	for _, p := range bus.pushes {
		if p.event == packetType && p.target == deviceB {
			envelopePush = true
			if _, ok := p.payload.(envelopePushPayload); !ok {
				t.Fatalf("unexpected push payload type %T", p.payload)
			}
		}
	}
	if !envelopePush {
		t.Fatalf("no e2ee.envelope push to device %s (pushes: %+v)", deviceB, bus.pushes)
	}

	// 11. Ack removes the envelope from the pending set.
	acked, err := s.AckEnvelopeByDevice(ctx, bob, deviceB, pending[0].Id)
	if err != nil || acked == nil {
		t.Fatalf("ack: %v %v", acked, err)
	}
	if acked.DeliveryStatus != envelopeStatusAcknowledged {
		t.Fatalf("ack status mismatch: %+v", acked)
	}
	pending2, _ := s.GetPendingEnvelopesByDevice(ctx, bob, deviceB, 100)
	if len(pending2) != 1 || pending2[0].Id != pending[1].Id {
		t.Fatalf("acked envelope still pending: %+v", pending2)
	}

	// 12. Group info upload with epoch guard.
	ui, err := s.UploadGroupInfo(ctx, groupID, []byte("gi-1"), []byte("rt-1"), 1)
	if err != nil || !ui.Success {
		t.Fatalf("upload group info: %v %+v", err, ui)
	}
	bad, err := s.UploadGroupInfo(ctx, groupID, []byte("gi-2"), []byte("rt-2"), 99)
	if err != nil || bad.Success || bad.Epoch != 1 {
		t.Fatalf("epoch guard must reject: %+v %v", bad, err)
	}
	gv, err := s.GetGroupState(ctx, groupID)
	if err != nil || gv == nil || !bytes.Equal(gv.GroupInfo, []byte("gi-1")) {
		t.Fatalf("group info round-trip: %+v %v", gv, err)
	}

	// 13. Revoke Bob's device: control envelope to Bob's sibling device.
	deviceB2 := "dev-" + uuid.NewString()[:8]
	if _, err := s.PublishMlsKeyPackage(ctx, bob, deviceB2, strPtr("Bob tablet"), []byte("kp-bob-2"), DefaultMlsCiphersuite, nil); err != nil {
		t.Fatalf("publish bob second kp: %v", err)
	}
	revoked, err := s.RevokeDevice(ctx, bob, deviceB)
	if err != nil || !revoked {
		t.Fatalf("revoke: %v %v", revoked, err)
	}
	b2Pending, err := s.GetPendingEnvelopesByDevice(ctx, bob, deviceB2, 100)
	if err != nil {
		t.Fatalf("bob b2 pending: %v", err)
	}
	var control bool
	for _, e := range b2Pending {
		if e.Type == envelopeTypeControl {
			control = true
			if e.ClientMessageId == nil || !strings.HasPrefix(*e.ClientMessageId, "mls-revoke-"+deviceB+"-") {
				t.Fatalf("unexpected control clientMessageId %v", e.ClientMessageId)
			}
		}
	}
	if !control {
		t.Fatalf("no control envelope to sibling device after revoke: %+v", b2Pending)
	}
	bobPending, _ := s.GetPendingEnvelopesByDevice(ctx, bob, deviceB, 100)
	if len(bobPending) != 0 {
		t.Fatalf("revoked device must have no pending envelopes: %+v", bobPending)
	}

	// 14. Group reset recreates the state at the new epoch.
	rs, err := s.ResetMlsGroup(ctx, groupID, 7, 3, nil)
	if err != nil || rs == nil {
		t.Fatalf("reset: %v %v", rs, err)
	}
	if rs.Epoch != 7 || rs.StateVersion != 4 {
		t.Fatalf("reset state mismatch: %+v", rs)
	}
	if _, err := s.ResetMlsGroup(ctx, "grp-missing-"+uuid.NewString()[:8], 1, 1, nil); err != nil {
		t.Fatalf("reset missing group must not error: %v", err)
	}
}
