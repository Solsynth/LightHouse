package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/accountjson"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
)

func currentUser(c *gin.Context) *gen.DyAccount { return authn.CurrentUser(c) }

// --- errors ---------------------------------------------------------------

func failConflict(c *gin.Context, code, message string) {
	httpx.Fail(c, errs.New(strings.ToUpper(code), message, http.StatusConflict))
}

func failForbidden(c *gin.Context, message string) {
	httpx.Fail(c, errs.Forbidden(message))
}

func failBadRequest(c *gin.Context, code, message string) {
	httpx.Fail(c, errs.BadRequest(code, message))
}

func failNotFound(c *gin.Context) {
	httpx.Fail(c, errs.NotFound("The requested resource was not found."))
}

// noContent mirrors ASP.NET's NoContent(): a bare 204 without a body.
func noContent(c *gin.Context) { c.Status(http.StatusNoContent) }

func failValidation(c *gin.Context, field, message, code string) {
	apiErr := errs.Validation(map[string][]string{field: {message}})
	if code != "" {
		apiErr.Code = code
	}
	httpx.Fail(c, apiErr)
}

func failInternal(c *gin.Context, err error) {
	if i, ok := c.Get("chat_instance"); ok {
		if inst, _ := i.(*instance); inst != nil && inst.log != nil {
			inst.log.Error("chat request failed", "error", err, "path", c.FullPath())
		}
	}
	httpx.Internal(c)
}

func uuidParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		failNotFound(c)
		return uuid.Nil, false
	}
	return id, true
}

// --- proto / json ---------------------------------------------------------

// protoJSON embeds a fleet proto in a chat payload with the C# wire shape
// (jsonb columns parsed, enums numeric, 64-bit integers unquoted).
func protoJSON(value proto.Message) any {
	return accountjson.Message(value)
}

