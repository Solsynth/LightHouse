package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"gorm.io/gorm"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/stargate/store"
)

// registerGRPC mounts the five wallet gRPC services the C# fleet exposes
// (WalletServiceGrpc, PaymentServiceGrpc, SubscriptionServiceGrpc,
// MerchantServiceGrpc and SubscriptionCatalogServiceGrpc), matching the C#
// ApplicationConfiguration's MapGrpcService list. DyPaymentService keeps its
// own RegisterAppSubscriptionDefinition, exactly as in the C# PaymentServiceGrpc;
// the catalog service is the JSON-carrier variant for app-owned definitions.
func registerGRPC(server *grpc.Server, d *Deps) {
	if server == nil || d == nil || d.Service == nil {
		return
	}
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	subs := NewSubscriptions(d.Service)
	subs.Configure(d.Settings, log)
	gen.RegisterDyWalletServiceServer(server, &dyWalletGRPC{d: d})
	gen.RegisterDyPaymentServiceServer(server, &dyPaymentGRPC{d: d, subs: subs})
	gen.RegisterDySubscriptionServiceServer(server, &dySubscriptionGRPC{d: d, subs: subs})
	gen.RegisterDyMerchantServiceServer(server, &dyMerchantGRPC{d: d})
	gen.RegisterDySubscriptionCatalogServiceServer(server, &dySubscriptionCatalogGRPC{subs: subs})
}

// ── errors and shared helpers ──────────────────────────────────────────────

// grpcError maps the package's domain errors onto gRPC status codes. The C#
// services let InvalidOperationException surface as Unknown; FailedPrecondition
// carries the same information with a code clients can branch on.
func grpcError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrForbidden):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, ErrInsufficientFunds),
		errors.Is(err, ErrInvalidState),
		errors.Is(err, ErrWalletQuotaExceeded):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func grpcTS(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t.UTC()) }

func grpcTSPtr(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(t.UTC())
}

// grpcStringValue converts a nullable string to a proto wrapper field.
func grpcStringValue(s *string) *wrapperspb.StringValue {
	if s == nil {
		return nil
	}
	return wrapperspb.String(*s)
}

func grpcUUIDValue(id *uuid.UUID) *wrapperspb.StringValue {
	if id == nil {
		return nil
	}
	return wrapperspb.String(id.String())
}

// grpcOptionalUUID parses a nullable proto string field as a UUID.
func grpcOptionalUUID(v *wrapperspb.StringValue) (*uuid.UUID, error) {
	if v == nil || strings.TrimSpace(v.Value) == "" {
		return nil, nil
	}
	id, err := uuid.Parse(v.Value)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid uuid %q", v.Value)
	}
	return &id, nil
}

// grpcOptionalString converts a proto wrapper to a nullable string.
func grpcOptionalString(v *wrapperspb.StringValue) *string {
	if v == nil {
		return nil
	}
	return &v.Value
}

func grpcOptionalStringPtr(v *string) *string {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil
	}
	return v
}

func grpcParseUUID(value, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, status.Errorf(codes.InvalidArgument, "%s must be a uuid", field)
	}
	return id, nil
}

func grpcParseDecimal(value, field string) (decimal.Decimal, error) {
	amount, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil {
		return decimal.Zero, status.Errorf(codes.InvalidArgument, "%s must be a decimal", field)
	}
	return amount, nil
}

// walletAccountStore borrows the shared account tables for the two gRPC paths
// that need account data (subscription purchase and transfer remarks).
func walletAccountStore(d *Deps) *store.Store {
	if d == nil || d.Service == nil {
		return nil
	}
	return store.New(d.Service.DB)
}

// ── entity → proto conversions ─────────────────────────────────────────────

func grpcWallet(e *WalletEntity, pockets []WalletPocketEntity) *gen.DyWallet {
	if e == nil {
		return nil
	}
	out := &gen.DyWallet{
		Id:        e.ID.String(),
		AccountId: "",
		RealmId:   "",
		Name:      e.Name,
		IsPrimary: e.IsPrimary,
		IsDefault: e.IsPrimary,
		PublicId:  "",
	}
	if e.AccountID != nil {
		out.AccountId = e.AccountID.String()
	}
	if e.RealmID != nil {
		out.RealmId = e.RealmID.String()
	}
	if e.PublicID != nil {
		out.PublicId = *e.PublicID
	}
	for i := range pockets {
		out.Pockets = append(out.Pockets, grpcPocket(&pockets[i]))
	}
	return out
}

func grpcPocket(e *WalletPocketEntity) *gen.DyWalletPocket {
	if e == nil {
		return nil
	}
	return &gen.DyWalletPocket{
		Id:       e.ID.String(),
		Currency: e.Currency,
		Amount:   e.Amount.String(),
		WalletId: e.WalletID.String(),
	}
}

func grpcOrderStatus(value int) gen.DyOrderStatus {
	switch value {
	case OrderUnpaid:
		return gen.DyOrderStatus_DY_ORDER_STATUS_UNPAID
	case OrderPaid:
		return gen.DyOrderStatus_DY_ORDER_STATUS_PAID
	case OrderCancelled:
		return gen.DyOrderStatus_DY_ORDER_STATUS_CANCELLED
	case OrderFinished:
		return gen.DyOrderStatus_DY_ORDER_STATUS_FINISHED
	case OrderExpired:
		return gen.DyOrderStatus_DY_ORDER_STATUS_EXPIRED
	default:
		return gen.DyOrderStatus_DY_ORDER_STATUS_UNSPECIFIED
	}
}

