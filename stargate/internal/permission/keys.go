// Package permission implements the Dyson Network permission system: the
// permission-node key registry, the default-group seed, and DB-backed
// evaluation. It is a Go port of DysonNetwork.Shared/Auth/PermissionKeys.cs,
// DysonNetwork.Padlock/Permission/PermissionSeedService.cs and
// DysonNetwork.Padlock/Permission/PermissionService.cs.
package permission

// Permission key constants. Values are the exact wire strings from
// DysonNetwork.Shared/Auth/PermissionKeys.cs — they travel over the wire, are
// compared against Redis `permission:` keys, and must never change.
//
// Convention: {domain}.{resource}.{action}; wildcard {domain}.* grants all
// permissions in that domain (evaluation supports '*' patterns, see
// Service.findPermissionNode).
const (
	// ── Permissions (Padlock admin) ──
	PermissionsCheck        = "permissions.check"
	PermissionsManage       = "permissions.manage"
	PermissionsGroupsCheck  = "permissions.groups.check"
	PermissionsGroupsManage = "permissions.groups.manage"
	PermissionsCacheManage  = "permissions.cache.manage"

	// ── Punishments ──
	PunishmentsView   = "punishments.view"
	PunishmentsCreate = "punishments.create"
	PunishmentsUpdate = "punishments.update"
	PunishmentsDelete = "punishments.delete"

	// ── Accounts ──
	AccountsDeletion        = "accounts.delete"
	AccountsStatusesUpdate  = "accounts.statuses.update"
	AccountsStatusesCreate  = "accounts.statuses.create"
	AccountsView            = "accounts.view"
	AccountsManage          = "accounts.manage"
	AccountsConnectionsView = "account.connections"

	// ── Tests ──
	TestsTake   = "tests.take"
	TestsManage = "tests.manage"
	TestsReview = "tests.review"

	// ── Developers ──
	DevelopersCreate = "developers.create"
	DevelopersManage = "developers.manage"

	// ── Custom Apps ──
	CustomAppsCreate        = "custom.apps.create"
	CustomAppsUpdate        = "custom.apps.update"
	CustomAppsDelete        = "custom.apps.delete"
	CustomAppsSecretsManage = "custom.apps.secrets.manage"

	// ── Bot Accounts ──
	BotAccountsCreate     = "bot.accounts.create"
	BotAccountsUpdate     = "bot.accounts.update"
	BotAccountsDelete     = "bot.accounts.delete"
	BotAccountsKeysManage = "bot.accounts.keys.manage"
	BotAccountsChatManage = "bot.accounts.chat.manage"

	// ── App Products ──
	AppProductsCreate = "app.products.create"
	AppProductsUpdate = "app.products.update"
	AppProductsDelete = "app.products.delete"

	// ── Dev Projects ──
	DevProjectsCreate = "dev.projects.create"
	DevProjectsUpdate = "dev.projects.update"
	DevProjectsDelete = "dev.projects.delete"

	// ── Mini Apps ──
	MiniAppsView          = "mini.apps.view"
	MiniAppsCreate        = "mini.apps.create"
	MiniAppsUpdate        = "mini.apps.update"
	MiniAppsDelete        = "mini.apps.delete"
	MiniAppsPackageUpload = "mini.apps.package.upload"

	// ── Quotas ──
	QuotasManage = "quotas.manage"

	// ── Files ──
	FilesUpload = "files.upload"

	// ── Chat ──
	ChatCreate         = "chat.create"
	ChatUpdate         = "chat.update"
	ChatDelete         = "chat.delete"
	ChatMessagesCreate = "chat.messages.create"
	ChatMessagesUpdate = "chat.messages.update"
	ChatMessagesDelete = "chat.messages.delete"
	ChatMessagesReact  = "chat.messages.react"
	ChatMembersManage  = "chat.members.manage"
	ChatMembersTimeout = "chat.members.timeout"
	ChatMembersKick    = "chat.members.kick"
	ChatInvitesManage  = "chat.invites.manage"
	ChatE2eeManage     = "chat.e2ee.manage"
	ChatSync           = "chat.sync"
	ChatCallStart      = "chat.call.start"
	ChatCallEnd        = "chat.call.end"
	ChatCallInvite     = "chat.call.invite"
	ChatCallKick       = "chat.call.kick"
	ChatCallMute       = "chat.call.mute"
	ChatGroupsManage   = "chat.groups.manage"
	ChatPinsManage     = "chat.pins.manage"
	ChatReadAll        = "chat.read.all"

	// ── Posts ──
	PostsView            = "posts.view"
	PostsCreateBlog      = "posts.create.blog"
	PostsCreate          = "posts.create"
	PostsUpdate          = "posts.update"
	PostsDelete          = "posts.delete"
	PostsPublish         = "posts.publish"
	PostsReact           = "posts.react"
	PostsBoost           = "posts.boost"
	PostsModerate        = "posts.moderate"
	PostsLock            = "posts.lock"
	PostsBookmark        = "posts.bookmark"
	PostsAward           = "posts.award"
	PostsSponsor         = "posts.sponsor"
	PostsPin             = "posts.pin"
	PostsBatchDelete     = "posts.batch.delete"
	PostsBatchVisibility = "posts.batch.visibility"

	// ── Post Collections ──
	PostCollectionsCreate      = "post.collections.create"
	PostCollectionsUpdate      = "post.collections.update"
	PostCollectionsDelete      = "post.collections.delete"
	PostCollectionsPostsManage = "post.collections.posts.manage"

	// ── Post Categories & Tags ──
	PostCategoriesManage    = "post.categories.manage"
	PostCategoriesSubscribe = "post.categories.subscribe"
	PostsTagsCreate         = "posts.tags.create"
	PostsTagsUpdate         = "posts.tags.update"
	PostsTagsDelete         = "posts.tags.delete"
	PostsTagsAssign         = "posts.tags.assign"
	PostsTagsProtect        = "posts.tags.protect"
	PostsTagsClaim          = "posts.tags.claim"
	PostsTagsEvent          = "posts.tags.event"

	// ── Post Subscriptions ──
	PostSubscriptionsManage = "post.subscriptions.manage"

	// ── Publishers ──
	PublishersCreate              = "publishers.create"
	PublishersUpdate              = "publishers.update"
	PublishersDelete              = "publishers.delete"
	PublishersModerate            = "publishers.moderate"
	PublishersMembersManage       = "publishers.members.manage"
	PublishersInvitesManage       = "publishers.invites.manage"
	PublishersFeaturesManage      = "publishers.features.manage"
	PublishersFediverseManage     = "publishers.fediverse.manage"
	PublishersDomainsManage       = "publishers.domains.manage"
	PublishersRewardsSettle       = "publishers.rewards.settle"
	PublishersRewardsResettle     = "publishers.rewards.resettle"
	PublishersSubscriptionsManage = "publishers.subscriptions.manage"

	// ── Timelines ──
	TimelinesFeedback      = "timelines.feedback"
	TimelinesWeightsManage = "timelines.weights.manage"
	TimelinesReset         = "timelines.reset"

	// ── Automod ──
	AutomodRulesManage = "automod.rules.manage"
	AutomodRulesTest   = "automod.rules.test"

	// ── Fediverse Moderation ──
	FediverseModerationRulesManage = "fediverse.moderation.rules.manage"
	FediverseModerationCheck       = "fediverse.moderation.check"
	FediverseKeysManage            = "fediverse.keys.manage"

	// ── Stickers ──
	StickersPacksCreate   = "stickers.packs.create"
	StickersPacksUpdate   = "stickers.packs.update"
	StickersPacksDelete   = "stickers.packs.delete"
	StickersPacksOwn      = "stickers.packs.own"
	StickersPacksOrder    = "stickers.packs.order"
	StickersCreate        = "stickers.create"
	StickersUpdate        = "stickers.update"
	StickersDelete        = "stickers.delete"
	StickersContentUpdate = "stickers.content.update"

	// ── Surveys ──
	SurveysCreate    = "surveys.create"
	SurveysUpdate    = "surveys.update"
	SurveysDelete    = "surveys.delete"
	SurveysPublish   = "surveys.publish"
	SurveysArchive   = "surveys.archive"
	SurveysClone     = "surveys.clone"
	SurveysAnswer    = "surveys.answer"
	SurveysSubscribe = "surveys.subscribe"

	// ── Live Streams ──
	LiveStreamsCreate       = "live.streams.create"
	LiveStreamsUpdate       = "live.streams.update"
	LiveStreamsDelete       = "live.streams.delete"
	LiveStreamsStart        = "live.streams.start"
	LiveStreamsEnd          = "live.streams.end"
	LiveStreamsEgress       = "live.streams.egress"
	LiveStreamsHls          = "live.streams.hls"
	LiveStreamsPin          = "live.streams.pin"
	LiveStreamsChatModerate = "live.streams.chat.moderate"
	LiveStreamsAwards       = "live.streams.awards"
	LiveStreamsThumbnail    = "live.streams.thumbnail"

	// ── Ads ──
	AdsManage          = "ads.manage"
	AdsLeaderboardView = "ads.leaderboard.view"

	// ── Translation ──
	TranslationManage = "translation.manage"

	// ── Quotes ──
	QuoteAuthorizationManage = "quotes.authorization.manage"

	// ── Wallet ──
	WalletsManage                 = "wallets.manage"
	WalletsBalanceModify          = "wallets.balance.modify"
	WalletsCreate                 = "wallets.create"
	WalletsDelete                 = "wallets.delete"
	WalletsFundsManage            = "wallets.funds.manage"
	WalletsTransactionsManage     = "wallets.transactions.manage"
	WalletsTransferRequestsManage = "wallets.transfer.requests.manage"
	WalletsPublicIdManage         = "wallets.public.id.manage"

	// ── Orders ──
	OrdersCreate        = "orders.create"
	OrdersUpdate        = "orders.update"
	OrdersPay           = "orders.pay"
	OrdersView          = "orders.view"
	OrdersPayoutsManage = "orders.payouts.manage"

	// ── Merchants ──
	MerchantsManage            = "merchants.manage"
	MerchantsSettlementsManage = "merchants.settlements.manage"

	// ── Subscriptions ──
	SubscriptionsCreate       = "subscriptions.create"
	SubscriptionsCancel       = "subscriptions.cancel"
	SubscriptionsOrderManage  = "subscriptions.order.manage"
	SubscriptionsActivate     = "subscriptions.activate"
	SubscriptionsCheckout     = "subscriptions.checkout"
	SubscriptionsGroupsManage = "subscriptions.groups.manage"
	SubscriptionGiftsPurchase = "subscription.gifts.purchase"
	SubscriptionGiftsRedeem   = "subscription.gifts.redeem"
	SubscriptionGiftsSend     = "subscription.gifts.send"
	SubscriptionGiftsCancel   = "subscription.gifts.cancel"

	// ── Notifications ──
	NotificationsSend                = "notifications.send"
	NotificationsPut                 = "notifications.put"
	NotificationsReadAll             = "notifications.read.all"
	NotificationsPreferencesManage   = "notifications.preferences.manage"
	NotificationsSubscriptionsManage = "notifications.subscriptions.manage"
	NotificationsSopSubscribe        = "notifications.sop.subscribe"

	// ── Emails ──
	EmailsSend = "emails.send"

	// ── Account Profile ──
	AccountsProfileBoard       = "accounts.profile.board"
	AccountsProfileBoardManage = "accounts.profile.board.manage"
	AccountsBoardManage        = "accounts.board.manage"

	// ── Social Credits ──
	CreditsValidatePerform = "credits.validate.perform"
	CreditsManage          = "credits.manage"

	// ── Presence ──
	PresencesScan           = "presences.scan"
	PresencesManage         = "presences.manage"
	PresencesActivityManage = "presences.activity.manage"
	PresencesArtworkManage  = "presences.artwork.manage"

	// ── Relationships ──
	RelationshipsCreate             = "relationships.create"
	RelationshipsUpdate             = "relationships.update"
	RelationshipsDelete             = "relationships.delete"
	RelationshipsFriendsManage      = "relationships.friends.manage"
	RelationshipsBlockManage        = "relationships.block.manage"
	RelationshipsMuteManage         = "relationships.mute.manage"
	RelationshipsCloseFriendsManage = "relationships.close.friends.manage"
	RelationshipsAliasManage        = "relationships.alias.manage"
	RelationshipsInspect            = "relationships.inspect"
	RelationshipsSync               = "relationships.sync"

	// ── Realms ──
	RealmsCreate            = "realms.create"
	RealmsUpdate            = "realms.update"
	RealmsDelete            = "realms.delete"
	RealmsModerate          = "realms.moderate"
	RealmsInvitesManage     = "realms.invites.manage"
	RealmsMembersManage     = "realms.members.manage"
	RealmsLabelsManage      = "realms.labels.manage"
	RealmsBoostsManage      = "realms.boosts.manage"
	RealmsPermissionsManage = "realms.permissions.manage"

	// ── Notable Days ──
	NotableDaysCreate = "notable.days.create"
	NotableDaysUpdate = "notable.days.update"
	NotableDaysDelete = "notable.days.delete"

	// ── NFC ──
	NfcTagsCreate  = "nfc.tags.create"
	NfcTagsUpdate  = "nfc.tags.update"
	NfcTagsDelete  = "nfc.tags.delete"
	NfcTagsClaim   = "nfc.tags.claim"
	NfcTagsLock    = "nfc.tags.lock"
	NfcAdminManage = "nfc.admin.manage"

	// ── Tickets ──
	TicketsCreate         = "tickets.create"
	TicketsUpdate         = "tickets.update"
	TicketsDelete         = "tickets.delete"
	TicketsMessagesCreate = "tickets.messages.create"
	TicketsStatusUpdate   = "tickets.status.update"
	TicketsAssign         = "tickets.assign"
	TicketsOnCallManage   = "tickets.oncall.manage"

	// ── Progression ──
	ProgressionAchievementsManage = "progression.achievements.manage"
	ProgressionQuestsManage       = "progression.quests.manage"
	ProgressionSync               = "progression.sync"
	ProgressionBadgesManage       = "progression.badges.manage"

	// ── Domain Trust ──
	DomainTrustCreate   = "domain.trust.create"
	DomainTrustUpdate   = "domain.trust.update"
	DomainTrustDelete   = "domain.trust.delete"
	DomainTrustValidate = "domain.trust.validate"

	// ── Meet / Location ──
	MeetCreate           = "meet.create"
	MeetUpdate           = "meet.update"
	MeetDelete           = "meet.delete"
	MeetComplete         = "meet.complete"
	MeetJoin             = "meet.join"
	MeetPinManage        = "meet.pin.manage"
	MeetVisibilityUpdate = "meet.visibility.update"

	LocationPinsCreate = "location.pins.create"
	LocationPinsUpdate = "location.pins.update"
	LocationPinsDelete = "location.pins.delete"

	// ── Calendar ──
	CalendarEventsCreate        = "calendar.events.create"
	CalendarEventsUpdate        = "calendar.events.update"
	CalendarEventsDelete        = "calendar.events.delete"
	CalendarSubscriptionsManage = "calendar.subscriptions.manage"
	CalendarCheckinManage       = "calendar.checkin.manage"

	// ── Nearby ──
	NearbyPresenceManage = "nearby.presence.manage"
	NearbyResolve        = "nearby.resolve"

	// ── Affiliations ──
	AffiliationsManage        = "affiliations.manage"
	AffiliationsResultsManage = "affiliations.results.manage"

	// ── Rewind ──
	RewindCreate = "rewind.create"

	// ── E2EE ──
	E2eeKeysManage    = "e2ee.keys.manage"
	E2eeMlsManage     = "e2ee.mls.manage"
	E2eeDevicesManage = "e2ee.devices.manage"

	// ── Auth ──
	AuthSessionsManage = "auth.sessions.manage"
	AuthFactorsManage  = "auth.factors.manage"
	AuthApiKeysManage  = "auth.api.keys.manage"
	AuthAppsAuthorize  = "auth.apps.authorize"
	AuthRecover        = "auth.recover"

	// ── Account Security ──
	AccountContactsManage       = "account.contacts.manage"
	AccountDevicesManage        = "account.devices.manage"
	AccountAuthorizedAppsManage = "account.authorized.apps.manage"

	// ── Reader / Cache ──
	CacheScrap = "cache.scrap"

	// ── Admin Dashboard ──
	AdminIpCheck = "admin.ip.check"

	// ── Workspaces (WattEngine.Valve platform-admin) ──
	AdminWorkspacesView        = "admin.workspaces.view"
	AdminWorkspacesManage      = "admin.workspaces.manage"
	AdminWorkspacesDelete      = "admin.workspaces.delete"
	AdminWorkspacesPlansManage = "admin.workspaces.plans.manage"

	// ── Boards (WattEngine.Ideask platform-admin) ──
	AdminBoardsView   = "admin.boards.view"
	AdminBoardsManage = "admin.boards.manage"
	AdminBoardsDelete = "admin.boards.delete"

	// ── Tasks (WattEngine.Ideask platform-admin) ──
	AdminTasksView               = "admin.tasks.view"
	AdminTasksManage             = "admin.tasks.manage"
	AdminTasksDelete             = "admin.tasks.delete"
	AdminTasksIntegrationsManage = "admin.tasks.integrations.manage"

	// ── Flywheel (WattEngine.Flywheel platform-admin) ──
	AdminFlywheelView        = "admin.flywheel.view"
	AdminFlywheelAppsManage  = "admin.flywheel.apps.manage"
	AdminFlywheelBlobsDelete = "admin.flywheel.blobs.delete"
	AdminFlywheelAuditView   = "admin.flywheel.audit.view"

	// ── Workspaces (WattEngine.Valve user / self-service) ──
	WorkspacesCreate        = "workspaces.create"
	WorkspacesView          = "workspaces.view"
	WorkspacesUpdate        = "workspaces.update"
	WorkspacesDelete        = "workspaces.delete"
	WorkspacesMembersManage = "workspaces.members.manage"
	WorkspacesPlansManage   = "workspaces.plans.manage"

	// ── Boards (WattEngine.Ideask user / self-service) ──
	BoardsView   = "boards.view"
	BoardsCreate = "boards.create"
	BoardsUpdate = "boards.update"
	BoardsDelete = "boards.delete"

	// ── Tasks (WattEngine.Ideask user / self-service) ──
	TasksView               = "tasks.view"
	TasksCreate             = "tasks.create"
	TasksUpdate             = "tasks.update"
	TasksDelete             = "tasks.delete"
	TasksAssignmentsManage  = "tasks.assignments.manage"
	TasksCommentsManage     = "tasks.comments.manage"
	TasksIntegrationsManage = "tasks.integrations.manage"

	// ── Flywheel (WattEngine.Flywheel user / self-service) ──
	FlywheelView        = "flywheel.view"
	FlywheelAppsManage  = "flywheel.apps.manage"
	FlywheelBlobsManage = "flywheel.blobs.manage"
	FlywheelBlobsDelete = "flywheel.blobs.delete"
)

