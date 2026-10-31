// Package realtime ports DysonNetwork.Messager's realtime call controller
// (LiveKit tokens and room administration) and its voice-message endpoints.
package realtime

import (
	"context"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/config"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/core"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

const (
	// ModuleID is the realtime module's registry ID.
	ModuleID = "app.solian.messager.realtime"
	// chatModuleID is the module whose rooms this module serves.
	chatModuleID = "app.solian.messager.chat"
	// providerName is the only realtime provider the port implements.
	providerName = "LiveKit"
)

// Factory returns the realtime module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{chatModuleID},
		Ports: []module.PortRequirement{
			{ID: ports.PortAccounts, Optional: true},
			{ID: ports.PortNotify, Optional: true},
			{ID: ports.PortRealtime, Optional: true},
		},
		Models: []any{
			store.RealtimeCallEntity{},
			store.ChatVoiceClipEntity{},
		},
		SQL:          []module.SchemaHook{indexHook},
		Capabilities: []string{"chat.realtime", "chat.voice"},
		New:          func() module.Instance[core.Services] { return &instance{} },
	}
}

// indexHook adds the lookup indexes the call and voice tables need.
func indexHook(ctx context.Context, db *gorm.DB) error {
	return module.DDL(
		`CREATE INDEX IF NOT EXISTS idx_chat_realtime_call_room_open
			ON chat_realtime_call (room_id, created_at DESC) WHERE ended_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_chat_voice_clips_created_at
			ON chat_voice_clips (created_at)`,
	)(ctx, db)
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	log := host.Log
	if log == nil {
		log = slog.Default()
	}

	s := &server{
		core: &services{Services: host.Core, Store: store.New(host.Core.DB)},
		log:  log,
	}
	s.livekit = newLiveKit(host.Core.Config, host.Core.Redis, log)
	if accounts, ok := module.Get[ports.Accounts](host.Ports, ports.PortAccounts); ok {
		s.accounts = accounts
	}
	if notify, ok := module.Get[ports.Notify](host.Ports, ports.PortNotify); ok {
		s.notify = notify
	}
	if realtime, ok := module.Get[ports.Realtime](host.Ports, ports.PortRealtime); ok {
		s.realtime = realtime
	}
	if host.Core.Clients != nil && host.Core.Clients.Ring != nil {
		s.ring = host.Core.Clients.Ring
	}
	s.voice = newVoiceService(s, host.Core.Config)

	requirePermission := func(key string) gin.HandlerFunc {
		if host.Core.Auth == nil {
			return func(c *gin.Context) { c.Next() }
		}
		return host.Core.Auth.RequirePermission(key)
	}

	calls := host.API.Group("/chat/realtime")
	calls.GET("/:roomId/participants", authn.RequireAuth(), s.getParticipants)
	calls.POST("/:roomId/kick/:targetAccountId", authn.RequireAuth(), requirePermission(permissionCallKick), s.kickParticipant)
	calls.POST("/:roomId/mute/:targetAccountId", authn.RequireAuth(), requirePermission(permissionCallMute), s.muteParticipant)
	calls.POST("/:roomId/unmute/:targetAccountId", authn.RequireAuth(), requirePermission(permissionCallMute), s.unmuteParticipant)
	calls.GET("/:roomId", authn.RequireAuth(), s.getOngoingCall)
	calls.GET("/:roomId/join", authn.RequireAuth(), s.joinCall)
	calls.POST("/:roomId", authn.RequireAuth(), s.startCall)
	calls.POST("/:roomId/invite/:targetAccountId", authn.RequireAuth(), requirePermission(permissionCallInvite), s.inviteToCall)
	calls.DELETE("/:roomId", authn.RequireAuth(), s.leaveCall)

	// The chat module owns the `/chat/:id` wildcard, so these routes must reuse
	// the same parameter name; gin rejects two different wildcard names at one
	// position.
	chat := host.API.Group("/chat")
	chat.POST("/:id/messages/voice", authn.RequireAuth(), requirePermission(permissionMessagesCreate), s.sendVoiceMessage)
	chat.GET("/:id/voice/:voiceId", s.getVoiceMessage)

	if host.Core.Jobs != nil {
		host.Core.Jobs.Cron("messager.voice.cleanup", s.voice.cleanupCron, s.voice.cleanupJob)
	}
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

// newVoiceService builds the voice service from the VoiceMessages section,
// applying the same fallbacks the configuration loader uses.
func newVoiceService(s *server, cfg *config.Config) *voiceService {
	maxSize := cfg.VoiceMessages.MaxFileSizeBytes
	if maxSize <= 0 {
		maxSize = 10 * 1024 * 1024
	}
	retention := cfg.VoiceMessages.RetentionDays
	if retention <= 0 {
		retention = 30
	}
	cron := strings.TrimSpace(cfg.VoiceMessages.CleanupCron)
	if cron == "" {
		cron = "0 15 * * * ?"
	}
	prefix := strings.TrimSpace(cfg.VoiceMessages.KeyPrefix)
	if prefix == "" {
		prefix = "voice"
	}
	bucket := cfg.VoiceMessages.S3
	return &voiceService{
		srv: s,
		storage: &s3Storage{settings: s3Settings{
			ServiceURL:     bucket.ServiceURL,
			Endpoint:       bucket.Endpoint,
			PublicBaseURL:  bucket.PublicBaseURL,
			Region:         bucket.Region,
			AccessKey:      bucket.AccessKey,
			SecretKey:      bucket.SecretKey,
			Bucket:         bucket.Bucket,
			ForcePathStyle: bucket.ForcePathStyle,
			EnableSSL:      bucket.EnableSSL,
		}},
		maxFileSize:   maxSize,
		keyPrefix:     prefix,
		retentionDays: retention,
		cleanupCron:   cron,
	}
}