func grpcTransactionType(value int) gen.DyTransactionType {
	switch value {
	case TransactionSystem:
		return gen.DyTransactionType_DY_TRANSACTION_TYPE_SYSTEM
	case TransactionTransfer:
		return gen.DyTransactionType_DY_TRANSACTION_TYPE_TRANSFER
	case TransactionOrder:
		return gen.DyTransactionType_DY_TRANSACTION_TYPE_ORDER
	default:
		return gen.DyTransactionType_DY_TRANSACTION_TYPE_UNSPECIFIED
	}
}

func transactionTypeFromProto(value gen.DyTransactionType) int {
	switch value {
	case gen.DyTransactionType_DY_TRANSACTION_TYPE_TRANSFER:
		return TransactionTransfer
	case gen.DyTransactionType_DY_TRANSACTION_TYPE_ORDER:
		return TransactionOrder
	default:
		return TransactionSystem
	}
}

func grpcOrderItem(e *WalletOrderItemEntity) *gen.DyOrderItem {
	if e == nil {
		return nil
	}
	return &gen.DyOrderItem{
		ProductIdentifier: e.ProductIdentifier,
		Quantity:          int32(e.Quantity),
		UnitPrice:         e.UnitPrice.String(),
		Currency:          e.Currency,
	}
}

func grpcTransaction(e *WalletTransactionEntity) *gen.DyTransaction {
	if e == nil {
		return nil
	}
	return &gen.DyTransaction{
		Id:            e.ID.String(),
		PayerWalletId: grpcUUIDValue(e.PayerWalletID),
		PayeeWalletId: grpcUUIDValue(e.PayeeWalletID),
		Currency:      e.Currency,
		Amount:        e.Amount.String(),
		Remarks:       grpcStringValue(e.Remarks),
		Type:          grpcTransactionType(e.Type),
	}
}

// grpcOrder renders an order with its lines and payment transaction, which the
// C# ToProtoValue included whenever those navigations were loaded.
func grpcOrder(ctx context.Context, svc *Service, e *WalletOrderEntity) *gen.DyOrder {
	if e == nil {
		return nil
	}
	out := &gen.DyOrder{
		Id:                e.ID.String(),
		PayeeWalletId:     grpcUUIDValue(e.PayeeWalletID),
		Currency:          wrapperspb.String(e.Currency),
		Amount:            e.Amount.String(),
		ExpiredAt:         grpcTS(e.ExpiredAt),
		AppIdentifier:     grpcStringValue(e.AppIdentifier),
		ProductIdentifier: grpcStringValue(e.ProductIdentifier),
		Status:            grpcOrderStatus(e.Status),
		TransactionId:     grpcUUIDValue(e.TransactionID),
		Remarks:           grpcStringValue(e.Remarks),
	}
	if len(e.Meta) > 0 {
		out.Meta = []byte(e.Meta)
	}
	if svc != nil {
		var items []WalletOrderItemEntity
		if err := svc.DB.WithContext(ctx).Where("order_id = ?", e.ID).Order("created_at").Find(&items).Error; err == nil {
			for i := range items {
				out.Items = append(out.Items, grpcOrderItem(&items[i]))
			}
		}
		if e.TransactionID != nil {
			var tx WalletTransactionEntity
			if err := svc.DB.WithContext(ctx).First(&tx, "id = ?", *e.TransactionID).Error; err == nil {
				out.Transaction = grpcTransaction(&tx)
			}
		}
	}
	return out
}

func grpcFund(e *WalletFundEntity, recipients []WalletFundRecipientEntity) *gen.DyWalletFund {
	if e == nil {
		return nil
	}
	out := &gen.DyWalletFund{
		Id:               e.ID.String(),
		Currency:         e.Currency,
		TotalAmount:      e.TotalAmount.String(),
		RemainingAmount:  e.RemainingAmount.String(),
		SplitType:        grpcFundSplitType(e.SplitType),
		Status:           grpcFundStatus(e.Status),
		Message:          grpcStringValue(e.Message),
		CreatorAccountId: e.CreatorAccountID.String(),
		ExpiredAt:        grpcTS(e.ExpiredAt),
	}
	for i := range recipients {
		out.Recipients = append(out.Recipients, grpcFundRecipient(&recipients[i]))
	}
	return out
}

func grpcFundRecipient(e *WalletFundRecipientEntity) *gen.DyWalletFundRecipient {
	if e == nil {
		return nil
	}
	return &gen.DyWalletFundRecipient{
		Id:                 e.ID.String(),
		FundId:             e.FundID.String(),
		RecipientAccountId: e.RecipientAccountID.String(),
		Amount:             e.Amount.String(),
		IsReceived:         e.IsReceived,
		ReceivedAt:         grpcTSPtr(e.ReceivedAt),
	}
}

func grpcFundSplitType(value int) gen.DyFundSplitType {
	switch value {
	case FundSplitEven:
		return gen.DyFundSplitType_DY_FUND_SPLIT_TYPE_EVEN
	case FundSplitRandom:
		return gen.DyFundSplitType_DY_FUND_SPLIT_TYPE_RANDOM
	default:
		return gen.DyFundSplitType_DY_FUND_SPLIT_TYPE_UNSPECIFIED
	}
}

