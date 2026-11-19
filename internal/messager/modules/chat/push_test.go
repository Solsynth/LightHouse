package chat

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/cache"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/core"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	platformapp "src.solsynth.dev/sosys/lighthouse/shared/platform/app"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/grpcclient"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

func TestChatPushPayloadDirectMessage(t *testing.T) {
	sender := &store.ChatMemberEntity{ID: uuid.New(), AccountID: uuid.New()}
	account := &gen.DyAccount{
		Id:   sender.AccountID.String(),
		Nick: "Alice",
		Profile: &gen.DyAccountProfile{
			Picture: &gen.DyCloudFile{Id: "pfp-1"},
		},
	}
	content := "hello there"
	room := &store.ChatRoomEntity{ID: uuid.New(), Type: int(model.ChatRoomDirectMessage)}
	message := &store.ChatMessageEntity{
		ID: uuid.New(), ChatRoomID: room.ID, SenderID: sender.ID, Content: &content,
		Attachments: store.ToJSON([]model.CloudFileReferenceObject{
			{Id: "doc-1", MimeType: "application/pdf"},
			{Id: "img-1", MimeType: "image/webp"},
		}),
	}

	title, body, meta := chatPushPayload(room, message, sender, account)

	if title != "Alice (DM)" {
		t.Fatalf("title = %q, want %q", title, "Alice (DM)")
	}
	if body != "hello there" {
		t.Fatalf("body = %q", body)
	}
	for key, want := range map[string]any{
		"sender_name": "Alice",
		"pfp":         "pfp-1",
		"image":       "img-1",
		"user_id":     sender.AccountID.String(),
		"sender_id":   sender.ID.String(),
		"message_id":  message.ID.String(),
		"room_id":     room.ID.String(),
	} {
		if got := meta[key]; got != want {
			t.Fatalf("meta[%q] = %v, want %v", key, got, want)
		}
	}
	images, ok := meta["images"].([]string)
	if !ok || len(images) != 1 || images[0] != "img-1" {
		t.Fatalf("meta[images] = %#v, want [img-1]", meta["images"])
	}
	if _, ok := meta["room_name"]; ok {
		t.Fatalf("DM push carries room_name: %v", meta)
	}
}

// TestChatPushPayloadAccountPictureShape pins the avatar source: Profile RPC
// accounts carry it on profile.picture, gateway-shaped accounts on the
// top-level picture. Reading only the top-level field left every DM push
// without an avatar.
func TestChatPushPayloadAccountPictureShape(t *testing.T) {
	sender := &store.ChatMemberEntity{ID: uuid.New(), AccountID: uuid.New()}
	room := &store.ChatRoomEntity{ID: uuid.New(), Type: int(model.ChatRoomDirectMessage)}
	message := &store.ChatMessageEntity{ID: uuid.New(), ChatRoomID: room.ID, SenderID: sender.ID}

	profileShape := &gen.DyAccount{
		Id:      sender.AccountID.String(),
		Nick:    "Alice",
		Profile: &gen.DyAccountProfile{Picture: &gen.DyCloudFile{Id: "pfp-profile"}},
	}
	if _, _, meta := chatPushPayload(room, message, sender, profileShape); meta["pfp"] != "pfp-profile" {
		t.Fatalf("profile pfp = %q", meta["pfp"])
	}

	wireShape := &gen.DyAccount{
		Id:      sender.AccountID.String(),
		Nick:    "Alice",
		Picture: &gen.DyCloudFile{Id: "pfp-wire"},
	}
	if _, _, meta := chatPushPayload(room, message, sender, wireShape); meta["pfp"] != "pfp-wire" {
		t.Fatalf("top-level pfp = %q", meta["pfp"])
	}

	if _, _, meta := chatPushPayload(room, message, sender, nil); meta["pfp"] != nil {
		t.Fatalf("pfp without an account = %v", meta["pfp"])
	}
}

func TestChatPushPayloadGroupRoom(t *testing.T) {
	name := "Release Crew"
	nick := "  Alice  "
	sender := &store.ChatMemberEntity{ID: uuid.New(), AccountID: uuid.New(), Nick: &nick}
	room := &store.ChatRoomEntity{ID: uuid.New(), Type: int(model.ChatRoomGroup), Name: &name}
	longContent := strings.Repeat("x", 150)
	message := &store.ChatMessageEntity{
		ID: uuid.New(), ChatRoomID: room.ID, SenderID: sender.ID, Content: &longContent,
	}

	title, body, meta := chatPushPayload(room, message, sender, nil)

	if title != "Alice (Release Crew)" {
		t.Fatalf("title = %q", title)
	}
	if meta["sender_name"] != "Alice" {
		t.Fatalf("sender_name = %q", meta["sender_name"])
	}
	if meta["room_name"] != "Release Crew" {
		t.Fatalf("room_name = %q", meta["room_name"])
	}
	if body != strings.Repeat("x", 100) {
		t.Fatalf("body = %q, want 100 x", body)
	}
	if _, ok := meta["pfp"]; ok {
		t.Fatalf("pfp without an account: %v", meta)
	}
	if _, ok := meta["image"]; ok {
		t.Fatalf("image without attachments: %v", meta)
	}
}

