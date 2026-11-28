package chat

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestRedirectMessagesCreatesHistorySegment pins the client contract the
// Island redirect card and history sheet parse: one destination message whose
// meta.redirect carries the frozen source transcript, version 2 for a
// multi-message selection.
func TestRedirectMessagesCreatesHistorySegment(t *testing.T) {
	i, db, room, identity := newRedirectFixture(t)

	sourceRoom := &store.ChatRoomEntity{ID: uuid.New(), Type: int(model.ChatRoomGroup)}
	sourceRoom.Name = new("General")
	if err := db.Create(sourceRoom).Error; err != nil {
		t.Fatalf("create source room: %v", err)
	}
	if err := db.Create(&store.ChatMemberEntity{
		ID: uuid.New(), ChatRoomID: sourceRoom.ID, AccountID: identity.AccountID,
		JoinedAt: new(time.Now().UTC().Add(-time.Hour)),
	}).Error; err != nil {
		t.Fatalf("create source member: %v", err)
	}

	createdAt := time.Now().UTC().Add(-30 * time.Minute)
	first := &store.ChatMessageEntity{
		ID: uuid.New(), Type: "text", SenderID: identity.ID, ChatRoomID: sourceRoom.ID,
		Content: new("hello"), CreatedAt: createdAt, UpdatedAt: createdAt,
		Attachments: store.ToJSON([]model.CloudFileReferenceObject{}), Meta: store.ToJSON(map[string]any{}),
	}
	second := &store.ChatMessageEntity{
		ID: uuid.New(), Type: "text", SenderID: identity.ID, ChatRoomID: sourceRoom.ID,
		Content: new("world"), CreatedAt: createdAt.Add(time.Minute), UpdatedAt: createdAt.Add(time.Minute),
		Attachments: store.ToJSON([]model.CloudFileReferenceObject{}), Meta: store.ToJSON(map[string]any{}),
	}
	for _, entity := range []*store.ChatMessageEntity{second, first} {
		if err := db.Create(entity).Error; err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	recorder, c := redirectRequest(t, room.ID, identity.AccountID, first.ID, second.ID)
	i.redirectMessages(c)
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal message: %v (body %s)", err, recorder.Body.String())
	}
	if payload["chat_room_id"] != room.ID.String() {
		t.Fatalf("chat_room_id = %v, want %s", payload["chat_room_id"], room.ID)
	}
	if payload["forwarded_message_id"] != first.ID.String() {
		t.Fatalf("forwarded_message_id = %v, want the oldest source id %s", payload["forwarded_message_id"], first.ID)
	}
	if payload["content"] != "Redirect chat history" {
		t.Fatalf("content = %v, want the localized placeholder", payload["content"])
	}

	meta, ok := payload["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta = %#v, want an object", payload["meta"])
	}
	redirect, ok := meta["redirect"].(map[string]any)
	if !ok {
		t.Fatalf("meta.redirect = %#v, want the snapshot object", meta["redirect"])
	}
	if redirect["version"] != float64(2) || redirect["kind"] != "history_segment" {
		t.Fatalf("redirect = version %v kind %v, want 2/history_segment", redirect["version"], redirect["kind"])
	}
	if redirect["source_room_id"] != sourceRoom.ID.String() {
		t.Fatalf("source_room_id = %v, want %s", redirect["source_room_id"], sourceRoom.ID)
	}
	snapshotRoom, ok := redirect["source_room"].(map[string]any)
	if !ok || snapshotRoom["name"] != "General" {
		t.Fatalf("source_room = %#v, want the frozen room snapshot", redirect["source_room"])
	}
	messages, ok := redirect["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v, want 2 entries", redirect["messages"])
	}
	ordered := make([]string, 0, len(messages))
	for _, entry := range messages {
		object, _ := entry.(map[string]any)
		ordered = append(ordered, object["id"].(string))
	}
	if ordered[0] != first.ID.String() || ordered[1] != second.ID.String() {
		t.Fatalf("messages order = %v, want chronological", ordered)
	}
	senderMap, ok := redirect["sender_map"].(map[string]any)
	if !ok {
		t.Fatalf("sender_map = %#v, want an object keyed by member id", redirect["sender_map"])
	}
	sender, _ := senderMap[identity.ID.String()].(map[string]any)
	account, _ := sender["account"].(map[string]any)
	if account["id"] != identity.AccountID.String() {
		t.Fatalf("sender account = %#v, want the source account", account)
	}
	rng, _ := redirect["range"].(map[string]any)
	if rng["message_count"] != float64(2) || rng["start_message_id"] != first.ID.String() {
		t.Fatalf("range = %#v, want the two-message range", rng)
	}
	destination, _ := redirect["redirected_to_room"].(map[string]any)
	if destination["id"] != room.ID.String() {
		t.Fatalf("redirected_to_room = %#v, want the destination room", redirect["redirected_to_room"])
	}
}