func grpcFundStatus(value int) gen.DyFundStatus {
	switch value {
	case FundCreated:
		return gen.DyFundStatus_DY_FUND_STATUS_CREATED
	case FundPartiallyReceived:
		return gen.DyFundStatus_DY_FUND_STATUS_PARTIALLY_RECEIVED
	case FundFullyReceived:
		return gen.DyFundStatus_DY_FUND_STATUS_FULLY_RECEIVED
	case FundExpired:
		return gen.DyFundStatus_DY_FUND_STATUS_EXPIRED
	case FundRefunded:
		return gen.DyFundStatus_DY_FUND_STATUS_REFUNDED
	default:
		return gen.DyFundStatus_DY_FUND_STATUS_UNSPECIFIED
	}
}

func grpcSubscriptionStatus(value int) gen.DySubscriptionStatus {
	switch value {
	case SubscriptionUnpaid:
		return gen.DySubscriptionStatus_DY_SUBSCRIPTION_STATUS_UNPAID
	case SubscriptionActive:
		return gen.DySubscriptionStatus_DY_SUBSCRIPTION_STATUS_ACTIVE
	case SubscriptionExpired:
		return gen.DySubscriptionStatus_DY_SUBSCRIPTION_STATUS_EXPIRED
	case SubscriptionCancelled:
		return gen.DySubscriptionStatus_DY_SUBSCRIPTION_STATUS_CANCELLED
	default:
		return gen.DySubscriptionStatus_DY_SUBSCRIPTION_STATUS_UNSPECIFIED
	}
}

func grpcPaymentDetails(details PaymentDetails) *gen.DyPaymentDetails {
	return &gen.DyPaymentDetails{
		Currency: details.Currency,
		OrderId:  details.OrderID,
	}
}

func grpcCoupon(e *WalletCouponEntity) *gen.DyCoupon {
	if e == nil {
		return nil
	}
	out := &gen.DyCoupon{
		Id:         e.ID.String(),
		Identifier: e.Identifier,
		Code:       e.Code,
		AffectedAt: grpcTSPtr(e.AffectedAt),
		ExpiredAt:  grpcTSPtr(e.ExpiredAt),
		CreatedAt:  grpcTS(e.CreatedAt),
		UpdatedAt:  grpcTS(e.UpdatedAt),
	}
	if e.DiscountAmount != nil {
		out.DiscountAmount = strPtr(e.DiscountAmount.String())
	}
	if e.DiscountRate != nil {
		out.DiscountRate = wrapperspb.Double(*e.DiscountRate)
	}
	if e.MaxUsage != nil {
		out.MaxUsage = wrapperspb.Int32(int32(*e.MaxUsage))
	}
	return out
}

// effectiveSubscriptionFinalPrice mirrors SnWalletSubscription.CalculateFinalPriceAt.
func effectiveSubscriptionFinalPrice(e *WalletSubscriptionEntity, coupon *WalletCouponEntity, now time.Time) decimal.Decimal {
	if e == nil {
		return decimal.Zero
	}
	if e.IsFreeTrial {
		return decimal.Zero
	}
	if coupon == nil {
		return e.BasePrice
	}
	if coupon.AffectedAt != nil && now.Before(*coupon.AffectedAt) ||
		coupon.ExpiredAt != nil && now.After(*coupon.ExpiredAt) {
		return e.BasePrice
	}
	if coupon.DiscountAmount != nil {
		return e.BasePrice.Sub(*coupon.DiscountAmount)
	}
	if coupon.DiscountRate != nil {
		return e.BasePrice.Mul(decimal.NewFromFloat(1 - *coupon.DiscountRate))
	}
	return e.BasePrice
}

// grpcSubscription renders a subscription, loading its coupon so the proto
// carries the same final price the C# navigation property produced.
func grpcSubscription(ctx context.Context, svc *Service, e *WalletSubscriptionEntity) *gen.DySubscription {
	if e == nil {
		return nil
	}
	var coupon *WalletCouponEntity
	if e.CouponID != nil && svc != nil {
		var row WalletCouponEntity
		if err := svc.DB.WithContext(ctx).First(&row, "id = ?", *e.CouponID).Error; err == nil {
			coupon = &row
		}
	}
	now := time.Now().UTC()
	out := &gen.DySubscription{
		Id:             e.ID.String(),
		BegunAt:        grpcTS(e.BegunAt),
		EndedAt:        grpcTSPtr(e.EndedAt),
		Identifier:     e.Identifier,
		IsActive:       e.IsActive,
		IsFreeTrial:    e.IsFreeTrial,
		Status:         grpcSubscriptionStatus(e.Status),
		PaymentMethod:  e.PaymentMethod,
		PaymentDetails: grpcPaymentDetails(parsePaymentDetails(e.PaymentDetails)),
		BasePrice:      e.BasePrice.String(),
		CouponId:       uuidPointer(e.CouponID),
		RenewalAt:      grpcTSPtr(e.RenewalAt),
		AccountId:      e.AccountID.String(),
		IsAvailable:    subscriptionAvailable(e, now),
		FinalPrice:     effectiveSubscriptionFinalPrice(e, coupon, now).String(),
		CreatedAt:      grpcTS(e.CreatedAt),
		UpdatedAt:      grpcTS(e.UpdatedAt),
	}
	if e.GroupIdentifier != nil {
		out.GroupIdentifier = *e.GroupIdentifier
	}
	if e.DisplayName != nil {
		out.DisplayName = *e.DisplayName
	}
	out.PerkLevel = int32(e.PerkLevel)
	if coupon != nil {
		out.Coupon = grpcCoupon(coupon)
	}
	return out
}

