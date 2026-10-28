package publishers

import (
	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
)

func (i *instance) registerPublisherRoutes(api *gin.RouterGroup) {
	api.GET("/publishers/quota", authn.RequireAuth(), i.handleQuota)
	api.GET("/publishers", authn.RequireAuth(), i.handleListManagedPublishers)
	api.GET("/publishers/invites", authn.RequireAuth(), i.handleListInvites)
	api.POST("/publishers/invites/:name", authn.RequireAuth(), i.requirePermission(permInvitesManage), i.handleInviteMember)
	api.POST("/publishers/invites/:name/accept", authn.RequireAuth(), i.requirePermission(permInvitesManage), i.handleAcceptInvite)
	api.POST("/publishers/invites/:name/decline", authn.RequireAuth(), i.requirePermission(permInvitesManage), i.handleDeclineInvite)
	api.DELETE("/publishers/:name/members/:memberId", authn.RequireAuth(), i.requirePermission(permMembersManage), i.handleRemoveMember)
	api.DELETE("/publishers/:name/members/me", authn.RequireAuth(), i.handleLeavePublisher)
	api.PATCH("/publishers/:name/members/:memberId/role", authn.RequireAuth(), i.requirePermission(permMembersManage), i.handleUpdateMemberRole)
	api.POST("/publishers/individual", authn.RequireAuth(), i.requirePermission(permCreate), i.handleCreateIndividual)
	api.POST("/publishers/organization/:realmSlug", authn.RequireAuth(), i.requirePermission(permCreate), i.handleCreateOrganization)
	api.PATCH("/publishers/:name", authn.RequireAuth(), i.requirePermission(permUpdate), i.handleUpdatePublisher)
	api.DELETE("/publishers/:name", authn.RequireAuth(), i.requirePermission(permDelete), i.handleDeletePublisher)
	api.GET("/publishers/:name/members", i.handleListMembers)
	api.GET("/publishers/:name/members/me", authn.RequireAuth(), i.handleCurrentMember)
	api.GET("/publishers/:name/features", authn.RequireAuth(), i.handleListFeatures)
	api.GET("/publishers/:name/rewards", authn.RequireAuth(), i.handleExpectedReward)
	api.GET("/publishers/:name/rating/history", i.handleRatingHistory)
	api.POST("/publishers/:name/features", authn.RequireAuth(), i.requirePermission(permFeaturesManage), i.handleAddFeature)
	api.DELETE("/publishers/:name/features", authn.RequireAuth(), i.requirePermission(permFeaturesManage), i.handleRemoveFeature)
	api.POST("/publishers/rewards/settle", authn.RequireAuth(), i.requirePermission(permRewardsSettle), i.handleSettleRewards)
	api.POST("/publishers/awards/settle", authn.RequireAuth(), i.requirePermission(permRewardsSettle), i.handleSettleAwards)
	api.POST("/publishers/rewards/resettle", authn.RequireAuth(), i.requirePermission(permRewardsSettle), i.handleResettle)
	api.GET("/publishers/:name/fediverse", authn.RequireAuth(), i.handleFediverseStatus)
	api.POST("/publishers/:name/fediverse", authn.RequireAuth(), i.requirePermission(permFediverseManage), i.handleEnableFediverse)
	api.PATCH("/publishers/:name/fediverse", authn.RequireAuth(), i.requirePermission(permFediverseManage), i.handleUpdateFediverse)
	api.DELETE("/publishers/:name/fediverse", authn.RequireAuth(), i.requirePermission(permFediverseManage), i.handleDisableFediverse)
	api.GET("/publishers/:name/domains", authn.RequireAuth(), i.handleListDomains)
	api.POST("/publishers/:name/domains", authn.RequireAuth(), i.requirePermission(permDomainsManage), i.handleAddDomain)
	api.DELETE("/publishers/:name/domains/:domainId", authn.RequireAuth(), i.requirePermission(permDomainsManage), i.handleRemoveDomain)
	api.POST("/publishers/:name/domains/:domainId/recheck", authn.RequireAuth(), i.requirePermission(permDomainsManage), i.handleRecheckDomain)
	api.GET("/publishers/:name/ads", authn.RequireAuth(), i.handleAdvertisingPosts)
}