func TestChatPushPayloadFallbacks(t *testing.T) {
	room := &store.ChatRoomEntity{ID: uuid.New(), Type: int(model.ChatRoomDirectMessage)}
	empty := ""
	sender := &store.ChatMemberEntity{ID: uuid.New(), AccountID: uuid.New()}
	message := &store.ChatMessageEntity{ID: uuid.New(), ChatRoomID: room.ID, Content: &empty}

	title, body, meta := chatPushPayload(room, message, sender, nil)

	if title != "Someone (DM)" {
		t.Fatalf("title = %q", title)
	}
	if body != "Attachment" {
		t.Fatalf("body = %q", body)
	}
	if meta["sender_name"] != "Someone" {
		t.Fatalf("sender_name = %q", meta["sender_name"])
	}
}

func TestChatPushPayloadAttachments(t *testing.T) {
	sender := &store.ChatMemberEntity{ID: uuid.New(), AccountID: uuid.New()}
	room := &store.ChatRoomEntity{ID: uuid.New(), Type: int(model.ChatRoomDirectMessage)}
	message := &store.ChatMessageEntity{
		ID: uuid.New(), ChatRoomID: room.ID, SenderID: sender.ID,
		Attachments: store.ToJSON([]model.CloudFileReferenceObject{
			{Id: "doc-1", MimeType: "application/pdf"},
			{Id: "img-1", MimeType: "image/webp", Url: "https://cdn.example/img-1.webp"},
			{Id: "img-2", MimeType: "image/png"},
		}),
	}

	_, body, meta := chatPushPayload(room, message, sender, nil)

	if body != "3 attachments" {
		t.Fatalf("body = %q, want the attachment count", body)
	}
	images, ok := meta["images"].([]string)
	if !ok || len(images) != 2 {
		t.Fatalf("meta[images] = %#v", meta["images"])
	}
	if images[0] != "https://cdn.example/img-1.webp" || images[1] != "img-2" {
		t.Fatalf("meta[images] = %v, want url then id", images)
	}
	if meta["image"] != "https://cdn.example/img-1.webp" {
		t.Fatalf("meta[image] = %v", meta["image"])
	}

	single := &store.ChatMessageEntity{
		ID: uuid.New(), ChatRoomID: room.ID, SenderID: sender.ID,
		Attachments: store.ToJSON([]model.CloudFileReferenceObject{
			{Id: "img-9", MimeType: "image/jpeg"},
		}),
	}
	if _, body, _ := chatPushPayload(room, single, sender, nil); body != "1 attachment" {
		t.Fatalf("singular body = %q", body)
	}
}

type stubProfileClient struct {
	gen.DyProfileServiceClient
	blocked   []string
	blockedBy []string
	muted     []string
}

func (s *stubProfileClient) ListBlocked(_ context.Context, req *gen.DyListRelationshipSimpleRequest, _ ...grpc.CallOption) (*gen.DyListRelationshipSimpleResponse, error) {
	if req.GetRelatedId() != "" {
		return &gen.DyListRelationshipSimpleResponse{AccountsId: s.blockedBy}, nil
	}
	return &gen.DyListRelationshipSimpleResponse{AccountsId: s.blocked}, nil
}

func (s *stubProfileClient) ListMuted(_ context.Context, _ *gen.DyListRelationshipSimpleRequest, _ ...grpc.CallOption) (*gen.DyListRelationshipSimpleResponse, error) {
	return &gen.DyListRelationshipSimpleResponse{AccountsId: s.muted}, nil
}