func jsonMap(raw datatypes.JSON) map[string]any {
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func jsonStringList(raw datatypes.JSON) []string {
	out := []string{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = []string{}
	}
	return out
}

// --- time -----------------------------------------------------------------

func utcNow() time.Time { return time.Now().UTC() }

func timePtr(value *time.Time) *model.Time { return store.TimePtr(value) }

// --- accounts and members -------------------------------------------------

func (i *instance) accountsByID(ctx context.Context, ids []string) map[string]*gen.DyAccount {
	out := map[string]*gen.DyAccount{}
	if i.accounts == nil || len(ids) == 0 {
		return out
	}
	unique := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	accounts, err := i.accounts.GetAccounts(ctx, unique)
	if err != nil {
		i.log.Warn("chat: loading accounts failed", "error", err)
		return out
	}
	for _, account := range accounts {
		out[account.GetId()] = account
	}
	return out
}

func (i *instance) account(ctx context.Context, id string) *gen.DyAccount {
	return i.accountsByID(ctx, []string{id})[id]
}

// memberModels hydrates member rows with their account snapshots and realm
// identity, mirroring ChatRoomService.LoadMemberAccounts + ApplyRealmIdentity.
func (i *instance) memberModels(ctx context.Context, entities []*store.ChatMemberEntity) []model.ChatMember {
	out := make([]model.ChatMember, 0, len(entities))
	if len(entities) == 0 {
		return out
	}
	ids := make([]string, 0, len(entities))
	for _, entity := range entities {
		ids = append(ids, entity.AccountID.String())
	}
	accounts := i.accountsByID(ctx, ids)
	for _, entity := range entities {
		member := store.MemberToModel(entity)
		if account := accounts[entity.AccountID.String()]; account != nil {
			member.Account = protoJSON(account)
		}
		out = append(out, member)
	}
	i.applyRealmIdentity(ctx, out)
	return out
}

// applyRealmIdentity fills the realm profile fields for members whose room is
// owned by a realm.
func (i *instance) applyRealmIdentity(ctx context.Context, members []model.ChatMember) {
	if i.realm == nil || len(members) == 0 {
		return
	}
	roomIDs := make([]uuid.UUID, 0, len(members))
	seenRoom := map[uuid.UUID]bool{}
	for _, member := range members {
		id, err := uuid.Parse(member.ChatRoomID)
		if err != nil || seenRoom[id] {
			continue
		}
		seenRoom[id] = true
		roomIDs = append(roomIDs, id)
	}
	if len(roomIDs) == 0 {
		return
	}
	var rooms []store.ChatRoomEntity
	if err := i.db.WithContext(ctx).Where("id IN ? AND realm_id IS NOT NULL", roomIDs).Find(&rooms).Error; err != nil {
		return
	}
	roomRealm := map[string]string{}
	for _, room := range rooms {
		if room.RealmID != nil {
			roomRealm[room.ID.String()] = room.RealmID.String()
		}
	}
	if len(roomRealm) == 0 {
		return
	}
	placeholders := make([]*gen.DyRealmMember, 0, len(members))
	seen := map[string]bool{}
	for _, member := range members {
		realmID, ok := roomRealm[member.ChatRoomID]
		if !ok {
			continue
		}
		key := realmID + ":" + member.AccountID
		if seen[key] {
			continue
		}
		seen[key] = true
		placeholders = append(placeholders, &gen.DyRealmMember{RealmId: realmID, AccountId: member.AccountID})
	}
	if len(placeholders) == 0 {
		return
	}
	resp, err := i.realm.LoadMemberAccounts(ctx, &gen.DyLoadMemberAccountsRequest{Members: placeholders})
	if err != nil {
		i.log.Debug("chat: realm identity hydration failed", "error", err)
		return
	}
	type key struct{ realm, account string }
	byKey := map[key]*gen.DyRealmMember{}
	for _, realmMember := range resp.GetMembers() {
		byKey[key{realmMember.GetRealmId(), realmMember.GetAccountId()}] = realmMember
	}
	for idx := range members {
		realmID, ok := roomRealm[members[idx].ChatRoomID]
		if !ok {
			continue
		}
		realmMember, ok := byKey[key{realmID, members[idx].AccountID}]
		if !ok {
			continue
		}
		nick := realmMember.GetNick()
		bio := realmMember.GetBio()
		experience := int(realmMember.GetExperience())
		level := int(realmMember.GetLevel())
		progress := realmMember.GetLevelingProgress()
		members[idx].RealmNick = &nick
		members[idx].RealmBio = &bio
		members[idx].RealmExperience = &experience
		members[idx].RealmLevel = &level
		members[idx].RealmLevelingProgress = &progress
		if label := realmMember.GetLabel(); label != nil {
			// The C# snapshot rebuilt a full SnRealmLabel from the partial
			// proto and serialized it as an object. Clients cast realm_label
			// as a map, so emitting the bare name broke the member.
			zero := model.Time(time.Time{})
			labelID := realmMember.GetLabelId()
			if labelID == "" {
				labelID = uuid.Nil.String()
			}
			members[idx].RealmLabel = &model.ChatRealmLabel{
				ID:                 labelID,
				RealmID:            realmID,
				Name:               label.GetName(),
				Description:        optionalLabelText(label.GetDescription()),
				Color:              optionalLabelText(label.GetColor()),
				Icon:               optionalLabelText(label.GetIcon()),
				CreatedByAccountID: uuid.Nil.String(),
				CreatedAt:          &zero,
				UpdatedAt:          &zero,
			}
		}
	}
}

// optionalLabelText mirrors SnRealmLabel.FromProtoValue: blank proto strings
// become JSON null.
func optionalLabelText(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func (i *instance) accountStatuses(ctx context.Context, accountIDs []string) map[string]*gen.DyAccountStatus {
	out := map[string]*gen.DyAccountStatus{}
	if i.host == nil || i.host.Core.Clients == nil || i.host.Core.Clients.Profile == nil || len(accountIDs) == 0 {
		return out
	}
	resp, err := i.host.Core.Clients.Profile.GetAccountStatusBatch(ctx, &gen.DyGetAccountBatchRequest{Id: accountIDs})
	if err != nil {
		i.log.Warn("chat: loading account statuses failed", "error", err)
		return out
	}
	for _, status := range resp.GetStatuses() {
		out[status.GetAccountId()] = status
	}
	return out
}

// --- rooms ----------------------------------------------------------------

// readableRoom applies the C# GetReadableRoomAsync rule: the room must exist
// and private/encrypted rooms require an active membership.
func (i *instance) readableRoom(ctx context.Context, c *gin.Context, roomID uuid.UUID) (*store.ChatRoomEntity, bool) {
	room, err := i.st.GetRoom(ctx, roomID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			failNotFound(c)
		} else {
			failInternal(c, err)
		}
		return nil, false
	}
	if room.EncryptionMode != int(model.ChatEncryptionNone) || !room.IsPublic {
		accountID := currentAccountID(c)
		if accountID == "" {
			httpx.Unauthorized(c)
			return nil, false
		}
		member, err := i.activeMember(ctx, roomID, accountID)
		if err != nil {
			failForbidden(c, "You are not a member of this chat room.")
			return nil, false
		}
		_ = member
	}
	return room, true
}

func currentAccountID(c *gin.Context) string {
	if user := currentUser(c); user != nil {
		return user.GetId()
	}
	return ""
}

func (i *instance) activeMember(ctx context.Context, roomID uuid.UUID, accountID string) (*store.ChatMemberEntity, error) {
	accountUUID, err := uuid.Parse(accountID)
	if err != nil {
		return nil, err
	}
	member, err := i.st.GetMember(ctx, roomID, accountUUID)
	if err != nil {
		return nil, err
	}
	if member.JoinedAt == nil || member.LeaveAt != nil {
		return nil, store.ErrNotFound
	}
	return member, nil
}

func (i *instance) isChatMember(ctx context.Context, roomID uuid.UUID, accountID string) bool {
	_, err := i.activeMember(ctx, roomID, accountID)
	return err == nil
}

func (i *instance) activeMemberEntities(ctx context.Context, roomID uuid.UUID) []*store.ChatMemberEntity {
	var members []*store.ChatMemberEntity
	_ = i.db.WithContext(ctx).
		Where("chat_room_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", roomID).
		Find(&members).Error
	return members
}

func (i *instance) roomAccountIDs(ctx context.Context, roomID uuid.UUID) []string {
	members := i.activeMemberEntities(ctx, roomID)
	ids := make([]string, 0, len(members))
	seen := map[string]bool{}
	for _, member := range members {
		id := member.AccountID.String()
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

func (i *instance) roomBySlug(ctx context.Context, scope, chatSlug string) (*store.ChatRoomEntity, error) {
	normalized := strings.ToLower(strings.TrimSpace(chatSlug))
	// Account scope first.
	if i.accounts != nil {
		accounts, err := i.accounts.LookupAccounts(ctx, []string{scope})
		if err == nil && len(accounts) > 0 {
			var room store.ChatRoomEntity
			err := i.db.WithContext(ctx).
				Where("account_id = ? AND realm_id IS NULL AND slug = ?", accounts[0].GetId(), normalized).
				First(&room).Error
			if err == nil {
				return &room, nil
			}
		}
	}
	if i.realm != nil {
		realm, err := i.realm.GetRealm(ctx, &gen.DyGetRealmRequest{
			Query: &gen.DyGetRealmRequest_Slug{Slug: scope},
		})
		if err == nil && realm != nil {
			var room store.ChatRoomEntity
			if err := i.db.WithContext(ctx).
				Where("realm_id = ? AND slug = ?", realm.GetId(), normalized).
				First(&room).Error; err == nil {
				return &room, nil
			}
		}
	}
	return nil, store.ErrNotFound
}

func (i *instance) slugAvailable(ctx context.Context, slug, ownerAccountID string, realmID *uuid.UUID, exclude *uuid.UUID) bool {
	query := i.db.WithContext(ctx).Model(&store.ChatRoomEntity{}).Where("slug = ?", slug)
	if realmID != nil {
		query = query.Where("realm_id = ?", *realmID)
	} else {
		owner, err := uuid.Parse(ownerAccountID)
		if err != nil {
			return false
		}
		query = query.Where("account_id = ? AND realm_id IS NULL", owner)
	}
	if exclude != nil {
		query = query.Where("id <> ?", *exclude)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false
	}
	return count == 0
}

var chatRoomSlugRegex = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9\-_.]*[a-z0-9])?$`)

func normalizeSlug(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	normalized := strings.ToLower(strings.TrimSpace(*value))
	return &normalized
}

func (i *instance) isRealmModerator(ctx context.Context, realmID *uuid.UUID, accountID string) bool {
	if realmID == nil {
		return false
	}
	if i.realm == nil {
		return false
	}
	value, err := i.realm.IsMemberWithRole(ctx, &gen.DyIsMemberWithRoleRequest{
		RealmId:       realmID.String(),
		AccountId:     accountID,
		RequiredRoles: []int32{50},
	})
	return err == nil && value.GetValue()
}

func (i *instance) isRealmMember(ctx context.Context, realmID string, accountID string) bool {
	if i.realm == nil {
		return false
	}
	value, err := i.realm.IsMemberWithRole(ctx, &gen.DyIsMemberWithRoleRequest{
		RealmId:       realmID,
		AccountId:     accountID,
		RequiredRoles: []int32{0},
	})
	return err == nil && value.GetValue()
}

func (i *instance) hasRealmPermission(ctx context.Context, realmID *uuid.UUID, accountID, permission string) bool {
	if realmID == nil || i.realm == nil {
		return false
	}
	value, err := i.realm.HasPermission(ctx, &gen.DyHasRealmPermissionRequest{
		RealmId:    realmID.String(),
		AccountId:  accountID,
		Permission: permission,
	})
	return err == nil && value.GetValue()
}

func (i *instance) realmBySlug(ctx context.Context, slug string) (*gen.DyRealm, error) {
	if i.realm == nil {
		return nil, portsUnavailable()
	}
	return i.realm.GetRealm(ctx, &gen.DyGetRealmRequest{Query: &gen.DyGetRealmRequest_Slug{Slug: slug}})
}

func (i *instance) hydrateRooms(ctx context.Context, rooms []*store.ChatRoomEntity, viewerAccountID string) []model.ChatRoom {
	out := make([]model.ChatRoom, 0, len(rooms))
	if len(rooms) == 0 {
		return out
	}
	realmIDs := make([]string, 0, len(rooms))
	for _, room := range rooms {
		if room.RealmID != nil {
			realmIDs = append(realmIDs, room.RealmID.String())
		}
	}
	realms := map[string]*gen.DyRealm{}
	if i.realm != nil && len(realmIDs) > 0 {
		if resp, err := i.realm.GetRealmBatch(ctx, &gen.DyGetRealmBatchRequest{Ids: realmIDs}); err == nil {
			for _, realm := range resp.GetRealms() {
				realms[realm.GetId()] = realm
			}
		}
	}
	directRoomIDs := make([]uuid.UUID, 0, len(rooms))
	for _, room := range rooms {
		if room.Type == int(model.ChatRoomDirectMessage) {
			directRoomIDs = append(directRoomIDs, room.ID)
		}
	}
	directMembers := map[string][]*store.ChatMemberEntity{}
	if len(directRoomIDs) > 0 {
		var members []*store.ChatMemberEntity
		query := i.db.WithContext(ctx).Where("chat_room_id IN ?", directRoomIDs)
		if viewerAccountID != "" {
			query = query.Where("account_id <> ?", viewerAccountID)
		}
		_ = query.Find(&members).Error
		for _, member := range members {
			directMembers[member.ChatRoomID.String()] = append(directMembers[member.ChatRoomID.String()], member)
		}
	}
	for _, room := range rooms {
		mapped := store.RoomToModel(room)
		if room.RealmID != nil {
			if realm, ok := realms[room.RealmID.String()]; ok {
				mapped.Realm = protoJSON(realm)
			}
		}
		if room.Type == int(model.ChatRoomDirectMessage) {
			if members, ok := directMembers[room.ID.String()]; ok {
				mapped.Members = i.memberModels(ctx, members)
			}
		}
		out = append(out, mapped)
	}
	return out
}

// --- messages -------------------------------------------------------------

func (i *instance) insertMessage(ctx context.Context, tx *gorm.DB, entity *store.ChatMessageEntity) error {
	run := func(handle *gorm.DB) error {
		sequence, err := i.st.NextRoomSequence(ctx, handle, entity.ChatRoomID)
		if err != nil {
			return err
		}
		entity.RoomSequence = sequence
		return handle.WithContext(ctx).Create(entity).Error
	}
	if tx == nil || tx == i.db {
		return i.db.WithContext(ctx).Transaction(run)
	}
	return run(tx)
}

func (i *instance) messageModel(ctx context.Context, entity *store.ChatMessageEntity) model.ChatMessage {
	message := store.MessageToModel(entity)
	var sender *store.ChatMemberEntity
	if err := i.db.WithContext(ctx).Where("id = ?", entity.SenderID).First(&sender).Error; err == nil && sender != nil {
		members := i.memberModels(ctx, []*store.ChatMemberEntity{sender})
		if len(members) > 0 {
			message.Sender = &members[0]
		}
	}
	return message
}

// hydrateMessages loads senders, reaction flags and thread counters for a page
// of messages, mirroring the C# controller/service hydration chain.
func (i *instance) hydrateMessages(ctx context.Context, entities []*store.ChatMessageEntity, viewerAccountID string) []model.ChatMessage {
	if len(entities) == 0 {
		return []model.ChatMessage{}
	}
	senderIDs := make([]uuid.UUID, 0, len(entities))
	for _, entity := range entities {
		senderIDs = append(senderIDs, entity.SenderID)
	}
	var senders []*store.ChatMemberEntity
	_ = i.db.WithContext(ctx).Where("id IN ?", senderIDs).Find(&senders).Error
	senderModels := i.memberModels(ctx, senders)
	senderMap := map[string]model.ChatMember{}
	for _, sender := range senderModels {
		senderMap[sender.ID] = sender
	}

	messages := make([]model.ChatMessage, 0, len(entities))
	for _, entity := range entities {
		message := store.MessageToModel(entity)
		if sender, ok := senderMap[entity.SenderID.String()]; ok {
			senderCopy := sender
			message.Sender = &senderCopy
		}
		messages = append(messages, message)
	}
	i.hydrateReactions(ctx, messages, viewerAccountID)
	i.hydrateThreads(ctx, messages)
	escapeMarkdownHeadings(messages)
	return messages
}

func (i *instance) hydrateReactions(ctx context.Context, messages []model.ChatMessage, viewerAccountID string) {
	if len(messages) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(messages))
	for _, message := range messages {
		if id, err := uuid.Parse(message.ID); err == nil {
			ids = append(ids, id)
		}
	}
	made := map[string]map[string]bool{}
	if viewerAccountID != "" {
		type row struct {
			MessageID uuid.UUID
			Symbol    string
		}
		var rows []row
		_ = i.db.WithContext(ctx).Raw(
			`SELECT r.message_id, r.symbol FROM chat_reactions r
			 JOIN chat_members m ON m.id = r.sender_id
			 WHERE r.message_id IN ? AND m.account_id = ? AND r.deleted_at IS NULL`,
			ids, viewerAccountID,
		).Scan(&rows).Error
		for _, item := range rows {
			key := item.MessageID.String()
			if made[key] == nil {
				made[key] = map[string]bool{}
			}
			made[key][item.Symbol] = true
		}
	}
	for idx := range messages {
		if viewerAccountID == "" {
			messages[idx].ReactionsMade = nil
			continue
		}
		flags := made[messages[idx].ID]
		if flags == nil {
			flags = map[string]bool{}
		}
		messages[idx].ReactionsMade = flags
	}
}

func (i *instance) hydrateThreads(ctx context.Context, messages []model.ChatMessage) {
	if len(messages) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(messages))
	for _, message := range messages {
		if id, err := uuid.Parse(message.ID); err == nil {
			ids = append(ids, id)
		}
	}
	type row struct {
		ThreadID uuid.UUID
		Count    int
	}
	var rows []row
	_ = i.db.WithContext(ctx).Raw(
		`SELECT thread_id, COUNT(*)::int AS count FROM chat_messages
		 WHERE thread_id IN ? AND deleted_at IS NULL GROUP BY thread_id`, ids,
	).Scan(&rows).Error
	counts := map[string]int{}
	for _, item := range rows {
		counts[item.ThreadID.String()] = item.Count
	}
	for idx := range messages {
		count := counts[messages[idx].ID]
		messages[idx].ThreadRepliesCount = count
		messages[idx].IsThreadRoot = count > 0
	}
}

func escapeMarkdownHeadings(messages []model.ChatMessage) {
	for idx := range messages {
		if messages[idx].EncryptionMeta != nil || messages[idx].Content == nil || *messages[idx].Content == "" {
			continue
		}
		escaped := escapeMarkdownHeadingMarkers(*messages[idx].Content)
		messages[idx].Content = &escaped
	}
}

func escapeMarkdownHeadingMarkers(content string) string {
	lines := strings.Split(content, "\n")
	inFence := false
	fenceMarker := ""
	for idx, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && (trimmed[0] == '`' || trimmed[0] == '~') {
			marker := trimmed[0]
			run := 0
			for run < len(trimmed) && trimmed[run] == marker {
				run++
			}
			if run >= 3 {
				if !inFence {
					inFence = true
					fenceMarker = strings.Repeat(string(marker), run)
				} else if strings.HasPrefix(trimmed, fenceMarker) && strings.TrimSpace(trimmed[len(fenceMarker):]) == "" {
					inFence = false
					fenceMarker = ""
				}
			}
			continue
		}
		if inFence {
			continue
		}
		index := 0
		for index < len(line) && index < 3 && line[index] == ' ' {
			index++
		}
		hashStart := index
		for index < len(line) && line[index] == '#' && index-hashStart < 6 {
			index++
		}
		if index-hashStart >= 1 && index-hashStart <= 6 && (index == len(line) || line[index] == ' ' || line[index] == '\t') {
			lines[idx] = line[:hashStart] + "\\" + line[hashStart:]
		}
	}
	return strings.Join(lines, "\n")
}

