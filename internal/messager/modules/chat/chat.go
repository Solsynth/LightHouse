// Package chat ports DysonNetwork.Messager's chat feature: rooms, members,
// messages, reactions, pins, groups, invites and realm chat.
package chat

import (
	"context"
	"log/slog"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/core"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
)

// ModuleID is the chat module's registry ID.
const ModuleID = "app.solian.messager.chat"

// Factory returns the chat module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.messager.integrations"},
		Ports: []module.PortRequirement{
			{ID: ports.PortAccounts, Optional: true},
			{ID: ports.PortFiles, Optional: true},
			{ID: ports.PortNotify, Optional: true},
			{ID: ports.PortRealtime, Optional: true},
			{ID: ports.PortSphere, Optional: true},
		},
		Models: []any{
			store.ChatRoomEntity{},
			store.ChatMemberEntity{},
			store.ChatGroupEntity{},
			store.ChatMessageEntity{},
			store.ChatReactionEntity{},
			store.ChatMessagePinEntity{},
			store.ChatRoomCounterEntity{},
		},
		SQL: []module.SchemaHook{chatIndexes},
		Capabilities: []string{
			"chat.rooms", "chat.messages", "chat.search", "chat.reactions",
			"chat.pins", "chat.members", "chat.invites", "chat.groups",
			"chat.e2ee", "chat.realms", "chat.sync",
		},
		New: func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	host *module.Host[core.Services]

	db       *gorm.DB
	st       *store.Store
	log      *slog.Logger
	accounts ports.Accounts
	files    ports.Files
	notify   ports.Notify
	realtime ports.Realtime
	sphere   ports.Sphere

	realm gen.DyRealmServiceClient
	mls   gen.DyMlsServiceClient
	blade gen.WebSocketServiceClient
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.host = host
	i.db = host.Core.DB
	i.log = host.Log
	if i.log == nil {
		i.log = slog.Default()
	}
	if host.Core.Clients != nil {
		i.realm = host.Core.Clients.Realm
		i.mls = host.Core.Clients.Mls
		i.blade = host.Core.Clients.Blade
	}
	i.accounts, _ = module.Get[ports.Accounts](host.Ports, ports.PortAccounts)
	i.files, _ = module.Get[ports.Files](host.Ports, ports.PortFiles)
	i.notify, _ = module.Get[ports.Notify](host.Ports, ports.PortNotify)
	i.realtime, _ = module.Get[ports.Realtime](host.Ports, ports.PortRealtime)
	i.sphere, _ = module.Get[ports.Sphere](host.Ports, ports.PortSphere)

	i.registerChatRoutes(host.API)
	i.registerRoomRoutes(host.API)
	host.API.GET("/realms/:slug/chat", authn.RequireAuth(), i.listRealmChat)

	if host.Core.Jobs != nil {
		host.Core.Jobs.Cron("chat.placeholders.expire", "0 * * * * ?", i.expireStalePlaceholders)
	}
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func (i *instance) auth() gin.HandlerFunc { return authn.RequireAuth() }

func (i *instance) perm(key string) gin.HandlerFunc {
	return i.host.Core.Auth.RequirePermission(key)
}

func (i *instance) registerChatRoutes(api *gin.RouterGroup) {
	api.GET("/chat/summary", i.auth(), i.getChatSummary)
	api.GET("/chat/unread", i.auth(), i.getTotalUnread)
	api.POST("/chat/read-all", i.auth(), i.perm("chat.read.all"), i.markAllRead)

	api.GET("/chat/:id/subscriptions", i.auth(), i.getRoomSubscriptions)
	api.GET("/chat/accounts/me/subscriptions", i.auth(), i.getMyAccountSubscriptions)
	api.GET("/chat/accounts/me/status", i.auth(), i.getMyChatStatus)

	api.GET("/chat/messages/search", i.auth(), i.searchMessages)

	api.GET("/chat/:id/messages", i.listMessages)
	api.POST("/chat/:id/messages", i.auth(), i.perm("chat.messages.create"), i.sendMessage)
	api.POST("/chat/:id/messages/placeholder", i.auth(), i.perm("chat.messages.create"), i.sendPlaceholderMessage)
	api.GET("/chat/:id/messages/:messageId", i.getMessage)
	api.PATCH("/chat/:id/messages/:messageId", i.auth(), i.perm("chat.messages.update"), i.updateMessage)
	api.DELETE("/chat/:id/messages/:messageId", i.auth(), i.perm("chat.messages.delete"), i.deleteMessage)

	api.GET("/chat/:id/messages/:messageId/reactions", i.listMessageReactions)
	api.POST("/chat/:id/messages/:messageId/reactions", i.auth(), i.perm("chat.messages.react"), i.reactMessage)
	api.DELETE("/chat/:id/messages/:messageId/reactions/:symbol", i.auth(), i.perm("chat.messages.react"), i.removeReaction)
	api.GET("/chat/:id/messages/:messageId/thread", i.getMessageThread)

	api.POST("/chat/:id/sync", i.roomSyncData)
	api.POST("/chat/sync", i.auth(), i.perm("chat.sync"), i.globalSyncData)
	api.POST("/chat/:id/autocomplete", i.autocompleteChat)
	api.POST("/chat/:id/devices/me/joined", i.auth(), i.deviceJoined)

	api.POST("/chat/:id/pins", i.auth(), i.perm("chat.pins.manage"), i.pinMessage)
	api.GET("/chat/:id/pins", i.listPins)
	api.DELETE("/chat/:id/pins/:pinId", i.auth(), i.perm("chat.pins.manage"), i.unpinMessage)
	api.GET("/chat/:id/bots/commands", i.botCommands)
}

