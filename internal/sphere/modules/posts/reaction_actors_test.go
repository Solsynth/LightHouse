package posts

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// The reaction list renders the actor from reaction.account.nick (or
// reaction.publisher.effectiveName for fediverse actors). A bare account_id
// makes every row show as "unknown".
func TestHydrateReactionActors(t *testing.T) {
	db := testNotificationDB(t)
	accountID := uuid.New()
	publisherID := uuid.New()
	reg := module.NewPortRegistry()
	module.Provide[ports.Accounts](reg, ports.PortAccounts, "test", module.KindIntegration,
		&fakeAccountsPort{accounts: map[string]*gen.DyAccount{
			accountID.String(): {Id: accountID.String(), Name: "sheep", Nick: "Sheep"},
		}})
	db.Create(&store.PublisherEntity{
		ID:   publisherID,
		Type: int(model.PublisherFediverse),
		Name: "actor@example.com",
		Nick: "Remote Actor",
	})

	h := &handler{db: db, ports: reg, log: slog.Default()}
	accountIDString := accountID.String()
	publisherIDString := publisherID.String()
	reactions := []model.PostReaction{
		{ID: "local", AccountID: &accountIDString},
		{ID: "remote", PublisherID: &publisherIDString},
	}
	h.hydrateReactionActors(context.Background(), reactions)

	account, ok := reactions[0].Account.(map[string]any)
	if !ok {
		t.Fatalf("local reaction account = %#v (%T), want the SnAccount object", reactions[0].Account, reactions[0].Account)
	}
	if account["name"] != "sheep" || account["nick"] != "Sheep" {
		t.Errorf("account name/nick = %v/%v, want sheep/Sheep", account["name"], account["nick"])
	}
	if _, ok := account["is_superuser"]; !ok {
		t.Error("account.is_superuser is missing; clients require the full SnAccount shape")
	}

	if reactions[1].Publisher == nil {
		t.Fatal("remote reaction publisher is missing")
	}
	if reactions[1].Publisher.Nick != "Remote Actor" {
		t.Errorf("publisher nick = %q, want Remote Actor", reactions[1].Publisher.Nick)
	}
}
