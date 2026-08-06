package grpcclient

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	gen "src.solsynth.dev/sosys/go/proto"
)

// fakeWalletClient implements gen.DySubscriptionServiceClient with only
// GetPerkSubscription overridden; the embedded interface covers the rest.
type fakeWalletClient struct {
	gen.DySubscriptionServiceClient
	resp *gen.DySubscription
	err  error
}

func (f *fakeWalletClient) GetPerkSubscription(
	ctx context.Context,
	in *gen.DyGetPerkSubscriptionRequest,
	opts ...grpc.CallOption,
) (*gen.DySubscription, error) {
	return f.resp, f.err
}

// TestGetPerkSubscriptionEmptySentinel pins the wallet wire contract: the
// wallet returns an empty-id DySubscription for users without an active perk
// subscription, which must map to nil (mirrors
// RemoteSubscriptionService.GetPerkSubscription's IsNullOrEmpty guard).
func TestGetPerkSubscriptionEmptySentinel(t *testing.T) {
	ctx := context.Background()
	const accountID = "11111111-1111-1111-1111-111111111111"

	t.Run("empty id maps to nil", func(t *testing.T) {
		p := &WalletPerkProvider{Client: &fakeWalletClient{resp: &gen.DySubscription{Id: ""}}}
		got, err := p.GetPerkSubscription(ctx, accountID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("GetPerkSubscription = %+v, want nil", got)
		}
	})

	t.Run("valid id maps to model", func(t *testing.T) {
		const subID = "b78f8d4a-4d2e-4a5c-9a4b-9b8f7a6e5d4c"
		p := &WalletPerkProvider{Client: &fakeWalletClient{
			resp: &gen.DySubscription{Id: subID, Identifier: "dy_plus"},
		}}
		got, err := p.GetPerkSubscription(ctx, accountID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.Id != subID {
			t.Fatalf("GetPerkSubscription = %+v, want model with id %q", got, subID)
		}
	})

	t.Run("client error propagates", func(t *testing.T) {
		sentinel := errors.New("wallet down")
		p := &WalletPerkProvider{Client: &fakeWalletClient{err: sentinel}}
		if _, err := p.GetPerkSubscription(ctx, accountID); !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want %v", err, sentinel)
		}
	})
}
