package chat

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
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
	for key, want := range map[string]string{
		"sender_name": "Alice",
		"pfp":         "pfp-1",
		"image":       "img-1",
		"user_id":     sender.AccountID.String(),
		"sender_id":   sender.ID.String(),
		"message_id":  message.ID.String(),
		"room_id":     room.ID.String(),
	} {
		if got := meta[key]; got != want {
			t.Fatalf("meta[%q] = %q, want %q", key, got, want)
		}
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

	if _, _, meta := chatPushPayload(room, message, sender, nil); meta["pfp"] != "" {
		t.Fatalf("pfp without an account = %q", meta["pfp"])
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