// TestRedirectSingleMessageUsesVersionOne keeps the single-message snapshot the
// inline redirect content renders from.
func TestRedirectSingleMessageUsesVersionOne(t *testing.T) {
	i, db, room, identity := newRedirectFixture(t)

	sourceRoom := &store.ChatRoomEntity{ID: uuid.New(), Type: int(model.ChatRoomGroup)}
	if err := db.Create(sourceRoom).Error; err != nil {
		t.Fatalf("create source room: %v", err)
	}
	if err := db.Create(&store.ChatMemberEntity{
		ID: uuid.New(), ChatRoomID: sourceRoom.ID, AccountID: identity.AccountID,
		JoinedAt: new(time.Now().UTC().Add(-time.Hour)),
	}).Error; err != nil {
		t.Fatalf("create source member: %v", err)
	}
	createdAt := time.Now().UTC().Add(-time.Minute)
	message := &store.ChatMessageEntity{
		ID: uuid.New(), Type: "text", SenderID: identity.ID, ChatRoomID: sourceRoom.ID,
		Content: new("only one"), CreatedAt: createdAt, UpdatedAt: createdAt,
		Attachments: store.ToJSON([]model.CloudFileReferenceObject{}), Meta: store.ToJSON(map[string]any{}),
	}
	if err := db.Create(message).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}

	recorder, c := redirectRequest(t, room.ID, identity.AccountID, message.ID)
	i.redirectMessages(c)
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal message: %v (body %s)", err, recorder.Body.String())
	}
	redirect, _ := payload.Meta["redirect"].(map[string]any)
	if redirect["version"] != float64(1) {
		t.Fatalf("version = %v, want 1", redirect["version"])
	}
	if redirect["source_message_id"] != message.ID.String() {
		t.Fatalf("source_message_id = %v, want %s", redirect["source_message_id"], message.ID)
	}
	if redirect["source_content"] != "only one" {
		t.Fatalf("source_content = %v, want the frozen content", redirect["source_content"])
	}
	if redirect["source_sender_name"] != "Redirector" {
		t.Fatalf("source_sender_name = %v, want the sender nick", redirect["source_sender_name"])
	}
}

func newRedirectFixture(t *testing.T) (*instance, *gorm.DB, *store.ChatRoomEntity, *store.ChatMemberEntity) {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(
		&store.ChatRoomEntity{},
		&store.ChatMemberEntity{},
		&store.ChatMessageEntity{},
		&store.ChatRoomCounterEntity{},
	); err != nil {
		t.Fatalf("AutoMigrate() error = %v", err)
	}

	room := &store.ChatRoomEntity{ID: uuid.New(), Type: int(model.ChatRoomGroup)}
	if err := db.Create(room).Error; err != nil {
		t.Fatalf("create destination room: %v", err)
	}
	accountID := uuid.New()
	member := &store.ChatMemberEntity{
		ID: uuid.New(), ChatRoomID: room.ID, AccountID: accountID,
		Nick: new("Redirector"), JoinedAt: new(time.Now().UTC()),
	}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("create destination member: %v", err)
	}

	i := &instance{
		db:  db,
		st:  store.New(db),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		accounts: stubAccounts{byID: map[string]*gen.DyAccount{
			accountID.String(): {Id: accountID.String(), Name: "redirector", Nick: "Redirector"},
		}},
	}
	return i, db, room, member
}

func redirectRequest(
	t *testing.T,
	roomID uuid.UUID,
	accountID uuid.UUID,
	messageIDs ...uuid.UUID,
) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	quoted := make([]string, 0, len(messageIDs))
	for _, id := range messageIDs {
		quoted = append(quoted, `"`+id.String()+`"`)
	}
	body := `{"message_ids":[` + strings.Join(quoted, ",") + `]}`

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(
		http.MethodPost,
		"/chat/"+roomID.String()+"/messages/redirect",
		strings.NewReader(body),
	)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: roomID.String()}}
	c.Set(authn.ContextKeyUser, &gen.DyAccount{Id: accountID.String(), Nick: "Redirector", Language: "en-US"})
	return recorder, c
}
