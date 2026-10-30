package wallet

// Subscription administration HTTP surface. Ported from
// DysonNetwork.Wallet's Payment/SubscriptionAdminController.cs (mounted at
// /api/admin/subscriptions).

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/permission"
)

// upsertSubscriptionDefinitionRequest mirrors UpsertSubscriptionDefinitionRequest.
type upsertSubscriptionDefinitionRequest struct {
	Identifier           string                     `json:"identifier"`
	GroupIdentifier      *string                    `json:"group_identifier"`
	DisplayName          string                     `json:"display_name"`
	Currency             string                     `json:"currency"`
	BasePrice            decimal.Decimal            `json:"base_price"`
	PerkLevel            int                        `json:"perk_level"`
	MinimumAccountLevel  *int                       `json:"minimum_account_level"`
	ExperienceMultiplier *decimal.Decimal           `json:"experience_multiplier"`
	GoldenPointReward    *int                       `json:"golden_point_reward"`
	DisplayConfig        *SubscriptionDisplayConfig `json:"display_config"`
	PaymentPolicy        *SubscriptionPaymentPolicy `json:"payment_policy"`
	GiftPolicy           *SubscriptionGiftPolicy    `json:"gift_policy"`
	ProviderMappings     map[string][]string        `json:"provider_mappings"`
	AppIdentifier        *string                    `json:"app_identifier"`
}

// maintenanceJobRequest mirrors MaintenanceJobRequest.
type maintenanceJobRequest struct {
	BatchSize int `json:"batch_size"`
}

// MaintenanceJobResponse mirrors MaintenanceJobResponse.
type MaintenanceJobResponse struct {
	AffectedCount int `json:"affected_count"`
}

// registerSubscriptionAdminHTTP mounts SubscriptionAdminController's routes.
func registerSubscriptionAdminHTTP(api *gin.RouterGroup, d *Deps) {
	if api == nil || d == nil || d.Service == nil {
		return
	}
	subs := NewSubscriptions(d.Service)
	subs.Configure(d.Settings, d.Log)

	group := api.Group("/admin/subscriptions", middleware.RequireAuth())
	group.GET("",
		requireWalletPermission(d, permission.SubscriptionsOrderManage),
		subs.adminListSubscriptionsHTTP)
	group.GET("/catalog",
		requireWalletPermission(d, permission.SubscriptionsGroupsManage),
		subs.adminListCatalogHTTP)
	group.GET("/catalog/:identifier",
		requireWalletPermission(d, permission.SubscriptionsGroupsManage),
		subs.adminGetCatalogItemHTTP)
	group.POST("/catalog",
		requireWalletPermission(d, permission.SubscriptionsGroupsManage),
		subs.adminUpsertCatalogItemHTTP)
	group.DELETE("/catalog/:identifier",
		requireWalletPermission(d, permission.SubscriptionsGroupsManage),
		subs.adminDeleteCatalogItemHTTP)
	group.POST("/maintenance/update-expired",
		requireWalletPermission(d, permission.SubscriptionsOrderManage),
		subs.adminUpdateExpiredHTTP)
	group.POST("/maintenance/activate-pending",
		requireWalletPermission(d, permission.SubscriptionsOrderManage),
		subs.adminActivatePendingHTTP)
	group.POST("/maintenance/cancel-unavailable-in-app-wallet",
		requireWalletPermission(d, permission.SubscriptionsOrderManage),
		subs.adminCancelUnavailableHTTP)
}

// GET /api/admin/subscriptions
func (s *Subscriptions) adminListSubscriptionsHTTP(c *gin.Context) {
	take := queryInt(c, "take", 50)
	if take < 1 {
		take = 1
	}
	if take > 200 {
		take = 200
	}
	offset := queryInt(c, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	query := s.db(c.Request.Context()).Model(&WalletSubscriptionEntity{})
	if raw := strings.TrimSpace(c.Query("account_id")); raw != "" {
		accountID, err := uuid.Parse(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
				"account_id": {"The account_id value is invalid."},
			}))
			return
		}
		query = query.Where("account_id = ?", accountID)
	}
	if raw := strings.TrimSpace(c.Query("identifier")); raw != "" {
		query = query.Where("identifier = ?", raw)
	}
	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		status, err := strconv.Atoi(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
				"status": {"The status value is invalid."},
			}))
			return
		}
		query = query.Where("status = ?", status)
	}
	if raw := strings.TrimSpace(c.Query("is_active")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
				"is_active": {"The is_active value is invalid."},
			}))
			return
		}
		query = query.Where("is_active = ?", value)
	}
	if raw := strings.TrimSpace(c.Query("is_testing")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
				"is_testing": {"The is_testing value is invalid."},
			}))
			return
		}
		query = query.Where("is_testing = ?", value)
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load subscriptions.", http.StatusInternalServerError))
		return
	}
	var subscriptions []WalletSubscriptionEntity
	if err := query.Session(&gorm.Session{}).Order("begun_at DESC").Offset(offset).Limit(take).Find(&subscriptions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load subscriptions.", http.StatusInternalServerError))
		return
	}
	setTotal(c, total)
	wires, err := s.subscriptionWires(c.Request.Context(), subscriptions)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load subscriptions.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, wires)
}

