package realtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/store"
)

type stubAccounts struct{ account *gen.DyAccount }

func (s stubAccounts) GetAccount(context.Context, string) (*gen.DyAccount, error) {
	return s.account, nil
}

func (s stubAccounts) GetAccounts(context.Context, []string) ([]*gen.DyAccount, error) {
	return []*gen.DyAccount{s.account}, nil
}

func (s stubAccounts) LookupAccounts(context.Context, []string) ([]*gen.DyAccount, error) {
	return []*gen.DyAccount{s.account}, nil
}

func (s stubAccounts) HasRelationship(context.Context, string, string) (bool, error) {
	return false, nil
}

// Call system messages are pushed as `messages.new`, and clients decode
// sender.account as a full SnAccount. A partial object makes every required
// key it omits null, which crashes the Dart parser on is_superuser.
func TestMemberModelEmbedsFullAccountShape(t *testing.T) {
	accountID := uuid.New()
	s := &server{accounts: stubAccounts{account: &gen.DyAccount{
		Id:       accountID.String(),
		Name:     "sheep",
		Nick:     "Sheep",
		Language: "en",
		Profile:  &gen.DyAccountProfile{Id: uuid.NewString()},
	}}}
	member := s.memberModel(context.Background(), &store.ChatMemberEntity{
		ID:         uuid.New(),
		ChatRoomID: uuid.New(),
		AccountID:  accountID,
	})

	raw, err := json.Marshal(member)
	if err != nil {
		t.Fatalf("marshal member: %v", err)
	}
	var wire struct {
		Account map[string]any `json:"account"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal member: %v", err)
	}
	if wire.Account == nil {
		t.Fatal("account is missing")
	}
	// SnAccount required keys.
	for _, key := range []string{
		"id", "name", "nick", "language", "is_superuser", "profile",
		"created_at", "updated_at",
	} {
		if value, ok := wire.Account[key]; !ok || value == nil {
			t.Errorf("account.%s = %#v (present %v), want a non-null value", key, value, ok)
		}
	}
	if wire.Account["is_superuser"] != false {
		t.Errorf("account.is_superuser = %#v, want false", wire.Account["is_superuser"])
	}
}