func grpcMerchant(e *MerchantEntity) *gen.DyMerchant {
	if e == nil {
		return nil
	}
	return &gen.DyMerchant{
		Id:              e.ID.String(),
		PublisherId:     e.PublisherID.String(),
		PaymentWalletId: uuidPointer(e.PaymentWalletID),
		Name:            e.Name,
	}
}

func grpcSettlementStatus(value int) gen.DyMerchantSettlementStatus {
	switch value {
	case SettlementPending:
		return gen.DyMerchantSettlementStatus_DY_MERCHANT_SETTLEMENT_STATUS_PENDING
	case SettlementSettled:
		return gen.DyMerchantSettlementStatus_DY_MERCHANT_SETTLEMENT_STATUS_SETTLED
	case SettlementCancelled:
		return gen.DyMerchantSettlementStatus_DY_MERCHANT_SETTLEMENT_STATUS_CANCELLED
	default:
		return gen.DyMerchantSettlementStatus_DY_MERCHANT_SETTLEMENT_STATUS_UNSPECIFIED
	}
}

func grpcSettlementTrigger(value *int) *gen.DyMerchantSettlementTrigger {
	if value == nil {
		return nil
	}
	trigger := gen.DyMerchantSettlementTrigger_DY_MERCHANT_SETTLEMENT_TRIGGER_UNSPECIFIED
	switch *value {
	case SettlementAutomatic:
		trigger = gen.DyMerchantSettlementTrigger_DY_MERCHANT_SETTLEMENT_TRIGGER_AUTOMATIC
	case SettlementManual:
		trigger = gen.DyMerchantSettlementTrigger_DY_MERCHANT_SETTLEMENT_TRIGGER_MANUAL
	}
	return &trigger
}

func grpcSettlement(e *MerchantSettlementEntity) *gen.DyMerchantSettlement {
	if e == nil {
		return nil
	}
	return &gen.DyMerchantSettlement{
		Id:              e.ID.String(),
		MerchantId:      e.MerchantID.String(),
		OrderId:         uuidPointer(e.OrderID),
		AwardId:         uuidPointer(e.AwardID),
		PaymentWalletId: e.PaymentWalletID.String(),
		Currency:        e.Currency,
		Amount:          e.Amount.String(),
		Status:          grpcSettlementStatus(e.Status),
		SettledBy:       grpcSettlementTrigger(e.SettledBy),
		SettledAt:       grpcTSPtr(e.SettledAt),
	}
}

// ── DyWalletService ────────────────────────────────────────────────────────

type dyWalletGRPC struct {
	gen.UnimplementedDyWalletServiceServer
	d *Deps
}

func (s *dyWalletGRPC) GetWallet(ctx context.Context, req *gen.DyGetWalletRequest) (*gen.DyWallet, error) {
	var (
		wallet  *WalletEntity
		pockets []WalletPocketEntity
		err     error
	)
	switch {
	case strings.TrimSpace(req.WalletId) != "":
		id, parseErr := grpcParseUUID(req.WalletId, "wallet_id")
		if parseErr != nil {
			return nil, parseErr
		}
		wallet, pockets, err = s.d.Service.GetWallet(ctx, id)
	case strings.TrimSpace(req.PublicId) != "":
		wallet, pockets, err = s.d.Service.GetWalletByPublicID(ctx, req.PublicId)
	case strings.TrimSpace(req.AccountId) != "":
		id, parseErr := grpcParseUUID(req.AccountId, "account_id")
		if parseErr != nil {
			return nil, parseErr
		}
		wallet, pockets, err = s.d.Service.GetAccountWallet(ctx, id)
	default:
		return nil, status.Error(codes.NotFound, "Wallet not found.")
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Error(codes.NotFound, "Wallet not found.")
		}
		return nil, grpcError(err)
	}
	if wallet == nil {
		return nil, status.Error(codes.NotFound, "Wallet not found.")
	}
	return grpcWallet(wallet, pockets), nil
}

func (s *dyWalletGRPC) CheckWalletExists(ctx context.Context, req *gen.DyCheckWalletExistsRequest) (*gen.DyCheckWalletExistsResponse, error) {
	id, err := grpcParseUUID(req.AccountId, "account_id")
	if err != nil {
		return nil, err
	}
	exists, err := s.d.Service.CheckWalletExists(ctx, id)
	if err != nil {
		return nil, grpcError(err)
	}
	return &gen.DyCheckWalletExistsResponse{Exists: exists}, nil
}

func (s *dyWalletGRPC) CreateWallet(ctx context.Context, req *gen.DyCreateWalletRequest) (*gen.DyWallet, error) {
	var accountID, realmID *uuid.UUID
	if strings.TrimSpace(req.AccountId) != "" {
		id, err := grpcParseUUID(req.AccountId, "account_id")
		if err != nil {
			return nil, err
		}
		accountID = &id
	}
	if strings.TrimSpace(req.RealmId) != "" {
		id, err := grpcParseUUID(req.RealmId, "realm_id")
		if err != nil {
			return nil, err
		}
		realmID = &id
	}
	name := grpcOptionalStringPtr(&req.Name)
	wallet, err := s.d.Service.CreateWallet(ctx, accountID, realmID, name)
	if err != nil {
		return nil, grpcError(err)
	}
	return grpcWallet(wallet, nil), nil
}

