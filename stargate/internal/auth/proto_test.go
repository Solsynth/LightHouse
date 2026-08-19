package auth

import (
	"testing"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// TestAccountToProtoPerk pins the perk-subscription proto contract: the
// wallet's "no active subscription" sentinel (a non-nil reference with an
// empty Id) must NOT be emitted on the wire. Downstream C# consumers
// (SnSubscriptionReferenceObject.FromProtoValue) Guid.Parse the id and throw
// FormatException on an empty string, which previously failed auth for every
// user without a subscription.
func TestAccountToProtoPerk(t *testing.T) {
	const (
		accountID = "11111111-1111-1111-1111-111111111111"
		subID     = "b78f8d4a-4d2e-4a5c-9a4b-9b8f7a6e5d4c"
	)
	valid := &model.SnSubscriptionReferenceObject{
		Id:          subID,
		Identifier:  "dy_plus",
		PerkLevel:   2,
		IsActive:    true,
		IsAvailable: true,
		BasePrice:   "19.99",
		FinalPrice:  "14.99",
		AccountId:   accountID,
	}

	tests := []struct {
		name    string
		perk    *model.SnSubscriptionReferenceObject
		wantNil bool
	}{
		{"valid subscription", valid, false},
		{"empty id sentinel", &model.SnSubscriptionReferenceObject{Id: "", Identifier: "dy_plus"}, true},
		{"nil subscription", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto := AccountToProto(&model.Account{
				Id:               accountID,
				Name:             "tester",
				PerkSubscription: tt.perk,
				PerkLevel:        2,
			})
			if proto == nil {
				t.Fatal("AccountToProto = nil")
			}
			if tt.wantNil {
				if proto.PerkSubscription != nil {
					t.Fatalf("PerkSubscription = %+v, want nil", proto.PerkSubscription)
				}
				if proto.PerkLevel != nil {
					t.Fatalf("PerkLevel = %v, want nil", proto.PerkLevel)
				}
				return
			}
			if proto.PerkSubscription == nil {
				t.Fatal("PerkSubscription = nil, want non-nil")
			}
			if proto.PerkSubscription.Id != subID {
				t.Errorf("PerkSubscription.Id = %q, want %q", proto.PerkSubscription.Id, subID)
			}
			// Prices must be present — the C# side decimal.Parse's them and
			// throws on "".
			if proto.PerkSubscription.BasePrice != "19.99" || proto.PerkSubscription.FinalPrice != "14.99" {
				t.Errorf("prices = %q/%q, want 19.99/14.99",
					proto.PerkSubscription.BasePrice, proto.PerkSubscription.FinalPrice)
			}
			if proto.PerkLevel == nil || *proto.PerkLevel != 2 {
				t.Errorf("PerkLevel = %v, want 2", proto.PerkLevel)
			}
		})
	}
}

func TestProfileToProtoCarriesProfileMarkers(t *testing.T) {
	active := any(map[string]any{
		"id":         "badge-1",
		"type":       "pioneer",
		"label":      "Pioneer",
		"meta":       map[string]any{"tier": "founder"},
		"account_id": "11111111-1111-1111-1111-111111111111",
	})
	profile := &model.Profile{
		AccountId:    "11111111-1111-1111-1111-111111111111",
		ActiveBadge:  &active,
		Verification: &model.SnVerificationMark{Type: 1},
	}
	got := ProfileToProto(profile)
	if got == nil || got.ActiveBadge == nil {
		t.Fatal("profile active_badge = nil, want gRPC marker")
	}
	if got.ActiveBadge.Id != "badge-1" || got.ActiveBadge.Type != "pioneer" {
		t.Fatalf("profile active_badge = %+v, want badge-1/pioneer", got.ActiveBadge)
	}
	if got.Verification == nil || got.Verification.Type != 1 {
		t.Fatalf("profile verification = %+v, want type 1", got.Verification)
	}
}