// AllKeys is the complete permission registry in source order. It backs
// /.well-known/permissions and can be used to detect newly added keys.
var AllKeys = []string{
	PermissionsCheck,
	PermissionsManage,
	PermissionsGroupsCheck,
	PermissionsGroupsManage,
	PermissionsCacheManage,

	PunishmentsView,
	PunishmentsCreate,
	PunishmentsUpdate,
	PunishmentsDelete,

	AccountsDeletion,
	AccountsStatusesUpdate,
	AccountsStatusesCreate,
	AccountsView,
	AccountsManage,
	AccountsConnectionsView,

	TestsTake,
	TestsManage,
	TestsReview,

	DevelopersCreate,
	DevelopersManage,

	CustomAppsCreate,
	CustomAppsUpdate,
	CustomAppsDelete,
	CustomAppsSecretsManage,

	BotAccountsCreate,
	BotAccountsUpdate,
	BotAccountsDelete,
	BotAccountsKeysManage,
	BotAccountsChatManage,

	AppProductsCreate,
	AppProductsUpdate,
	AppProductsDelete,

	DevProjectsCreate,
	DevProjectsUpdate,
	DevProjectsDelete,

	MiniAppsView,
	MiniAppsCreate,
	MiniAppsUpdate,
	MiniAppsDelete,
	MiniAppsPackageUpload,

	QuotasManage,

	FilesUpload,

	ChatCreate,
	ChatUpdate,
	ChatDelete,
	ChatMessagesCreate,
	ChatMessagesUpdate,
	ChatMessagesDelete,
	ChatMessagesReact,
	ChatMembersManage,
	ChatMembersTimeout,
	ChatMembersKick,
	ChatInvitesManage,
	ChatE2eeManage,
	ChatSync,
	ChatCallStart,
	ChatCallEnd,
	ChatCallInvite,
	ChatCallKick,
	ChatCallMute,
	ChatGroupsManage,
	ChatPinsManage,
	ChatReadAll,

	PostsView,
	PostsCreateBlog,
	PostsCreate,
	PostsUpdate,
	PostsDelete,
	PostsPublish,
	PostsReact,
	PostsBoost,
	PostsModerate,
	PostsLock,
	PostsBookmark,
	PostsAward,
	PostsSponsor,
	PostsPin,
	PostsBatchDelete,
	PostsBatchVisibility,

	PostCollectionsCreate,
	PostCollectionsUpdate,
	PostCollectionsDelete,
	PostCollectionsPostsManage,

	PostCategoriesManage,
	PostCategoriesSubscribe,
	PostsTagsCreate,
	PostsTagsUpdate,
	PostsTagsDelete,
	PostsTagsAssign,
	PostsTagsProtect,
	PostsTagsClaim,
	PostsTagsEvent,

	PostSubscriptionsManage,

	PublishersCreate,
	PublishersUpdate,
	PublishersDelete,
	PublishersModerate,
	PublishersMembersManage,
	PublishersInvitesManage,
	PublishersFeaturesManage,
	PublishersFediverseManage,
	PublishersDomainsManage,
	PublishersRewardsSettle,
	PublishersRewardsResettle,
	PublishersSubscriptionsManage,

	TimelinesFeedback,
	TimelinesWeightsManage,
	TimelinesReset,

	AutomodRulesManage,
	AutomodRulesTest,

	FediverseModerationRulesManage,
	FediverseModerationCheck,
	FediverseKeysManage,

	StickersPacksCreate,
	StickersPacksUpdate,
	StickersPacksDelete,
	StickersPacksOwn,
	StickersPacksOrder,
	StickersCreate,
	StickersUpdate,
	StickersDelete,
	StickersContentUpdate,

	SurveysCreate,
	SurveysUpdate,
	SurveysDelete,
	SurveysPublish,
	SurveysArchive,
	SurveysClone,
	SurveysAnswer,
	SurveysSubscribe,

	LiveStreamsCreate,
	LiveStreamsUpdate,
	LiveStreamsDelete,
	LiveStreamsStart,
	LiveStreamsEnd,
	LiveStreamsEgress,
	LiveStreamsHls,
	LiveStreamsPin,
	LiveStreamsChatModerate,
	LiveStreamsAwards,
	LiveStreamsThumbnail,

	AdsManage,
	AdsLeaderboardView,

	TranslationManage,

	QuoteAuthorizationManage,

	WalletsManage,
	WalletsBalanceModify,
	WalletsCreate,
	WalletsDelete,
	WalletsFundsManage,
	WalletsTransactionsManage,
	WalletsTransferRequestsManage,
	WalletsPublicIdManage,

	OrdersCreate,
	OrdersUpdate,
	OrdersPay,
	OrdersView,
	OrdersPayoutsManage,

	MerchantsManage,
	MerchantsSettlementsManage,

	SubscriptionsCreate,
	SubscriptionsCancel,
	SubscriptionsOrderManage,
	SubscriptionsActivate,
	SubscriptionsCheckout,
	SubscriptionsGroupsManage,
	SubscriptionGiftsPurchase,
	SubscriptionGiftsRedeem,
	SubscriptionGiftsSend,
	SubscriptionGiftsCancel,

	NotificationsSend,
	NotificationsPut,
	NotificationsReadAll,
	NotificationsPreferencesManage,
	NotificationsSubscriptionsManage,
	NotificationsSopSubscribe,

	EmailsSend,

	AccountsProfileBoard,
	AccountsProfileBoardManage,
	AccountsBoardManage,

	CreditsValidatePerform,
	CreditsManage,

	PresencesScan,
	PresencesManage,
	PresencesActivityManage,
	PresencesArtworkManage,

	RelationshipsCreate,
	RelationshipsUpdate,
	RelationshipsDelete,
	RelationshipsFriendsManage,
	RelationshipsBlockManage,
	RelationshipsMuteManage,
	RelationshipsCloseFriendsManage,
	RelationshipsAliasManage,
	RelationshipsInspect,
	RelationshipsSync,

	RealmsCreate,
	RealmsUpdate,
	RealmsDelete,
	RealmsModerate,
	RealmsInvitesManage,
	RealmsMembersManage,
	RealmsLabelsManage,
	RealmsBoostsManage,
	RealmsPermissionsManage,

	NotableDaysCreate,
	NotableDaysUpdate,
	NotableDaysDelete,

	NfcTagsCreate,
	NfcTagsUpdate,
	NfcTagsDelete,
	NfcTagsClaim,
	NfcTagsLock,
	NfcAdminManage,

	TicketsCreate,
	TicketsUpdate,
	TicketsDelete,
	TicketsMessagesCreate,
	TicketsStatusUpdate,
	TicketsAssign,
	TicketsOnCallManage,

	ProgressionAchievementsManage,
	ProgressionQuestsManage,
	ProgressionSync,
	ProgressionBadgesManage,

	DomainTrustCreate,
	DomainTrustUpdate,
	DomainTrustDelete,
	DomainTrustValidate,

	MeetCreate,
	MeetUpdate,
	MeetDelete,
	MeetComplete,
	MeetJoin,
	MeetPinManage,
	MeetVisibilityUpdate,

	LocationPinsCreate,
	LocationPinsUpdate,
	LocationPinsDelete,

	CalendarEventsCreate,
	CalendarEventsUpdate,
	CalendarEventsDelete,
	CalendarSubscriptionsManage,
	CalendarCheckinManage,

	NearbyPresenceManage,
	NearbyResolve,

	AffiliationsManage,
	AffiliationsResultsManage,

	RewindCreate,

	E2eeKeysManage,
	E2eeMlsManage,
	E2eeDevicesManage,

	AuthSessionsManage,
	AuthFactorsManage,
	AuthApiKeysManage,
	AuthAppsAuthorize,
	AuthRecover,

	AccountContactsManage,
	AccountDevicesManage,
	AccountAuthorizedAppsManage,

	CacheScrap,

	AdminIpCheck,

	AdminWorkspacesView,
	AdminWorkspacesManage,
	AdminWorkspacesDelete,
	AdminWorkspacesPlansManage,

	AdminBoardsView,
	AdminBoardsManage,
	AdminBoardsDelete,

	AdminTasksView,
	AdminTasksManage,
	AdminTasksDelete,
	AdminTasksIntegrationsManage,

	AdminFlywheelView,
	AdminFlywheelAppsManage,
	AdminFlywheelBlobsDelete,
	AdminFlywheelAuditView,

	WorkspacesCreate,
	WorkspacesView,
	WorkspacesUpdate,
	WorkspacesDelete,
	WorkspacesMembersManage,
	WorkspacesPlansManage,

	BoardsView,
	BoardsCreate,
	BoardsUpdate,
	BoardsDelete,

	TasksView,
	TasksCreate,
	TasksUpdate,
	TasksDelete,
	TasksAssignmentsManage,
	TasksCommentsManage,
	TasksIntegrationsManage,

	FlywheelView,
	FlywheelAppsManage,
	FlywheelBlobsManage,
	FlywheelBlobsDelete,
}