func (i *instance) latestMessage(ctx context.Context, roomID uuid.UUID, viewerAccountID string) *model.ChatMessage {
	var entity store.ChatMessageEntity
	err := i.db.WithContext(ctx).Unscoped().
		Where("chat_room_id = ?", roomID).
		Order("created_at DESC").First(&entity).Error
	if err != nil {
		return nil
	}
	messages := i.hydrateMessages(ctx, []*store.ChatMessageEntity{&entity}, viewerAccountID)
	if len(messages) == 0 {
		return nil
	}
	return &messages[0]
}

// --- text -----------------------------------------------------------------

// sanitizeText mirrors TextSanitizer.Sanitize: control characters other than
// \n\r\t are dropped, as are format characters and non-spacing marks.
func sanitizeText(text *string) *string {
	if text == nil || *text == "" {
		return text
	}
	var builder strings.Builder
	builder.Grow(len(*text))
	for _, r := range *text {
		if unicode.Is(unicode.Cc, r) {
			if r == '\n' || r == '\r' || r == '\t' {
				builder.WriteRune(r)
			}
			continue
		}
		if unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Mn, r) {
			continue
		}
		builder.WriteRune(r)
	}
	sanitized := builder.String()
	return &sanitized
}

var mentionRegex = regexp.MustCompile(`@(?:u/)?([A-Za-z0-9_-]+)`)