func (i *instance) registerRoomRoutes(api *gin.RouterGroup) {
	api.GET("/chat/:id", i.getChatRoom)
	api.GET("/chat/public/:scope/:chatSlug", i.getChatRoomBySlug)
	api.GET("/chat", i.auth(), i.listJoinedRooms)
	api.POST("/chat/rooms/sync", i.auth(), i.perm("chat.sync"), i.syncJoinedRooms)
	api.POST("/chat/direct", i.auth(), i.perm("chat.create"), i.createDirectMessage)
	api.POST("/chat/:id/confirm", i.auth(), i.confirmDirectMessage)
	api.POST("/chat/:id/block", i.auth(), i.blockDirectMessage)
	api.DELETE("/chat/:id/block", i.auth(), i.unblockDirectMessage)
	api.GET("/chat/direct/:accountId", i.auth(), i.getDirectChatRoom)

	api.POST("/chat", i.auth(), i.perm("chat.create"), i.createChatRoom)
	api.PATCH("/chat/:id", i.perm("chat.update"), i.updateChatRoom)
	api.POST("/chat/:id/e2ee/enable", i.auth(), i.enableE2eeLegacy)
	api.POST("/chat/:id/mls/enable", i.auth(), i.enableMls)
	api.DELETE("/chat/:id", i.perm("chat.delete"), i.deleteChatRoom)

	api.GET("/chat/:id/members/me", i.auth(), i.getRoomIdentity)
	api.POST("/chat/:id/members/me", i.auth(), i.joinChatRoom)
	api.DELETE("/chat/:id/members/me", i.auth(), i.leaveChat)
	api.PATCH("/chat/:id/members/me/profile", i.auth(), i.perm("chat.members.manage"), i.updateRoomIdentity)
	api.DELETE("/chat/:id/members/me/profile", i.auth(), i.clearRoomIdentity)
	api.PATCH("/chat/:id/members/me/notify", i.auth(), i.updateMemberNotify)
	api.GET("/chat/:id/members/online", i.membersOnline)
	api.GET("/chat/:id/members", i.listMembers)

	api.POST("/chat/invites/:id", i.auth(), i.perm("chat.invites.manage"), i.inviteMember)
	api.GET("/chat/invites", i.auth(), i.listInvites)
	api.POST("/chat/invites/:id/accept", i.auth(), i.acceptInvite)
	api.POST("/chat/invites/:id/decline", i.auth(), i.declineInvite)

	api.POST("/chat/:id/members/:memberId/timeout", i.auth(), i.perm("chat.members.timeout"), i.timeoutChatMember)
	api.DELETE("/chat/:id/members/:memberId/timeout", i.auth(), i.removeChatMemberTimeout)
	api.DELETE("/chat/:id/members/:memberId", i.auth(), i.perm("chat.members.kick"), i.removeChatMember)

	api.GET("/chat/groups", i.auth(), i.listGroups)
	api.POST("/chat/groups", i.auth(), i.perm("chat.groups.manage"), i.createGroup)
	api.PATCH("/chat/groups/:groupId", i.auth(), i.updateGroup)
	api.DELETE("/chat/groups/:groupId", i.auth(), i.deleteGroup)
	api.PATCH("/chat/rooms/:roomId/group", i.auth(), i.moveToGroup)

	api.DELETE("/chat/rooms/:roomId/messages/:messageId", i.auth(), i.perm("chat.messages.delete"), i.moderateDeleteMessage)
	api.POST("/chat/rooms/:roomId/members/:accountId/timeout", i.auth(), i.moderateTimeoutMember)
}
