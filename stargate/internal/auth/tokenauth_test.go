package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// TestTokenExpired pins the exp claim check: numeric and string encodings
// (the C# minting serializes claims as strings) must both be honored, and a
// missing claim must not be treated as expired.
func TestTokenExpired(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name   string
		claims jwt.MapClaims
		want   bool
	}{
		{"number expired", jwt.MapClaims{"exp": float64(now - 10)}, true},
		{"string expired", jwt.MapClaims{"exp": strconv.FormatInt(now-10, 10)}, true},
		{"number future", jwt.MapClaims{"exp": float64(now + 3600)}, false},
		{"string future", jwt.MapClaims{"exp": strconv.FormatInt(now+3600, 10)}, false},
		{"missing", jwt.MapClaims{}, false},
		{"garbage", jwt.MapClaims{"exp": "soon"}, false},
	}
	for _, tc := range cases {
		if got := tokenExpired(tc.claims); got != tc.want {
			t.Errorf("tokenExpired(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// stubPerkProvider returns a fixed subscription or error.
type stubPerkProvider struct {
	sub *model.SnSubscriptionReferenceObject
	err error
}

func (p stubPerkProvider) GetPerkSubscription(context.Context, string) (*model.SnSubscriptionReferenceObject, error) {
	return p.sub, p.err
}

// TestHydratePerkWireContract pins the gRPC account wire contract for perk:
// after HydratePerk, AccountToProto must emit PerkLevel/PerkSubscription so
// downstream consumers (DysonFS derives the perk storage base quota from
// these) see the real perk level instead of a zeroed account. A nil provider
// or a wallet failure must degrade to no perk, mirroring the C# try/catch.
func TestHydratePerkWireContract(t *testing.T) {
	const (
		accountID = "11111111-1111-1111-1111-111111111111"
		subID     = "b78f8d4a-4d2e-4a5c-9a4b-9b8f7a6e5d4c"
	)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	run := func(t *testing.T, perk stubPerkProvider, wantLevel *int32) *gen.DyAccount {
		t.Helper()
		svc := NewTokenAuthService(nil, nil, &JWTService{}, perk, nil, log)
		account := &model.Account{Id: accountID, Name: "tester"}
		svc.HydratePerk(ctx, account)
		proto := AccountToProto(account)
		if proto == nil {
			t.Fatal("AccountToProto = nil")
		}
		if wantLevel == nil {
			if proto.PerkLevel != nil {
				t.Fatalf("PerkLevel = %v, want nil (no subscription)", *proto.PerkLevel)
			}
			return proto
		}
		if proto.PerkLevel == nil || *proto.PerkLevel != *wantLevel {
			t.Fatalf("PerkLevel = %v, want %d", proto.PerkLevel, *wantLevel)
		}
		return proto
	}

	t.Run("subscription hydrates perk level", func(t *testing.T) {
		lvl := int32(2)
		proto := run(t, stubPerkProvider{sub: &model.SnSubscriptionReferenceObject{
			Id: subID, Identifier: "dy_plus", PerkLevel: 2, IsActive: true, IsAvailable: true,
			BasePrice: "19.99", FinalPrice: "14.99", AccountId: accountID,
		}}, &lvl)
		if proto.PerkSubscription == nil || proto.PerkSubscription.Id != subID {
			t.Fatalf("PerkSubscription = %+v, want id %q", proto.PerkSubscription, subID)
		}
	})

	t.Run("nil provider degrades to no perk", func(t *testing.T) {
		proto := run(t, stubPerkProvider{}, nil)
		if proto.PerkSubscription != nil {
			t.Fatalf("PerkSubscription = %+v, want nil", proto.PerkSubscription)
		}
	})

	t.Run("wallet failure degrades to no perk", func(t *testing.T) {
		proto := run(t, stubPerkProvider{err: errors.New("wallet down")}, nil)
		if proto.PerkSubscription != nil {
			t.Fatalf("PerkSubscription = %+v, want nil", proto.PerkSubscription)
		}
	})
}

func TestAccountFromProtoCarriesProfile(t *testing.T) {
	account := accountFromProto(&gen.DyAccount{
		Id: "acct-1",
		Profile: &gen.DyAccountProfile{
			Id:         "profile-1",
			AccountId:  "acct-1",
			Level:      42,
			Experience: 12345,
		},
	})
	if account.Profile == nil {
		t.Fatal("account profile = nil, want hydrated profile")
	}
	if account.Profile.Id != "profile-1" || account.Profile.AccountId != "acct-1" ||
		account.Profile.Level != 42 || account.Profile.Experience != 12345 {
		t.Fatalf("account profile = %+v, want id/profile level data", account.Profile)
	}
}
