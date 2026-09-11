package authctl

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
)

// fakeNfc records ValidateNfcToken calls so the NFC factor contract can be
// asserted without a live Passport.
type fakeNfc struct {
	calls    int
	last     *gen.DyValidateNfcTokenRequest
	response *gen.DyValidateNfcTokenResponse
	err      error
}

func (f *fakeNfc) ValidateNfcToken(_ context.Context, in *gen.DyValidateNfcTokenRequest, _ ...grpc.CallOption) (*gen.DyValidateNfcTokenResponse, error) {
	f.calls++
	f.last = in
	if f.err != nil {
		return nil, f.err
	}
	return f.response, nil
}

func (f *fakeNfc) ResolveNfcTag(context.Context, *gen.DyResolveNfcTagRequest, ...grpc.CallOption) (*gen.DyResolveNfcTagResponse, error) {
	return &gen.DyResolveNfcTagResponse{}, nil
}

// TestVerifyNfcToken pins the physical-passport login contract: the challenge
// PATCH carries "<tag hardware uid>:<nfc payload>", which is split and handed
// to Passport's DyNfcService. A tag that validates but belongs to a different
// account must not satisfy the challenge, and every failure mode (no client,
// malformed code, RPC error, invalid token) degrades to an invalid password
// instead of panicking.
func TestVerifyNfcToken(t *testing.T) {
	const uid = "04A224B2C33A80"
	const accountID = "11111111-1111-1111-1111-111111111111"
	payload := "solian://phpass?picc_data=" + strings.Repeat("AB", 32) +
		"&e=" + strings.Repeat("CD", 24) + "&cmac=" + strings.Repeat("EF", 8)
	code := uid + ":" + payload

	valid := func(owner string) *gen.DyValidateNfcTokenResponse {
		return &gen.DyValidateNfcTokenResponse{IsValid: true, AccountId: owner, TagId: "tag-1"}
	}
	handlerWith := func(client gen.DyNfcServiceClient) *handler {
		return &handler{d: Deps{Clients: &grpcclient.Clients{Nfc: client}}}
	}

	t.Run("valid tag owned by the account passes the split fields", func(t *testing.T) {
		client := &fakeNfc{response: valid(accountID)}
		if !handlerWith(client).verifyNfcToken(context.Background(), accountID, code) {
			t.Fatal("verifyNfcToken = false, want true")
		}
		if client.last == nil || client.last.GetTagUid() != uid || client.last.GetUidHex() != payload {
			t.Fatalf("request = %+v, want tag_uid=%q uid_hex=%q", client.last, uid, payload)
		}
	})

	t.Run("valid tag owned by another account is rejected", func(t *testing.T) {
		client := &fakeNfc{response: valid("22222222-2222-2222-2222-222222222222")}
		if handlerWith(client).verifyNfcToken(context.Background(), accountID, code) {
			t.Fatal("verifyNfcToken = true for a tag owned by another account")
		}
	})

	t.Run("uppercase account id still matches", func(t *testing.T) {
		client := &fakeNfc{response: valid(strings.ToUpper(accountID))}
		if !handlerWith(client).verifyNfcToken(context.Background(), accountID, code) {
			t.Fatal("verifyNfcToken = false, want true")
		}
	})

	t.Run("invalid token is rejected", func(t *testing.T) {
		client := &fakeNfc{response: &gen.DyValidateNfcTokenResponse{IsValid: false, ErrorCode: "TAG_UNCLAIMED"}}
		if handlerWith(client).verifyNfcToken(context.Background(), accountID, code) {
			t.Fatal("verifyNfcToken = true for an invalid token")
		}
	})

	t.Run("rpc failure degrades to invalid", func(t *testing.T) {
		client := &fakeNfc{err: errors.New("passport down")}
		if handlerWith(client).verifyNfcToken(context.Background(), accountID, code) {
			t.Fatal("verifyNfcToken = true when the RPC failed")
		}
	})

	t.Run("malformed codes never reach Passport", func(t *testing.T) {
		for _, bad := range []string{
			"",
			"short",
			strings.Repeat("A", 64), // long enough but no separator
			":" + payload,
			uid + ":",
		} {
			client := &fakeNfc{response: valid(accountID)}
			if handlerWith(client).verifyNfcToken(context.Background(), accountID, bad) {
				t.Fatalf("verifyNfcToken = true for malformed code %q", bad)
			}
			if client.calls != 0 {
				t.Fatalf("calls = %d for malformed code %q, want 0", client.calls, bad)
			}
		}
	})

	t.Run("unconfigured passport client is not usable", func(t *testing.T) {
		if handlerWith(nil).verifyNfcToken(context.Background(), accountID, code) {
			t.Fatal("verifyNfcToken = true without a configured Nfc client")
		}
	})
}