func (s *dyWalletGRPC) GetOrCreateWalletPocket(ctx context.Context, req *gen.DyGetOrCreateWalletPocketRequest) (*gen.DyWalletPocket, error) {
	walletID, err := grpcParseUUID(req.WalletId, "wallet_id")
	if err != nil {
		return nil, err
	}
	var initial *decimal.Decimal
	if req.InitialAmount != nil {
		amount, parseErr := grpcParseDecimal(*req.InitialAmount, "initial_amount")
		if parseErr != nil {
			return nil, parseErr
		}
		initial = &amount
	}
	pocket, _, err := s.d.Service.GetOrCreatePocket(ctx, nil, walletID, req.Currency, initial)
	if err != nil {
		return nil, grpcError(err)
	}
	return grpcPocket(pocket), nil
}

// ── DyPaymentService ───────────────────────────────────────────────────────

type dyPaymentGRPC struct {
	gen.UnimplementedDyPaymentServiceServer
	d    *Deps
	subs *Subscriptions
}

func (s *dyPaymentGRPC) CreateOrder(ctx context.Context, req *gen.DyCreateOrderRequest) (*gen.DyOrder, error) {
	amount, err := grpcParseDecimal(req.Amount, "amount")
	if err != nil {
		return nil, err
	}
	appIdentifier := "internal"
	if req.AppIdentifier != nil && strings.TrimSpace(*req.AppIdentifier) != "" {
		appIdentifier = *req.AppIdentifier
	}
	var payeeWalletID *uuid.UUID
	if req.PayeeWalletId != nil && strings.TrimSpace(*req.PayeeWalletId) != "" {
		id, parseErr := grpcParseUUID(*req.PayeeWalletId, "payee_wallet_id")
		if parseErr != nil {
			return nil, parseErr
		}
		payeeWalletID = &id
	} else {
		resolved, resolveErr := s.resolveMerchantWalletID(ctx, appIdentifier)
		if resolveErr != nil {
			return nil, grpcError(resolveErr)
		}
		payeeWalletID = resolved
	}

	var expiration *time.Duration
	if req.Expiration != nil {
		duration := req.Expiration.AsDuration()
		expiration = &duration
	}
	var meta map[string]any
	if len(req.Meta) > 0 {
		if err := json.Unmarshal(req.Meta, &meta); err != nil {
			return nil, status.Error(codes.InvalidArgument, "meta must be a JSON object")
		}
	}
	items := make([]OrderItemInput, 0, len(req.Items))
	for _, item := range req.Items {
		if item == nil {
			continue
		}
		unitPrice, parseErr := grpcParseDecimal(item.UnitPrice, "items.unit_price")
		if parseErr != nil {
			return nil, parseErr
		}
		items = append(items, OrderItemInput{
			ProductIdentifier: item.ProductIdentifier,
			Quantity:          int(item.Quantity),
			UnitPrice:         unitPrice,
			Currency:          item.Currency,
		})
	}

	order, err := s.d.Service.CreateOrder(ctx, OrderInput{
		PayeeWalletID:     payeeWalletID,
		Currency:          req.Currency,
		Amount:            amount,
		Expiration:        expiration,
		AppIdentifier:     strPtr(appIdentifier),
		ProductIdentifier: req.ProductIdentifier,
		Remarks:           req.Remarks,
		Meta:              meta,
		Reuseable:         req.Reuseable,
		Items:             items,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return grpcOrder(ctx, s.d.Service, order), nil
}

// resolveMerchantWalletID mirrors PaymentServiceGrpc.ResolveMerchantWalletIdAsync:
// a "developer.app:<id>" order pays the developer's merchant wallet.
func (s *dyPaymentGRPC) resolveMerchantWalletID(ctx context.Context, appIdentifier string) (*uuid.UUID, error) {
	const prefix = "developer.app:"
	if len(appIdentifier) <= len(prefix) || !strings.EqualFold(appIdentifier[:len(prefix)], prefix) {
		return nil, nil
	}
	appID, err := uuid.Parse(appIdentifier[len(prefix):])
	if err != nil {
		return nil, nil
	}
	if s.d.Clients == nil || s.d.Clients.Develop == nil {
		return nil, nil
	}
	developer, err := s.d.Clients.Develop.GetAppDeveloper(ctx, &gen.DyGetAppDeveloperRequest{AppId: appID.String()})
	if err != nil || developer.GetDeveloper() == nil {
		// The C# swallows RpcException and treats the order as system-paid.
		return nil, nil
	}
	publisherID, err := uuid.Parse(developer.Developer.PublisherId)
	if err != nil {
		return nil, nil
	}
	merchant, err := s.d.Service.GetMerchantByPublisher(ctx, publisherID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return merchant.PaymentWalletID, nil
}

func (s *dyPaymentGRPC) CreateTransactionWithAccount(ctx context.Context, req *gen.DyCreateTransactionWithAccountRequest) (*gen.DyTransaction, error) {
	payer, err := grpcOptionalUUID(req.PayerAccountId)
	if err != nil {
		return nil, err
	}
	payee, err := grpcOptionalUUID(req.PayeeAccountId)
	if err != nil {
		return nil, err
	}
	amount, err := grpcParseDecimal(req.Amount, "amount")
	if err != nil {
		return nil, err
	}
	transaction, err := s.d.Service.CreateTransactionWithAccounts(
		ctx, payer, payee, req.Currency, amount, grpcOptionalString(req.Remarks), transactionTypeFromProto(req.Type),
	)
	if err != nil {
		return nil, grpcError(err)
	}
	return grpcTransaction(transaction), nil
}

func (s *dyPaymentGRPC) CreateTransaction(ctx context.Context, req *gen.DyCreateTransactionRequest) (*gen.DyTransaction, error) {
	payer, err := grpcOptionalUUID(req.PayerWalletId)
	if err != nil {
		return nil, err
	}
	payee, err := grpcOptionalUUID(req.PayeeWalletId)
	if err != nil {
		return nil, err
	}
	amount, err := grpcParseDecimal(req.Amount, "amount")
	if err != nil {
		return nil, err
	}
	transaction, err := s.d.Service.CreateTransaction(ctx, TransactionInput{
		PayerWalletID: payer,
		PayeeWalletID: payee,
		Currency:      req.Currency,
		Amount:        amount,
		Remarks:       grpcOptionalString(req.Remarks),
		Type:          transactionTypeFromProto(req.Type),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return grpcTransaction(transaction), nil
}

func (s *dyPaymentGRPC) CancelOrder(ctx context.Context, req *gen.DyCancelOrderRequest) (*gen.DyOrder, error) {
	orderID, err := grpcParseUUID(req.OrderId, "order_id")
	if err != nil {
		return nil, err
	}
	order, err := s.d.Service.CancelOrder(ctx, orderID)
	if err != nil {
		return nil, grpcError(err)
	}
	return grpcOrder(ctx, s.d.Service, order), nil
}

func (s *dyPaymentGRPC) RefundOrder(ctx context.Context, req *gen.DyRefundOrderRequest) (*gen.DyRefundOrderResponse, error) {
	orderID, err := grpcParseUUID(req.OrderId, "order_id")
	if err != nil {
		return nil, err
	}
	order, transaction, err := s.d.Service.RefundOrder(ctx, orderID)
	if err != nil {
		return nil, grpcError(err)
	}
	return &gen.DyRefundOrderResponse{
		Order:             grpcOrder(ctx, s.d.Service, order),
		RefundTransaction: grpcTransaction(transaction),
	}, nil
}

func (s *dyPaymentGRPC) Transfer(ctx context.Context, req *gen.DyTransferRequest) (*gen.DyTransaction, error) {
	amount, err := grpcParseDecimal(req.Amount, "amount")
	if err != nil {
		return nil, err
	}
	remarks := grpcOptionalStringPtr(&req.Remarks)

	var transaction *WalletTransactionEntity
	switch {
	case req.PayerWalletId != "" && req.PayeeWalletId != "":
		payerID, parseErr := grpcParseUUID(req.PayerWalletId, "payer_wallet_id")
		if parseErr != nil {
			return nil, parseErr
		}
		payeeID, parseErr := grpcParseUUID(req.PayeeWalletId, "payee_wallet_id")
		if parseErr != nil {
			return nil, parseErr
		}
		transaction, err = s.d.Service.TransferBetweenWallets(ctx, payerID, payeeID, req.Currency, amount, remarks, false, false)
	case req.PayerWalletId != "" && req.PayeePublicId != "":
		payerID, parseErr := grpcParseUUID(req.PayerWalletId, "payer_wallet_id")
		if parseErr != nil {
			return nil, parseErr
		}
		payeeWallet, _, lookupErr := s.d.Service.GetWalletByPublicID(ctx, req.PayeePublicId)
		if lookupErr != nil || payeeWallet == nil {
			return nil, status.Error(codes.NotFound, "Payee wallet not found.")
		}
		transaction, err = s.d.Service.TransferBetweenWallets(ctx, payerID, payeeWallet.ID, req.Currency, amount, remarks, false, false)
	case req.PayerAccountId != "" && req.PayeeAccountId != "":
		payerID, parseErr := grpcParseUUID(req.PayerAccountId, "payer_account_id")
		if parseErr != nil {
			return nil, parseErr
		}
		payeeID, parseErr := grpcParseUUID(req.PayeeAccountId, "payee_account_id")
		if parseErr != nil {
			return nil, parseErr
		}
		transaction, err = s.transferBetweenAccounts(ctx, payerID, payeeID, req.Currency, amount, remarks)
	default:
		return nil, status.Error(codes.InvalidArgument,
			"Either payer_account_id/payee_account_id, payer_wallet_id/payee_wallet_id, or payer_wallet_id/payee_public_id is required.")
	}
	if err != nil {
		return nil, grpcError(err)
	}
	return grpcTransaction(transaction), nil
}

// transferBetweenAccounts ports PaymentService.TransferAsync. The C# composed a
// localized remark from both account names; without the wallet localizer this
// port always uses the English phrasing.
func (s *dyPaymentGRPC) transferBetweenAccounts(ctx context.Context, payerAccountID, payeeAccountID uuid.UUID, currency string, amount decimal.Decimal, remarks *string) (*WalletTransactionEntity, error) {
	payerWallet, _, err := s.d.Service.GetAccountWallet(ctx, payerAccountID)
	if err != nil || payerWallet == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "Payer wallet not found for account %s", payerAccountID)
	}
	payeeWallet, _, err := s.d.Service.GetAccountWallet(ctx, payeeAccountID)
	if err != nil || payeeWallet == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "Payee wallet not found for account %s", payeeAccountID)
	}
	if remarks == nil {
		payerName, payeeName := "unknown", "unknown"
		if accounts := walletAccountStore(s.d); accounts != nil {
			if payer, lookupErr := accounts.GetAccountByID(ctx, payerAccountID); lookupErr == nil {
				payerName = payer.Name
			}
			if payee, lookupErr := accounts.GetAccountByID(ctx, payeeAccountID); lookupErr == nil {
				payeeName = payee.Name
			}
		}
		// The C# localized this against the payee's language; the remark is
		// persisted and later returned through the transaction API.
		remark := s.d.Service.Localize(ctx, payeeAccountID.String(), "transferRemark", map[string]string{
			"payer": "@" + payerName,
			"payee": "@" + payeeName,
		})
		remarks = &remark
	}
	return s.d.Service.TransferBetweenWallets(ctx, payerWallet.ID, payeeWallet.ID, currency, amount, remarks, false, false)
}

func (s *dyPaymentGRPC) GetWalletFund(ctx context.Context, req *gen.DyGetWalletFundRequest) (*gen.DyWalletFund, error) {
	fundID, err := grpcParseUUID(req.FundId, "fund_id")
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid fund ID.")
	}
	fund, recipients, err := s.d.Service.GetFund(ctx, fundID)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Error(codes.NotFound, "Fund not found.")
		}
		return nil, grpcError(err)
	}
	if fund == nil {
		return nil, status.Error(codes.NotFound, "Fund not found.")
	}
	return grpcFund(fund, recipients), nil
}

