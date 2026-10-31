package wallet

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc"

	"src.solsynth.dev/sosys/stargate/model"
)

func init() {
	// SnWallet amounts are C# decimals, which System.Text.Json wrote as bare
	// numbers. shopspring quotes decimal.Decimal by default, and clients parse
	// amount/price as num, so the quoted form fails their type check. The
	// switch is process-wide; every struct this package marshals to HTTP
	// carries the fleet shape, and the custom Decimal types in realm/purchases
	// marshal themselves regardless.
	decimal.MarshalJSONWithoutQuotes = true
}

// RegisterHTTP mounts every wallet HTTP surface on the /api router group.
func RegisterHTTP(api *gin.RouterGroup, d *Deps) {
	registerWalletHTTP(api, d)
	registerOrderHTTP(api, d)
	registerSubscriptionHTTP(api, d)
	registerSubscriptionAdminHTTP(api, d)
	registerGiftHTTP(api, d)
	registerMerchantHTTP(api, d)
	registerProductHTTP(api, d)
	registerPaymentAdminHTTP(api, d)
}

// RegisterGRPC mounts the four wallet gRPC services.
func RegisterGRPC(server *grpc.Server, d *Deps) { registerGRPC(server, d) }

// StartJobs starts the wallet interval jobs and returns their stop function.
func StartJobs(ctx context.Context, d *Deps) func() { return d.startJobs(ctx) }

// ProductsService builds the wallet product service with the module settings
// applied, so the module and the HTTP surface share one definition.
func ProductsService(d *Deps) *Products { return newProductsFromDeps(d) }

// PerkReference projects a subscription for the perks port and the token
// authenticator: SnSubscriptionReferenceObject, with sensitive fields dropped.
func PerkReference(subscription *WalletSubscriptionEntity) *model.SnSubscriptionReferenceObject {
	if subscription == nil {
		return nil
	}
	now := time.Now().UTC()
	return &model.SnSubscriptionReferenceObject{
		Id:          subscription.ID.String(),
		Identifier:  subscription.Identifier,
		DisplayName: subscription.DisplayName,
		PerkLevel:   subscription.PerkLevel,
		BegunAt:     timeValue(subscription.BegunAt),
		EndedAt:     timePointer(subscription.EndedAt),
		IsActive:    subscription.IsActive,
		IsAvailable: subscriptionAvailable(subscription, now),
		IsFreeTrial: subscription.IsFreeTrial,
		Status:      subscription.Status,
		BasePrice:   subscription.BasePrice.String(),
		FinalPrice:  subscription.BasePrice.String(),
		RenewalAt:   timePointer(subscription.RenewalAt),
		AccountId:   subscription.AccountID.String(),
		CreatedAt:   timeValue(subscription.CreatedAt),
		UpdatedAt:   timeValue(subscription.UpdatedAt),
	}
}