func (i *instance) extractMentions(ctx context.Context, content *string, repliedMessageID, forwardedMessageID *uuid.UUID, roomID uuid.UUID, excludeAccountID string) []string {
	mentions := []string{}
	seen := map[string]bool{}
	add := func(accountID string) {
		if accountID == "" || seen[accountID] || accountID == excludeAccountID {
			return
		}
		seen[accountID] = true
		mentions = append(mentions, accountID)
	}
	if repliedMessageID != nil {
		var replied store.ChatMessageEntity
		if err := i.db.WithContext(ctx).Where("id = ? AND chat_room_id = ?", *repliedMessageID, roomID).First(&replied).Error; err == nil {
			var member store.ChatMemberEntity
			if err := i.db.WithContext(ctx).Where("id = ?", replied.SenderID).First(&member).Error; err == nil {
				add(member.AccountID.String())
			}
		}
	}
	if forwardedMessageID != nil {
		var forwarded store.ChatMessageEntity
		if err := i.db.WithContext(ctx).Where("id = ?", *forwardedMessageID).First(&forwarded).Error; err == nil {
			var member store.ChatMemberEntity
			if err := i.db.WithContext(ctx).Where("id = ?", forwarded.SenderID).First(&member).Error; err == nil {
				add(member.AccountID.String())
			}
		}
	}
	if content == nil || strings.TrimSpace(*content) == "" {
		return mentions
	}
	names := []string{}
	seenName := map[string]bool{}
	for _, match := range mentionRegex.FindAllStringSubmatch(*content, -1) {
		name := match[1]
		if seenName[name] {
			continue
		}
		seenName[name] = true
		names = append(names, name)
	}
	if len(names) == 0 || i.accounts == nil {
		return mentions
	}
	accounts, err := i.accounts.LookupAccounts(ctx, names)
	if err != nil {
		return mentions
	}
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.GetId())
	}
	if len(ids) == 0 {
		return mentions
	}
	var memberIDs []string
	_ = i.db.WithContext(ctx).Model(&store.ChatMemberEntity{}).
		Where("chat_room_id = ? AND account_id IN ? AND joined_at IS NOT NULL AND leave_at IS NULL", roomID, ids).
		Pluck("account_id", &memberIDs).Error
	for _, id := range memberIDs {
		add(id)
	}
	return mentions
}

