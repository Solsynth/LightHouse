package chat

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
)

// TestListInvitesCarriesChatRoom pins the invite wire shape the Island client
// depends on: ChatInvitesSheet renders `invite.chatRoom!` and a DM invite names
// its peer from `chat_room.members`. Returning bare member rows left chat_room
// null, so every invite sheet threw instead of listing the invites.
func TestListInvitesCarriesChatRoom(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&store.ChatRoomEntity{}, &store.ChatMemberEntity{}); err != nil {
		t.Fatalf("AutoMigrate() error = %v", err)
	}

	invitee := uuid.New()
	inviter := uuid.New()
	room := &store.ChatRoomEntity{ID: uuid.New(), Type: int(model.ChatRoomDirectMessage)}
	if err := db.Create(room).Error; err != nil {
		t.Fatalf("create room: %v", err)
	}
	joinedAt := time.Now().UTC()
	members := []*store.ChatMemberEntity{
		{ID: uuid.New(), ChatRoomID: room.ID, AccountID: inviter, JoinedAt: &joinedAt},
		{ID: uuid.New(), ChatRoomID: room.ID, AccountID: invitee},
	}
	for _, member := range members {
		if err := db.Create(member).Error; err != nil {
			t.Fatalf("create member: %v", err)
		}
	}

	account := func(id, nick string) *gen.DyAccount {
		return &gen.DyAccount{Id: id, Name: nick, Nick: nick, Profile: &gen.DyAccountProfile{Id: id}}
	}
	i := &instance{
		db:  db,
		st:  store.New(db),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		accounts: stubAccounts{byID: map[string]*gen.DyAccount{
			invitee.String(): account(invitee.String(), "Invitee"),
			inviter.String(): account(inviter.String(), "Inviter"),
		}},
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/chat/invites", nil)
	c.Set(authn.ContextKeyUser, &gen.DyAccount{Id: invitee.String()})
	i.listInvites(c)

	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var payload []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal invites: %v (body %s)", err, recorder.Body.String())
	}
	if len(payload) != 1 {
		t.Fatalf("invites = %d, want 1 (body %s)", len(payload), recorder.Body.String())
	}

	invite := payload[0]
	if invite["account_id"] != invitee.String() {
		t.Fatalf("account_id = %v, want %s", invite["account_id"], invitee)
	}
	if invite["account"] == nil {
		t.Fatalf("invite carries no account: %s", recorder.Body.String())
	}
	room0, ok := invite["chat_room"].(map[string]any)
	if !ok {
		t.Fatalf("chat_room = %#v, want an object (body %s)", invite["chat_room"], recorder.Body.String())
	}
	if room0["id"] != room.ID.String() {
		t.Fatalf("chat_room.id = %v, want %s", room0["id"], room.ID)
	}
	for _, key := range []string{
		"name", "description", "type", "is_public", "encryption_mode",
		"picture", "background", "realm", "account_id", "realm_id",
		"created_at", "updated_at", "deleted_at",
	} {
		if _, present := room0[key]; !present {
			t.Errorf("chat_room is missing %q; the Dart SnChatRoom parser requires it", key)
		}
	}

	// The DM tile is titled from the other member's account, so the invite's
	// room has to carry the peer — and only the peer.
	roomMembers, ok := room0["members"].([]any)
	if !ok || len(roomMembers) != 1 {
		t.Fatalf("chat_room.members = %#v, want the one peer", room0["members"])
	}
	peer, ok := roomMembers[0].(map[string]any)
	if !ok || peer["account_id"] != inviter.String() {
		t.Fatalf("room member = %#v, want the inviter", roomMembers[0])
	}
	peerAccount, ok := peer["account"].(map[string]any)
	if !ok || peerAccount["id"] != inviter.String() {
		t.Fatalf("peer account = %#v, want the inviter's account", peer["account"])
	}
	if _, hasProfile := peerAccount["profile"]; !hasProfile {
		t.Fatalf("peer account carries no profile: %#v", peerAccount)
	}

	// Nested rooms would recurse forever; a member snapshot never embeds one.
	if nested, present := peer["chat_room"]; present && nested != nil {
		t.Fatalf("peer member embeds a nested chat_room: %#v", nested)
	}
}
