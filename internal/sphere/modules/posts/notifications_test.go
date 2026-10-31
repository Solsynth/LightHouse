package posts

import (
	"context"
	"log/slog"
	"sort"
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

type fakeAccountsPort struct {
	accounts map[string]*gen.DyAccount
}

func (f *fakeAccountsPort) GetAccount(_ context.Context, id string) (*gen.DyAccount, error) {
	if acc, ok := f.accounts[id]; ok {
		return acc, nil
	}
	return nil, nil
}

func (f *fakeAccountsPort) GetAccounts(_ context.Context, ids []string) ([]*gen.DyAccount, error) {
	res := make([]*gen.DyAccount, 0, len(ids))
	for _, id := range ids {
		if acc, ok := f.accounts[id]; ok {
			res = append(res, acc)
		}
	}
	return res, nil
}

func (f *fakeAccountsPort) SearchAccounts(_ context.Context, _ string, _ int) ([]*gen.DyAccount, error) {
	return nil, nil
}

func (f *fakeAccountsPort) HasRelationship(_ context.Context, _, _ string) (bool, error) {
	return false, nil
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
	title, body := chopPostForNotification(post, "en")
	if title != "This is a ..." {
		t.Errorf("title = %q, want 'This is a ...'", title)
	}
	if body != desc {
		t.Errorf("body = %q, want %q", body, desc)
	}

	customTitle := "My Custom Title"
	post.Title = &customTitle
	title, body = chopPostForNotification(post, "en")
	if title != customTitle {
		t.Errorf("title = %q, want %q", title, customTitle)
	}

	mediaOnlyPost := &store.PostEntity{}
	titleEn, _ := chopPostForNotification(mediaOnlyPost, "en")
	if titleEn != "Shared 1 media" {
		t.Errorf("titleEn = %q, want 'Shared 1 media'", titleEn)
	}
	titleZh, _ := chopPostForNotification(mediaOnlyPost, "zh-hans")
	if titleZh != "分享了 1 个媒体文件" {
		t.Errorf("titleZh = %q, want '分享了 1 个媒体文件'", titleZh)
	}
}

func TestNotifySubscribersOnPostPublishLocalized(t *testing.T) {
	db := testNotificationDB(t)
	fakeNotify := &fakeNotifyPort{}
	reg := module.NewPortRegistry()
	module.Provide[ports.Notify](reg, ports.PortNotify, "test", module.KindIntegration, fakeNotify)

	subEn := uuid.New()
	subZh := uuid.New()
	fakeAccounts := &fakeAccountsPort{
		accounts: map[string]*gen.DyAccount{
			subEn.String(): {Id: subEn.String(), Language: "en-US"},
			subZh.String(): {Id: subZh.String(), Language: "zh-CN"},
		},
	}
	module.Provide[ports.Accounts](reg, ports.PortAccounts, "test", module.KindIntegration, fakeAccounts)

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

	blockedSub := uuid.New()

	db.Create(&store.PublisherSubscriptionEntity{
		ID:          uuid.New(),
		PublisherID: publisherID,
		AccountID:   &subEn,
		State:       int(model.SubscriptionAccepted),
		Notify:      true,
	})
	db.Create(&store.PublisherSubscriptionEntity{
		ID:          uuid.New(),
		PublisherID: publisherID,
		AccountID:   &subZh,
		State:       int(model.SubscriptionAccepted),
		Notify:      true,
	})
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

	if len(fakeNotify.pushes) != 2 {
		t.Fatalf("expected 2 push broadcasts (one per language), got %d", len(fakeNotify.pushes))
	}

	pushByLang := make(map[string]recordedPush)
	for _, p := range fakeNotify.pushes {
		if len(p.AccountIDs) != 1 {
			t.Fatalf("expected 1 account per push, got %v", p.AccountIDs)
		}
		if p.AccountIDs[0] == subEn.String() {
			pushByLang["en"] = p
		} else if p.AccountIDs[0] == subZh.String() {
			pushByLang["zh"] = p
		}
	}

	enPush, ok := pushByLang["en"]
	if !ok {
		t.Fatalf("missing en push notification")
	}
	if enPush.Title != "Lighthouse News posted" {
		t.Errorf("enPush.Title = %q, want 'Lighthouse News posted'", enPush.Title)
	}

	zhPush, ok := pushByLang["zh"]
	if !ok {
		t.Fatalf("missing zh push notification")
	}
	if zhPush.Title != "Lighthouse News 发布了帖子" {
		t.Errorf("zhPush.Title = %q, want 'Lighthouse News 发布了帖子'", zhPush.Title)
	}
}

func TestNotifyReplyAuthor(t *testing.T) {
	db := testNotificationDB(t)
	fakeNotify := &fakeNotifyPort{}
	reg := module.NewPortRegistry()
	module.Provide[ports.Notify](reg, ports.PortNotify, "test", module.KindIntegration, fakeNotify)

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

	fakeAccounts := &fakeAccountsPort{
		accounts: map[string]*gen.DyAccount{
			authorAccountID.String(): {Id: authorAccountID.String(), Language: "zh-CN"},
		},
	}
	module.Provide[ports.Accounts](reg, ports.PortAccounts, "test", module.KindIntegration, fakeAccounts)

	h := &handler{
		db:    db,
		ports: reg,
		log:   slog.Default(),
	}

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
	if push.Title != "FriendlyReplier 回复了您的帖子" {
		t.Errorf("push.Title = %q, want 'FriendlyReplier 回复了您的帖子'", push.Title)
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

	fakeAccounts := &fakeAccountsPort{
		accounts: map[string]*gen.DyAccount{
			authorAccountID.String(): {Id: authorAccountID.String(), Language: "en"},
		},
	}
	module.Provide[ports.Accounts](reg, ports.PortAccounts, "test", module.KindIntegration, fakeAccounts)

	h := &handler{
		db:    db,
		ports: reg,
		log:   slog.Default(),
	}

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
	if push.Body != "Alice reacted to \"Interesting Article\" with ❤️" {
		t.Errorf("push.Body = %q, want 'Alice reacted to \"Interesting Article\" with ❤️'", push.Body)
	}
	if push.Meta["notification_type"] != "reaction" || push.Meta["reaction"] != "❤️" {
		t.Errorf("push.Meta = %v, want reaction ❤️", push.Meta)
	}
	if len(push.AccountIDs) != 1 || push.AccountIDs[0] != authorAccountID.String() {
		t.Errorf("recipient = %v, want [%s]", push.AccountIDs, authorAccountID)
	}
}

func TestNotifyReactionToAuthorZh(t *testing.T) {
	db := testNotificationDB(t)
	fakeNotify := &fakeNotifyPort{}
	reg := module.NewPortRegistry()
	module.Provide[ports.Notify](reg, ports.PortNotify, "test", module.KindIntegration, fakeNotify)

	authorAccountID := uuid.New()
	authorPubID := uuid.New()
	db.Create(&store.PublisherEntity{
		ID:        authorPubID,
		AccountID: &authorAccountID,
		Name:      "author",
	})
	title := "有趣的文章"
	post := &store.PostEntity{
		ID:          uuid.New(),
		PublisherID: authorPubID,
		Title:       &title,
	}
	db.Create(post)

	fakeAccounts := &fakeAccountsPort{
		accounts: map[string]*gen.DyAccount{
			authorAccountID.String(): {Id: authorAccountID.String(), Language: "zh-Hans-CN"},
		},
	}
	module.Provide[ports.Accounts](reg, ports.PortAccounts, "test", module.KindIntegration, fakeAccounts)

	h := &handler{
		db:    db,
		ports: reg,
		log:   slog.Default(),
	}

	actorID := uuid.New()
	actor := &gen.DyAccount{
		Id:   actorID.String(),
		Nick: "鲍勃",
	}

	h.notifyReaction(context.Background(), post, actor, "👍", 0)

	if len(fakeNotify.pushes) != 1 {
		t.Fatalf("expected 1 reaction push, got %d", len(fakeNotify.pushes))
	}
	push := fakeNotify.pushes[0]
	if push.Title != "鲍勃 对您的帖子做出了反应" {
		t.Errorf("push.Title = %q, want '鲍勃 对您的帖子做出了反应'", push.Title)
	}
	if push.Body != "鲍勃 用 👍 对 \"有趣的文章\" 做出了反应" {
		t.Errorf("push.Body = %q, want '鲍勃 用 👍 对 \"有趣的文章\" 做出了反应'", push.Body)
	}
}

// Silence unused sort import if needed
var _ = sort.Strings