// --- embeds ---------------------------------------------------------------

func messageMeta(entity *store.ChatMessageEntity) map[string]any {
	return jsonMap(entity.Meta)
}

func addEmbed(meta map[string]any, embed map[string]any) {
	embeds, _ := meta["embeds"].([]any)
	embeds = append(embeds, embed)
	meta["embeds"] = embeds
}

func removeEmbed(meta map[string]any, embedType string) {
	embeds, _ := meta["embeds"].([]any)
	filtered := make([]any, 0, len(embeds))
	for _, item := range embeds {
		entry, ok := item.(map[string]any)
		if ok {
			if value, exists := entry["type"]; exists && value == embedType {
				continue
			}
		}
		filtered = append(filtered, item)
	}
	meta["embeds"] = filtered
}

func hasEmbed(meta map[string]any, embedType string) bool {
	embeds, _ := meta["embeds"].([]any)
	for _, item := range embeds {
		if entry, ok := item.(map[string]any); ok {
			if value, exists := entry["type"]; exists && value == embedType {
				return true
			}
		}
	}
	return false
}

var wktRegex = regexp.MustCompile(`(?i)^(POINT|LINESTRING|POLYGON|MULTIPOINT|MULTILINESTRING|MULTIPOLYGON|GEOMETRYCOLLECTION)\s*(Z|M|ZM)?\s*\(.*\)\s*$`)

