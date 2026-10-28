// Package publishers ports DysonNetwork.Sphere's publisher domain: publisher
// CRUD, memberships, subscriptions, ratings, verified domains, fediverse
// actors, creator rewards and the per-account publishing settings.
package publishers

import (
	"context"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
	gen "src.solsynth.dev/sosys/go/proto"
)

// ModuleID is the publisher module's registry ID.
const ModuleID = "app.solian.sphere.publishers"

// Permission keys, copied verbatim from DysonNetwork.Shared.Auth.PermissionKeys.
const (
	permCreate            = "publishers.create"
	permUpdate            = "publishers.update"
	permDelete            = "publishers.delete"
	permModerate          = "publishers.moderate"
	permMembersManage     = "publishers.members.manage"
	permInvitesManage     = "publishers.invites.manage"
	permFeaturesManage    = "publishers.features.manage"
	permFediverseManage   = "publishers.fediverse.manage"
	permDomainsManage     = "publishers.domains.manage"
	permRewardsSettle     = "publishers.rewards.settle"
	permSubscriptions     = "publishers.subscriptions.manage"
)

// PublisherFeatureFlag constants.
const (
	flagDevelop                = "develop"
	flagFollowRequiresApproval = "followRequiresApproval"
	flagPostsRequireFollow     = "postsRequireFollow"
	minPerkLevel               = 2
)

// Member roles.
const (
	roleViewer  = 25
	roleEditor  = 50
	roleManager = 75
	roleOwner   = 100
)

// Publisher types.
const (
	publisherIndividual     = 0
	publisherOrganizational = 1
	publisherFediverse      = 2
)

// Subscription states and end reasons.
const (
	subscriptionPending  = 0
	subscriptionAccepted = 1
	subscriptionRejected = 2

	endReasonUserLeft           = 0
	endReasonRemovedByPublisher = 1
)

// Domain verification statuses.
const (
	domainPending  = 0
	domainVerified = 1
	domainFailed   = 2
	domainRevoked  = 3
)

// baseRating is the starting rating every publisher has.
const baseRating = 100.0

// Factory returns the publishers module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Models: []any{
			store.PublisherEntity{},
			store.PublisherMemberEntity{},
			store.PublisherSubscriptionEntity{},
			store.PublisherFeatureEntity{},
			store.PublisherRatingRecordEntity{},
			store.PublisherVerifiedDomainEntity{},
			store.PublishingSettingsEntity{},
			store.FediverseInstanceEntity{},
			store.FediverseKeyEntity{},
		},
		SQL: []module.SchemaHook{indexHook},
		Capabilities: []string{
			"publishers.create",
			"publishers.members",
			"publishers.features",
			"publishers.rewards",
			"publishers.fediverse",
			"publishers.domains",
			"publishers.subscriptions",
			"publishers.ads",
			"publishers.rating",
			"accounts.publishing-settings",
		},
		New: func() module.Instance[core.Services] { return &instance{} },
	}
}

// indexHook adds the constraints AutoMigrate cannot express on the legacy
// tables and backfills the membership timestamps the C# ModelBase carried.
func indexHook(ctx context.Context, db *gorm.DB) error {
	statements := []string{
		`ALTER TABLE publisher_members ADD COLUMN IF NOT EXISTS created_at timestamp with time zone NOT NULL DEFAULT now()`,
		`ALTER TABLE publisher_members ADD COLUMN IF NOT EXISTS updated_at timestamp with time zone NOT NULL DEFAULT now()`,
		`ALTER TABLE publisher_members ADD COLUMN IF NOT EXISTS deleted_at timestamp with time zone`,
		`CREATE INDEX IF NOT EXISTS ix_publisher_members_deleted_at ON publisher_members (deleted_at)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ix_publishers_name_deleted_at ON publishers (name, deleted_at)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ix_publisher_verified_domains_publisher_id_domain ON publisher_verified_domains (publisher_id, domain)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ix_publishing_settings_account_id_deleted_at ON publishing_settings (account_id, deleted_at)`,
	}
	for _, statement := range statements {
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

type instance struct {
	core     *core.Services
	store    *store.Store
	log      *slog.Logger
	accounts ports.Accounts
	files    ports.Files
	notify   ports.Notify
	payments ports.Payments
	domains  ports.Domains
}

func (i *instance) Configure(ctx context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core
	i.store = store.New(host.Core.DB)
	i.log = host.Log
	if i.log == nil {
		i.log = slog.Default()
	}
	i.accounts, _ = module.Get[ports.Accounts](host.Ports, ports.PortAccounts)
	i.files, _ = module.Get[ports.Files](host.Ports, ports.PortFiles)
	i.notify, _ = module.Get[ports.Notify](host.Ports, ports.PortNotify)
	i.payments, _ = module.Get[ports.Payments](host.Ports, ports.PortPayments)
	i.domains, _ = module.Get[ports.Domains](host.Ports, ports.PortDomains)

	i.registerPublisherRoutes(host.API)
	i.registerPublicRoutes(host.API)
	i.registerSubscriptionRoutes(host.API)
	i.registerAdminRoutes(host.API)
	i.registerSettingsRoutes(host.API)

	gen.RegisterDyPublisherServiceServer(host.GRPC, &publisherServer{core: host.Core, store: i.store})
	gen.RegisterDyPublisherRatingServiceServer(host.GRPC, &ratingServer{core: host.Core, store: i.store})

	if host.Core.Jobs != nil {
		host.Core.Jobs.Cron("publisher-settlement", "0 0 0 * * ?", func(ctx context.Context) error {
			if err := i.settlePublisherRewards(ctx); err != nil {
				return err
			}
			_, err := i.settlePostAwards(ctx)
			return err
		})
		host.Core.Jobs.Cron("publisher-follow-request-cleanup", "0 0 6 * * ?", func(ctx context.Context) error {
			return i.cleanupExpiredFollowRequests(ctx)
		})
	}
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func (i *instance) maxTake() int {
	if i.core != nil && i.core.Config != nil && i.core.Config.Security.MaxTake > 0 {
		return i.core.Config.Security.MaxTake
	}
	return 100
}

func (i *instance) requirePermission(key string) gin.HandlerFunc {
	if i.core != nil && i.core.Auth != nil {
		return i.core.Auth.RequirePermission(key)
	}
	return func(c *gin.Context) {
		c.AbortWithStatusJSON(503, gin.H{"code": "SERVICE_UNAVAILABLE", "message": "The permission service is unavailable.", "status": 503})
	}
}

// trimLower normalizes a route name the way the C# controllers do.
func trimLower(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
