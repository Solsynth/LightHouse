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
			if proto.PerkLevel == nil || *proto.PerkLevel != 2 {
				t.Errorf("PerkLevel = %v, want 2", proto.PerkLevel)
			}
		})
	}
}