func (s *dyPaymentGRPC) RegisterAppSubscriptionDefinition(ctx context.Context, req *gen.DyRegisterAppSubscriptionDefinitionRequest) (*gen.DyRegisterAppSubscriptionDefinitionResponse, error) {
	if strings.TrimSpace(req.Identifier) == "" || strings.TrimSpace(req.AppIdentifier) == "" {
		return nil, status.Error(codes.InvalidArgument, "identifier and app_identifier required")
	}
	if s.subs == nil {
		return nil, status.Error(codes.Unimplemented, "subscription catalog is unavailable")
	}
	if req.Remove {
		if _, err := s.subs.RemoveAppDefinition(ctx, req.Identifier, req.AppIdentifier); err != nil {
			return nil, grpcError(err)
		}
		return &gen.DyRegisterAppSubscriptionDefinitionResponse{Created: false}, nil
	}
	basePrice, err := grpcParseDecimal(req.BasePrice, "base_price")
	if err != nil {
		return nil, err
	}
	created, err := s.subs.RegisterAppDefinition(
		ctx,
		req.Identifier,
		req.AppIdentifier,
		req.DisplayName,
		req.Currency,
		basePrice,
		grpcOptionalStringPtr(&req.GroupIdentifier),
		int(req.CycleDurationDays),
	)
	if err != nil {
		return nil, grpcError(err)
	}
	return &gen.DyRegisterAppSubscriptionDefinitionResponse{Created: created}, nil
}