// GET /api/admin/subscriptions/catalog
func (s *Subscriptions) adminListCatalogHTTP(c *gin.Context) {
	definitions, err := s.ListDefinitions(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription catalog.", http.StatusInternalServerError))
		return
	}
	items := make([]SubscriptionDefinition, 0, len(definitions))
	for i := range definitions {
		items = append(items, definitionModel(&definitions[i]))
	}
	c.JSON(http.StatusOK, items)
}

// GET /api/admin/subscriptions/catalog/:identifier
func (s *Subscriptions) adminGetCatalogItemHTTP(c *gin.Context) {
	definition, err := s.GetDefinition(c.Request.Context(), c.Param("identifier"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to load the subscription definition.", http.StatusInternalServerError))
		return
	}
	if definition == nil {
		c.JSON(http.StatusNotFound, errs.New("WALLET_SUBSCRIPTION_DEFINITION_NOT_FOUND",
			"Subscription definition was not found.", http.StatusNotFound))
		return
	}
	c.JSON(http.StatusOK, definitionModel(definition))
}

// POST /api/admin/subscriptions/catalog
func (s *Subscriptions) adminUpsertCatalogItemHTTP(c *gin.Context) {
	var request upsertSubscriptionDefinitionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"identifier": {"The request body is invalid."},
		}))
		return
	}
	errorsByField := map[string][]string{}
	if strings.TrimSpace(request.Identifier) == "" {
		errorsByField["identifier"] = []string{"The Identifier field is required."}
	}
	if strings.TrimSpace(request.DisplayName) == "" {
		errorsByField["display_name"] = []string{"The DisplayName field is required."}
	}
	if strings.TrimSpace(request.Currency) == "" {
		errorsByField["currency"] = []string{"The Currency field is required."}
	}
	if len(errorsByField) > 0 {
		c.JSON(http.StatusBadRequest, errs.Validation(errorsByField))
		return
	}
	policy := defaultPaymentPolicy()
	if request.PaymentPolicy != nil {
		policy = *request.PaymentPolicy
	}
	if policy.AllowedMethods == nil {
		policy.AllowedMethods = []string{}
	}
	input := SubscriptionDefinitionInput{
		Identifier:           request.Identifier,
		GroupIdentifier:      request.GroupIdentifier,
		DisplayName:          request.DisplayName,
		Currency:             request.Currency,
		BasePrice:            request.BasePrice,
		PerkLevel:            request.PerkLevel,
		MinimumAccountLevel:  request.MinimumAccountLevel,
		ExperienceMultiplier: request.ExperienceMultiplier,
		GoldenPointReward:    request.GoldenPointReward,
		DisplayConfig:        request.DisplayConfig,
		PaymentPolicy:        policy,
		GiftPolicy:           request.GiftPolicy,
		ProviderMappings:     request.ProviderMappings,
		AppIdentifier:        request.AppIdentifier,
	}
	definition, created, err := s.UpsertDefinition(c.Request.Context(), input)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to save the subscription definition.", http.StatusInternalServerError))
		return
	}
	if created {
		c.Header("Location", "/api/admin/subscriptions/catalog/"+definition.Identifier)
		c.JSON(http.StatusCreated, definitionModel(definition))
		return
	}
	c.JSON(http.StatusOK, definitionModel(definition))
}

// DELETE /api/admin/subscriptions/catalog/:identifier
func (s *Subscriptions) adminDeleteCatalogItemHTTP(c *gin.Context) {
	deleted, err := s.DeleteDefinition(c.Request.Context(), c.Param("identifier"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Failed to delete the subscription definition.", http.StatusInternalServerError))
		return
	}
	if !deleted {
		c.JSON(http.StatusNotFound, errs.New("WALLET_SUBSCRIPTION_DEFINITION_NOT_FOUND",
			"Subscription definition was not found.", http.StatusNotFound))
		return
	}
	c.Status(http.StatusNoContent)
}

func maintenanceBatchSize(c *gin.Context) int {
	var request maintenanceJobRequest
	_ = c.ShouldBindJSON(&request)
	size := request.BatchSize
	if size <= 0 {
		size = 100
	}
	if size < 1 {
		size = 1
	}
	if size > 1000 {
		size = 1000
	}
	return size
}

// POST /api/admin/subscriptions/maintenance/update-expired
func (s *Subscriptions) adminUpdateExpiredHTTP(c *gin.Context) {
	affected, err := s.UpdateExpiredSubscriptions(c.Request.Context(), maintenanceBatchSize(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Subscription maintenance failed.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, MaintenanceJobResponse{AffectedCount: affected})
}

// POST /api/admin/subscriptions/maintenance/activate-pending
func (s *Subscriptions) adminActivatePendingHTTP(c *gin.Context) {
	affected, err := s.ActivatePendingSubscriptions(c.Request.Context(), maintenanceBatchSize(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Subscription maintenance failed.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, MaintenanceJobResponse{AffectedCount: affected})
}

// POST /api/admin/subscriptions/maintenance/cancel-unavailable-in-app-wallet
func (s *Subscriptions) adminCancelUnavailableHTTP(c *gin.Context) {
	affected, err := s.CancelUnavailableInAppWalletSubscriptions(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Subscription maintenance failed.", http.StatusInternalServerError))
		return
	}
	c.JSON(http.StatusOK, MaintenanceJobResponse{AffectedCount: affected})
}
