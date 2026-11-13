package posts

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// Every post listing outside the timeline module (featured, collections,
// replies, gRPC) hydrates through this path. Clients render the author from
// post.publisher.account, so a bare account_id shows up as "unknown".
func TestHydrateEmbedsPublisherAccount(t *testing.T) {
	db := testNotificationDB(t)
	if err := db.AutoMigrate(
		&store.PostCollectionEntity{},
		&store.PostCollectionItemEntity{},
		&store.PostFeaturedRecordEntity{},
		&store.PostReactionEntity{},
		&store.PostBookmarkEntity{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	accountID := uuid.New()
	publisherID := uuid.New()
	postID := uuid.New()
	reg := module.NewPortRegistry()
	module.Provide[ports.Accounts](reg, ports.PortAccounts, "test", module.KindIntegration,
		&fakeAccountsPort{accounts: map[string]*gen.DyAccount{
			accountID.String(): {Id: accountID.String(), Name: "sheep", Nick: "Sheep"},
		}})
	if err := db.Create(&store.PublisherEntity{
		ID:        publisherID,
		Type:      int(model.PublisherIndividual),
		Name:      "sheep",
		Nick:      "Sheep",
		AccountID: &accountID,
	}).Error; err != nil {
		t.Fatalf("create publisher: %v", err)
	}
	content := "hello"
	now := time.Now().UTC()
	ent := store.PostEntity{
		ID:          postID,
		PublisherID: publisherID,
		Content:     &content,
		Visibility:  int(model.VisibilityPublic),
		PublishedAt: &now,
	}
	if err := db.Create(&ent).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	h := &handler{db: db, ports: reg, log: slog.Default()}
	posts, err := h.hydrate(context.Background(), []store.PostEntity{ent}, nil, false, false)
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("hydrate returned %d posts, want 1", len(posts))
	}
	if posts[0].Publisher == nil {
		t.Fatal("publisher snapshot is missing")
	}
	account, ok := posts[0].Publisher.Account.(map[string]any)
	if !ok {
		t.Fatalf("publisher.account = %#v (%T), want the SnAccount object", posts[0].Publisher.Account, posts[0].Publisher.Account)
	}
	if account["name"] != "sheep" || account["nick"] != "Sheep" {
		t.Errorf("account name/nick = %v/%v, want sheep/Sheep", account["name"], account["nick"])
	}
}

// For multi-account managed publishers (e.g. organizations or teams), posts
// track and expose which specific account authored the post for audit purposes.
func TestHydrateEmbedsPostCreatorAccount(t *testing.T) {
	db := testNotificationDB(t)
	if err := db.AutoMigrate(
		&store.PostCollectionEntity{},
		&store.PostCollectionItemEntity{},
		&store.PostFeaturedRecordEntity{},
		&store.PostReactionEntity{},
		&store.PostBookmarkEntity{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	creatorAccountID := uuid.New()
	publisherID := uuid.New()
	postID := uuid.New()
	reg := module.NewPortRegistry()
	module.Provide[ports.Accounts](reg, ports.PortAccounts, "test", module.KindIntegration,
		&fakeAccountsPort{accounts: map[string]*gen.DyAccount{
			creatorAccountID.String(): {Id: creatorAccountID.String(), Name: "author", Nick: "Author Sheep"},
		}})

	// Multi-account managed publisher (organization), so publisher has no 1:1 account_id
	if err := db.Create(&store.PublisherEntity{
		ID:   publisherID,
		Type: int(model.PublisherOrganizational),
		Name: "team_sphere",
		Nick: "Team Sphere",
	}).Error; err != nil {
		t.Fatalf("create publisher: %v", err)
	}
	content := "multi-account audit post"
	now := time.Now().UTC()
	ent := store.PostEntity{
		ID:          postID,
		PublisherID: publisherID,
		AccountID:   &creatorAccountID,
		Content:     &content,
		Visibility:  int(model.VisibilityPublic),
		PublishedAt: &now,
	}
	if err := db.Create(&ent).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	h := &handler{db: db, ports: reg, log: slog.Default()}
	posts, err := h.hydrate(context.Background(), []store.PostEntity{ent}, nil, false, false)
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("hydrate returned %d posts, want 1", len(posts))
	}
	if posts[0].AccountID == nil || *posts[0].AccountID != creatorAccountID.String() {
		t.Fatalf("post.account_id = %v, want %s", posts[0].AccountID, creatorAccountID.String())
	}
	account, ok := posts[0].Account.(map[string]any)
	if !ok {
		t.Fatalf("post.account = %#v (%T), want SnAccount map", posts[0].Account, posts[0].Account)
	}
	if account["name"] != "author" || account["nick"] != "Author Sheep" {
		t.Errorf("account name/nick = %v/%v, want author/Author Sheep", account["name"], account["nick"])
	}
}