// ── DySubscriptionService ──────────────────────────────────────────────────

type dySubscriptionGRPC struct {
	gen.UnimplementedDySubscriptionServiceServer
	d    *Deps
	subs *Subscriptions
}

func (s *dySubscriptionGRPC) GetSubscription(ctx context.Context, req *gen.DyGetSubscriptionRequest) (*gen.DySubscription, error) {
	accountID, err := grpcParseUUID(req.AccountId, "account_id")
	if err != nil {
		return nil, err
	}
	subscription, err := s.subs.GetSubscription(ctx, accountID, req.Identifier)
	if err != nil {
		return nil, grpcError(err)
	}
	if subscription == nil {
		return nil, status.Error(codes.NotFound, "Subscription not found")
	}
	return grpcSubscription(ctx, s.d.Service, subscription), nil
}

func (s *dySubscriptionGRPC) GetPerkSubscription(ctx context.Context, req *gen.DyGetPerkSubscriptionRequest) (*gen.DySubscription, error) {
	accountID, err := grpcParseUUID(req.AccountId, "account_id")
	if err != nil {
		return nil, err
	}
	subscription, err := s.subs.GetPerkSubscription(ctx, accountID)
	if err != nil {
		return nil, grpcError(err)
	}
	if subscription == nil {
		// RemoteSubscriptionService treats an empty id as "no perk".
		return &gen.DySubscription{Id: ""}, nil
	}
	return grpcSubscription(ctx, s.d.Service, subscription), nil
}

func (s *dySubscriptionGRPC) GetPerkSubscriptions(ctx context.Context, req *gen.DyGetPerkSubscriptionsRequest) (*gen.DyGetPerkSubscriptionsResponse, error) {
	accountIDs := make([]uuid.UUID, 0, len(req.AccountIds))
	for _, raw := range req.AccountIds {
		id, err := grpcParseUUID(raw, "account_ids")
		if err != nil {
			return nil, err
		}
		accountIDs = append(accountIDs, id)
	}
	subscriptions, err := s.subs.GetPerkSubscriptions(ctx, accountIDs)
	if err != nil {
		return nil, grpcError(err)
	}
	response := &gen.DyGetPerkSubscriptionsResponse{}
	for _, subscription := range subscriptions {
		if subscription == nil {
			continue
		}
		response.Subscriptions = append(response.Subscriptions, grpcSubscription(ctx, s.d.Service, subscription))
	}
	return response, nil
}

func (s *dySubscriptionGRPC) CreateSubscription(ctx context.Context, req *gen.DyCreateSubscriptionRequest) (*gen.DySubscription, error) {
	accountID, err := grpcParseUUID(req.AccountId, "account_id")
	if err != nil {
		return nil, err
	}
	accounts := walletAccountStore(s.d)
	if accounts == nil {
		return nil, status.Error(codes.FailedPrecondition, "account store is unavailable")
	}
	account, err := accounts.GetAccountWithProfile(ctx, accountID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "Account not found")
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	couponCode := ""
	if req.CouponCode != nil {
		couponCode = *req.CouponCode
	}
	subscription, err := s.subs.CreateSubscription(
		ctx, account, req.Identifier, req.PaymentMethod, PaymentDetails{}, nil, couponCode, req.IsFreeTrial, true, true,
	)
	if err != nil {
		return nil, grpcError(err)
	}
	return grpcSubscription(ctx, s.d.Service, subscription), nil
}