// TestPushSuppression pins the two C# SendPushNotificationsAsync filters: a
// live room subscription and a block/mute relationship both drop the account.
func TestPushSuppression(t *testing.T) {
	ctx := context.Background()
	roomID := uuid.New()
	subscribed := &store.ChatMemberEntity{ID: uuid.New(), AccountID: uuid.New(), ChatRoomID: roomID}
	plain := &store.ChatMemberEntity{ID: uuid.New(), AccountID: uuid.New(), ChatRoomID: roomID}
	blocked, blockedBy, muted := uuid.NewString(), uuid.NewString(), uuid.NewString()

	redis := cache.NewMemoryCacheService(64)
	subs := map[string][]cachedDevice{
		subscribed.ID.String(): {{DeviceToken: "device-1", ExpiresAt: utcNow().Add(time.Minute)}},
	}
	if err := redis.Set(ctx, subscriptionKey(roomID), subs, time.Minute); err != nil {
		t.Fatalf("seed subscriptions: %v", err)
	}
	i := &instance{
		host: &module.Host[core.Services]{Core: &core.Services{Env: &platformapp.Env{
			Redis: redis,
			Clients: &grpcclient.Clients{Profile: &stubProfileClient{
				blocked: []string{blocked}, blockedBy: []string{blockedBy}, muted: []string{muted},
			}},
		}}},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	suppressed := i.suppressedPushAccountIDs(ctx, roomID, []*store.ChatMemberEntity{subscribed, plain}, uuid.NewString())
	for _, id := range []string{subscribed.AccountID.String(), blocked, blockedBy, muted} {
		if !suppressed[id] {
			t.Errorf("suppressed[%s] = false, want true", id)
		}
	}
	if suppressed[plain.AccountID.String()] {
		t.Errorf("unsubscribed, unrelated member was suppressed")
	}

	got := filterPushRecipients([]string{subscribed.AccountID.String(), plain.AccountID.String(), blocked, blockedBy, muted}, suppressed)
	if len(got) != 1 || got[0] != plain.AccountID.String() {
		t.Fatalf("filterPushRecipients = %v, want [%s]", got, plain.AccountID.String())
	}
}

type stubAccounts struct {
	byID map[string]*gen.DyAccount
}

func (s stubAccounts) GetAccount(_ context.Context, id string) (*gen.DyAccount, error) {
	return s.byID[id], nil
}

func (s stubAccounts) GetAccounts(_ context.Context, ids []string) ([]*gen.DyAccount, error) {
	out := make([]*gen.DyAccount, 0, len(ids))
	for _, id := range ids {
		if account := s.byID[id]; account != nil {
			out = append(out, account)
		}
	}
	return out, nil
}

func (stubAccounts) LookupAccounts(context.Context, []string) ([]*gen.DyAccount, error) {
	return nil, nil
}

func (stubAccounts) HasRelationship(context.Context, string, string) (bool, error) {
	return false, nil
}

type sentPush struct {
	ids   []string
	title string
	body  string
}

type stubNotify struct {
	sent []sentPush
}

func (s *stubNotify) SendPush(ctx context.Context, accountID, topic, title, body string, meta map[string]any) error {
	return s.SendPushToAccounts(ctx, []string{accountID}, topic, title, body, meta)
}

func (s *stubNotify) SendPushToAccounts(_ context.Context, accountIDs []string, _, title, body string, _ map[string]any) error {
	s.sent = append(s.sent, sentPush{ids: append([]string(nil), accountIDs...), title: title, body: body})
	return nil
}

// TestEncryptedPushBodyLocalized pins the C# encrypted-message notification:
// every recipient is notified, with the placeholder body in their language.
func TestEncryptedPushBodyLocalized(t *testing.T) {
	room := &store.ChatRoomEntity{ID: uuid.New(), Type: int(model.ChatRoomDirectMessage)}
	sender := &store.ChatMemberEntity{ID: uuid.New(), AccountID: uuid.New()}
	entity := &store.ChatMessageEntity{
		ID:             uuid.New(),
		ChatRoomID:     room.ID,
		SenderID:       sender.ID,
		EncryptionMeta: store.ToJSON(map[string]any{"scheme": "chat.mls.v2"}),
	}
	english := &gen.DyAccount{Id: uuid.NewString(), Language: "en-US"}
	chinese := &gen.DyAccount{Id: uuid.NewString(), Language: "zh-CN"}
	notify := &stubNotify{}
	i := &instance{
		accounts: stubAccounts{byID: map[string]*gen.DyAccount{
			english.Id: english,
			chinese.Id: chinese,
		}},
		notify: notify,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	i.sendEncryptedMessagePush(context.Background(), []string{english.Id, chinese.Id}, room, entity, sender, nil)

	if len(notify.sent) != 2 {
		t.Fatalf("sent %d pushes, want 2", len(notify.sent))
	}
	got := map[string][]string{}
	for _, push := range notify.sent {
		got[push.body] = push.ids
	}
	if ids := got["You've received a message"]; len(ids) != 1 || ids[0] != english.Id {
		t.Errorf("English push ids = %v, want [%s]", ids, english.Id)
	}
	if ids := got["您收到了一条消息"]; len(ids) != 1 || ids[0] != chinese.Id {
		t.Errorf("Chinese push ids = %v, want [%s]", ids, chinese.Id)
	}
}
