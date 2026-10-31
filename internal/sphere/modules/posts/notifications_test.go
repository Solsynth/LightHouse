package posts

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

type recordedPush struct {
	AccountIDs []string
	Title      string
	Body       string
	Meta       map[string]string
}

type fakeNotifyPort struct {
	pushes []recordedPush
}

func (f *fakeNotifyPort) SendPush(_ context.Context, accountID, title, body string, meta map[string]string) error {
	f.pushes = append(f.pushes, recordedPush{
		AccountIDs: []string{accountID},
		Title:      title,
		Body:       body,
		Meta:       meta,
	})
	return nil
}

func (f *fakeNotifyPort) SendPushToAccounts(_ context.Context, accountIDs []string, title, body string, meta map[string]string) error {
	f.pushes = append(f.pushes, recordedPush{
		AccountIDs: accountIDs,
		Title:      title,
		Body:       body,
		Meta:       meta,
	})
	return nil
}

func testNotificationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	if err := db.AutoMigrate(
		&store.PostEntity{},
		&store.PublisherEntity{},
		&store.PublisherSubscriptionEntity{},
		&store.PostCategorySubscriptionEntity{},
		&store.PostWatchPreferenceEntity{},
		&store.PostCollectionItemEntity{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	_ = db.Exec("CREATE TABLE IF NOT EXISTS post_category_links (posts_id text, categories_id text)").Error
	_ = db.Exec("CREATE TABLE IF NOT EXISTS post_tag_links (posts_id text, tags_id text)").Error
	return db
}

func TestChopPostForNotification(t *testing.T) {
	desc := "This is a brief description of the post."
	post := &store.PostEntity{
		Description: &desc,
	}
	title, body := chopPostForNotification(post)
	if title != "This is a ..." {
		t.Errorf("title = %q, want 'This is a ...'", title)
	}
	if body != desc {
		t.Errorf("body = %q, want %q", body, desc)
	}

	customTitle := "My Custom Title"
	post.Title = &customTitle
	title, body = chopPostForNotification(post)
	if title != customTitle {
		t.Errorf("title = %q, want %q", title, customTitle)
	}
}

func TestNotifySubscribersOnPostPublish(t *testing.T) {
	db := testNotificationDB(t)
	fakeNotify := &fakeNotifyPort{}
	reg := module.NewPortRegistry()
	module.Provide[ports.Notify](reg, ports.PortNotify, "test", module.KindIntegration, fakeNotify)

	h := &handler{
		db:    db,
		ports: reg,
		log:   slog.Default(),
	}

	publisherID := uuid.New()
	pubOwnerID := uuid.New()
	nick := "Lighthouse News"
	db.Create(&store.PublisherEntity{
		ID:        publisherID,
		AccountID: &pubOwnerID,
		Name:      "news",
		Nick:      nick,
	})

	sub1 := uuid.New()
	sub2 := uuid.New()
	blockedSub := uuid.New()

	db.Create(&store.PublisherSubscriptionEntity{
		ID:          uuid.New(),
		PublisherID: publisherID,
		AccountID:   &sub1,
		State:       int(model.SubscriptionAccepted),
		Notify:      true,
	})
	db.Create(&store.PublisherSubscriptionEntity{
		ID:          uuid.New(),
		PublisherID: publisherID,
		AccountID:   &sub2,
		State:       int(model.SubscriptionAccepted),
		Notify:      true,
	})
	// Notify = false subscriber: should not be notified
	db.Create(&store.PublisherSubscriptionEntity{
		ID:          uuid.New(),
		PublisherID: publisherID,
		AccountID:   &blockedSub,
		State:       int(model.SubscriptionAccepted),
		Notify:      false,
	})

	postTitle := "Big Announcement"
	post := &store.PostEntity{
		ID:          uuid.New(),
		PublisherID: publisherID,
		Title:       &postTitle,
		Visibility:  int(model.VisibilityPublic),
	}
	db.Create(post)

	h.notifyPostAudience(context.Background(), post, nil, "created", pubOwnerID)

	if len(fakeNotify.pushes) != 1 {
		t.Fatalf("expected 1 push broadcast, got %d", len(fakeNotify.pushes))
	}
	push := fakeNotify.pushes[0]
	if push.Title != "Lighthouse News" {
		t.Errorf("push.Title = %q, want 'Lighthouse News'", push.Title)
	}
	if push.Meta["notification_type"] != "subscription" {
		t.Errorf("notification_type = %q, want 'subscription'", push.Meta["notification_type"])
	}
	if len(push.AccountIDs) != 2 {
		t.Fatalf("expected 2 subscribers, got %d (%v)", len(push.AccountIDs), push.AccountIDs)
	}
}

func TestNotifyReplyAuthor(t *testing.T) {
	db := testNotificationDB(t)
	fakeNotify := &fakeNotifyPort{}
	reg := module.NewPortRegistry()
	module.Provide[ports.Notify](reg, ports.PortNotify, "test", module.KindIntegration, fakeNotify)

	h := &handler{
		db:    db,
		ports: reg,
		log:   slog.Default(),
	}

	authorAccountID := uuid.New()
	authorPubID := uuid.New()
	db.Create(&store.PublisherEntity{
		ID:        authorPubID,
		AccountID: &authorAccountID,
		Name:      "parent_author",
	})
	parentPostID := uuid.New()
	db.Create(&store.PostEntity{
		ID:          parentPostID,
		PublisherID: authorPubID,
	})

	replierAccountID := uuid.New()
	replierPubID := uuid.New()
	nick := "FriendlyReplier"
	db.Create(&store.PublisherEntity{
		ID:        replierPubID,
		AccountID: &replierAccountID,
		Name:      "replier",
		Nick:      nick,
	})

	replyContent := "Great post!"
	replyPost := &store.PostEntity{
		ID:            uuid.New(),
		PublisherID:   replierPubID,
		Content:       &replyContent,
		RepliedPostID: &parentPostID,
		Visibility:    int(model.VisibilityPublic),
	}
	db.Create(replyPost)

	h.notifyPostAudience(context.Background(), replyPost, nil, "created", replierAccountID)

	if len(fakeNotify.pushes) != 1 {
		t.Fatalf("expected 1 reply push, got %d", len(fakeNotify.pushes))
	}
	push := fakeNotify.pushes[0]
	if push.Title != "FriendlyReplier replied to your post" {
		t.Errorf("push.Title = %q, want 'FriendlyReplier replied to your post'", push.Title)
	}
	if push.Meta["notification_type"] != "post_watch" || push.Meta["watch_event_type"] != "reply" {
		t.Errorf("push.Meta = %v, want post_watch reply", push.Meta)
	}
	if len(push.AccountIDs) != 1 || push.AccountIDs[0] != authorAccountID.String() {
		t.Errorf("recipient = %v, want [%s]", push.AccountIDs, authorAccountID)
	}
}

func TestNotifyReactionToAuthor(t *testing.T) {
	db := testNotificationDB(t)
	fakeNotify := &fakeNotifyPort{}
	reg := module.NewPortRegistry()
	module.Provide[ports.Notify](reg, ports.PortNotify, "test", module.KindIntegration, fakeNotify)

	h := &handler{
		db:    db,
		ports: reg,
		log:   slog.Default(),
	}

	authorAccountID := uuid.New()
	authorPubID := uuid.New()
	db.Create(&store.PublisherEntity{
		ID:        authorPubID,
		AccountID: &authorAccountID,
		Name:      "author",
	})
	title := "Interesting Article"
	post := &store.PostEntity{
		ID:          uuid.New(),
		PublisherID: authorPubID,
		Title:       &title,
	}
	db.Create(post)

	actorID := uuid.New()
	actor := &gen.DyAccount{
		Id:   actorID.String(),
		Nick: "Alice",
	}

	h.notifyReaction(context.Background(), post, actor, "❤️", 0)

	if len(fakeNotify.pushes) != 1 {
		t.Fatalf("expected 1 reaction push, got %d", len(fakeNotify.pushes))
	}
	push := fakeNotify.pushes[0]
	if push.Title != "Alice reacted to your post" {
		t.Errorf("push.Title = %q, want 'Alice reacted to your post'", push.Title)
	}
	if push.Body != "Alice reacted with ❤️ to \"Interesting Article\"" {
		t.Errorf("push.Body = %q, want 'Alice reacted with ❤️ to \"Interesting Article\"'", push.Body)
	}
	if push.Meta["notification_type"] != "reaction" || push.Meta["reaction"] != "❤️" {
		t.Errorf("push.Meta = %v, want reaction ❤️", push.Meta)
	}
	if len(push.AccountIDs) != 1 || push.AccountIDs[0] != authorAccountID.String() {
		t.Errorf("recipient = %v, want [%s]", push.AccountIDs, authorAccountID)
	}
}