func (s *dySubscriptionGRPC) CancelSubscription(ctx context.Context, req *gen.DyCancelSubscriptionRequest) (*gen.DySubscription, error) {
	accountID, err := grpcParseUUID(req.AccountId, "account_id")
	if err != nil {
		return nil, err
	}
	subscription, err := s.subs.CancelSubscription(ctx, accountID, req.Identifier)
	if err != nil {
		return nil, grpcError(err)
	}
	return grpcSubscription(ctx, s.d.Service, subscription), nil
}

// ── DyMerchantService ──────────────────────────────────────────────────────

type dyMerchantGRPC struct {
	gen.UnimplementedDyMerchantServiceServer
	d *Deps
}

func (s *dyMerchantGRPC) UpsertMerchant(ctx context.Context, req *gen.DyUpsertMerchantRequest) (*gen.DyMerchant, error) {
	publisherID, err := grpcParseUUID(req.PublisherId, "publisher_id")
	if err != nil {
		return nil, err
	}
	var paymentWalletID *uuid.UUID
	if req.PaymentWalletId != nil && strings.TrimSpace(*req.PaymentWalletId) != "" {
		id, parseErr := grpcParseUUID(*req.PaymentWalletId, "payment_wallet_id")
		if parseErr != nil {
			return nil, parseErr
		}
		paymentWalletID = &id
	}
	merchant, err := s.d.Service.UpsertMerchant(ctx, publisherID, paymentWalletID, req.Name)
	if err != nil {
		return nil, grpcError(err)
	}
	return grpcMerchant(merchant), nil
}

func (s *dyMerchantGRPC) CreateMerchantSettlement(ctx context.Context, req *gen.DyCreateMerchantSettlementRequest) (*gen.DyMerchantSettlement, error) {
	publisherID, err := grpcParseUUID(req.PublisherId, "publisher_id")
	if err != nil {
		return nil, err
	}
	merchant, err := s.d.Service.GetMerchantByPublisher(ctx, publisherID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, grpcError(err)
		}
		merchant, err = s.d.Service.UpsertMerchant(ctx, publisherID, nil, nil)
		if err != nil {
			return nil, grpcError(err)
		}
	}
	if merchant == nil || merchant.PaymentWalletID == nil {
		return nil, status.Error(codes.FailedPrecondition, "Merchant has no payment wallet configured")
	}
	amount, err := grpcParseDecimal(req.Amount, "amount")
	if err != nil {
		return nil, err
	}
	var orderID, awardID *uuid.UUID
	if req.OrderId != nil && strings.TrimSpace(*req.OrderId) != "" {
		id, parseErr := grpcParseUUID(*req.OrderId, "order_id")
		if parseErr != nil {
			return nil, parseErr
		}
		orderID = &id
	}
	if req.AwardId != nil && strings.TrimSpace(*req.AwardId) != "" {
		id, parseErr := grpcParseUUID(*req.AwardId, "award_id")
		if parseErr != nil {
			return nil, parseErr
		}
		awardID = &id
	}
	settlement, err := s.d.Service.CreateSettlement(
		ctx, merchant.ID, nil, *merchant.PaymentWalletID, req.Currency, amount, orderID,
	)
	if err != nil {
		return nil, grpcError(err)
	}
	// Service.CreateSettlement has no award parameter (awards live in Sphere);
	// record the reference the gRPC contract carries.
	if awardID != nil {
		if err := s.d.Service.DB.WithContext(ctx).Model(&MerchantSettlementEntity{}).
			Where("id = ?", settlement.ID).Update("award_id", *awardID).Error; err != nil {
			return nil, grpcError(err)
		}
		settlement.AwardID = awardID
	}
	return grpcSettlement(settlement), nil
}

func (s *dyMerchantGRPC) GetPendingSettlements(ctx context.Context, req *gen.DyGetPendingSettlementsRequest) (*gen.DyGetPendingSettlementsResponse, error) {
	walletID, err := grpcParseUUID(req.PaymentWalletId, "payment_wallet_id")
	if err != nil {
		return nil, err
	}
	settlements, err := s.d.Service.PendingSettlements(ctx, walletID)
	if err != nil {
		return nil, grpcError(err)
	}
	response := &gen.DyGetPendingSettlementsResponse{}
	for i := range settlements {
		response.Settlements = append(response.Settlements, grpcSettlement(&settlements[i]))
	}
	return response, nil
}

func (s *dyMerchantGRPC) SettleMerchant(ctx context.Context, req *gen.DySettleMerchantRequest) (*gen.DySettleMerchantResponse, error) {
	walletID, err := grpcParseUUID(req.PaymentWalletId, "payment_wallet_id")
	if err != nil {
		return nil, err
	}
	transactions, err := s.d.Service.SettleWallet(ctx, walletID, SettlementManual)
	if err != nil {
		return nil, grpcError(err)
	}
	response := &gen.DySettleMerchantResponse{}
	for i := range transactions {
		response.Transactions = append(response.Transactions, grpcTransaction(&transactions[i]))
	}
	return response, nil
}