func validWKT(value *string) bool {
	if value == nil || strings.TrimSpace(*value) == "" {
		return true
	}
	text := strings.TrimSpace(*value)
	if !wktRegex.MatchString(text) {
		return false
	}
	depth := 0
	for _, r := range text {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

func hasLocationPayload(name, address, wkt *string) bool {
	return nonEmpty(name) || nonEmpty(address) || nonEmpty(wkt)
}

func nonEmpty(value *string) bool { return value != nil && strings.TrimSpace(*value) != "" }

func locationEmbed(name, address, wkt *string) map[string]any {
	embed := map[string]any{"type": "location"}
	if nonEmpty(name) {
		embed["name"] = *name
	} else {
		embed["name"] = nil
	}
	if nonEmpty(address) {
		embed["address"] = *address
	} else {
		embed["address"] = nil
	}
	if nonEmpty(wkt) {
		embed["wkt"] = strings.TrimSpace(*wkt)
	} else {
		embed["wkt"] = nil
	}
	return embed
}

// --- realtime -------------------------------------------------------------

func (i *instance) broadcast(ctx context.Context, roomID uuid.UUID, event string, payload any) {
	if i.realtime == nil {
		return
	}
	ids := i.roomAccountIDs(ctx, roomID)
	if len(ids) == 0 {
		return
	}
	if err := i.realtime.Push(ctx, ids, event, payload); err != nil {
		i.log.Warn("chat: realtime push failed", "event", event, "room_id", roomID, "error", err)
	}
}

func (i *instance) pushToAccounts(ctx context.Context, accountIDs []string, event string, payload any) {
	if i.realtime == nil || len(accountIDs) == 0 {
		return
	}
	if err := i.realtime.Push(ctx, accountIDs, event, payload); err != nil {
		i.log.Warn("chat: realtime push failed", "event", event, "error", err)
	}
}

func (i *instance) sendPush(ctx context.Context, accountIDs []string, topic, title, body string, meta map[string]string) {
	if i.notify == nil || len(accountIDs) == 0 {
		return
	}
	if err := i.notify.SendPushToAccounts(ctx, accountIDs, topic, title, body, meta); err != nil {
		i.log.Warn("chat: push notification failed", "topic", topic, "error", err)
	}
}

// --- misc -----------------------------------------------------------------

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func uuidPtr(value *uuid.UUID) *string { return store.UUIDPtrString(value) }

type unavailableError struct{}

func (unavailableError) Error() string { return "port provider unavailable" }

func portsUnavailable() error { return unavailableError{} }
