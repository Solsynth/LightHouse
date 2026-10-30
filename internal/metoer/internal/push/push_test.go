package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/sideshow/apns2"

	"src.solsynth.dev/sosys/metoer/internal/model"
)

// TestShouldQueueSopReplay pins the C# signature:
// !isSavable && any Sop subscription.
func TestShouldQueueSopReplay(t *testing.T) {
	now := model.NowTime()
	sop := &model.PushSubscription{ModelBase: model.ModelBase{Id: uuid.New(), CreatedAt: now, UpdatedAt: now}, Provider: model.PushProviderSop}
	google := &model.PushSubscription{ModelBase: model.ModelBase{Id: uuid.New(), CreatedAt: now, UpdatedAt: now}, Provider: model.PushProviderGoogle}

	if !ShouldQueueSopReplay(false, []*model.PushSubscription{sop}) {
		t.Fatal("non-savable with Sop sub must queue replay")
	}
	if ShouldQueueSopReplay(true, []*model.PushSubscription{sop}) {
		t.Fatal("savable must not queue replay")
	}
	if ShouldQueueSopReplay(false, []*model.PushSubscription{google}) {
		t.Fatal("non-savable without Sop sub must not queue replay")
	}
	if ShouldQueueSopReplay(false, nil) {
		t.Fatal("no subscriptions must not queue replay")
	}
}

// TestGetSubscriptionPriority pins the C# provider priority table.
func TestGetSubscriptionPriority(t *testing.T) {
	now := model.NowTime()
	base := func(provider model.PushProvider) *model.PushSubscription {
		return &model.PushSubscription{ModelBase: model.ModelBase{Id: uuid.New(), CreatedAt: now, UpdatedAt: now}, Provider: provider}
	}
	voip := &model.Notification{PushType: new("VoIP")}
	alert := &model.Notification{PushType: new("Alert")}

	if p := getSubscriptionPriority(base(model.PushProviderSop), map[string]struct{}{}, alert); p != 0 {
		t.Fatalf("disconnected sop priority = %d, want 0", p)
	}
	if p := getSubscriptionPriority(base(model.PushProviderSop), map[string]struct{}{"dev": {}}, alert); p != 0 {
		t.Fatalf("unrelated connected sop priority = %d, want 0", p)
	}
	sop := base(model.PushProviderSop)
	sop.DeviceId = "dev:sop"
	if p := getSubscriptionPriority(sop, map[string]struct{}{"dev": {}}, alert); p != 5 {
		t.Fatalf("connected sop priority = %d, want 5", p)
	}
	if p := getSubscriptionPriority(base(model.PushProviderGoogle), nil, alert); p != 2 {
		t.Fatalf("google priority = %d, want 2", p)
	}
	if p := getSubscriptionPriority(base(model.PushProviderUnifiedPush), nil, alert); p != 1 {
		t.Fatalf("unified push priority = %d, want 1", p)
	}
	if p := getSubscriptionPriority(base(model.PushProviderApple), nil, alert); p != 4 {
		t.Fatalf("apple alert priority = %d, want 4", p)
	}
	if p := getSubscriptionPriority(base(model.PushProviderApple), nil, voip); p != 3 {
		t.Fatalf("apple voip priority = %d, want 3", p)
	}
	if p := getSubscriptionPriority(base(model.PushProviderAppk), nil, voip); p != 4 {
		t.Fatalf("appk voip priority = %d, want 4", p)
	}
	if p := getSubscriptionPriority(base(model.PushProviderAppk), nil, alert); p != 0 {
		t.Fatalf("appk alert priority = %d, want 0", p)
	}
}

// TestFcmPayload pins the FCM v1 messages:send body.
func TestFcmPayload(t *testing.T) {
	payload := fcmPayload("tok", "Title", "sub\ncontent")
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"message":{"notification":{"body":"sub\ncontent","title":"Title"},"token":"tok"}}`
	if string(data) != want {
		t.Fatalf("fcm payload mismatch:\n got %s\nwant %s", data, want)
	}
}

// TestApplePayload pins the C# APNs payload dict: topic/type/aps/meta, with
// sound null when priority < 5.
func TestApplePayload(t *testing.T) {
	meta := map[string]any{"k": "v"}
	payload := applePayload("dev.solsynth.solian", "t", map[string]any{"title": "x"}, meta, 4)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"aps":{"alert":{"title":"x"},"mutable-content":1,"sound":null},"meta":{"k":"v"},"topic":"dev.solsynth.solian","type":"t"}`
	if string(data) != want {
		t.Fatalf("apple payload mismatch:\n got %s\nwant %s", data, want)
	}

	payloadHigh := applePayload("dev.solsynth.solian", "t", map[string]any{}, meta, 10)
	raw, _ := json.Marshal(payloadHigh)
	if !strings.Contains(string(raw), `"sound":"default"`) {
		t.Fatalf("priority >= 5 must set sound default: %s", raw)
	}
}

// TestAppkPayload pins the VoIP payload: meta spread + content-available +
// uuid (TryAdd semantics).
func TestAppkPayload(t *testing.T) {
	id := uuid.New()
	n := &model.Notification{
		ModelBase: model.ModelBase{Id: id},
		Meta:      map[string]any{"custom": 1},
	}
	payload := appkPayload(n)
	if payload["uuid"] != id.String() {
		t.Fatalf("uuid missing from appk payload: %v", payload)
	}
	aps, ok := payload["aps"].(map[string]any)
	if !ok || aps["content-available"] != 1 {
		t.Fatalf("aps content-available missing: %v", payload)
	}
	if payload["custom"] != 1 {
		t.Fatalf("meta not spread: %v", payload)
	}

	// TryAdd: an existing uuid in meta wins.
	n2 := &model.Notification{ModelBase: model.ModelBase{Id: id}, Meta: map[string]any{"uuid": "keep-me"}}
	payload2 := appkPayload(n2)
	if payload2["uuid"] != "keep-me" {
		t.Fatalf("existing uuid must win: %v", payload2)
	}
}

// TestNormalizeSopDeviceId pins the ":sop" suffix stripping.
func TestNormalizeSopDeviceId(t *testing.T) {
	if got := NormalizeSopDeviceId("abc:sop"); got != "abc" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeSopDeviceId("abc"); got != "abc" {
		t.Fatalf("got %q", got)
	}
}

// testP8Key returns a freshly generated P-256 p8 PEM key (the format
// apns2's token.AuthKeyFromBytes parses).
func testP8Key(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// TestApnSenderEnvironment pins the APNs host selection: apns2 defaults to
// the development host, so production MUST be selected explicitly — pushing
// production tokens to the sandbox returns 400 BadDeviceToken.
func TestApnSenderEnvironment(t *testing.T) {
	key := testP8Key(t)

	prod, err := NewApnSender(key, "k", "t", "dev.solsynth.solian", true)
	if err != nil {
		t.Fatal(err)
	}
	if prod.client.Host != apns2.HostProduction {
		t.Fatalf("production sender host = %q, want %q", prod.client.Host, apns2.HostProduction)
	}

	dev, err := NewApnSender(key, "k", "t", "dev.solsynth.solian", false)
	if err != nil {
		t.Fatal(err)
	}
	if dev.client.Host != apns2.HostDevelopment {
		t.Fatalf("development sender host = %q, want %q", dev.client.Host, apns2.HostDevelopment)
	}
}

// TestTopicsLookup pins the case-insensitive topic lookup: configs write
// "alert"/"voip", the C# lookups use "Alert"/"VoIP".
func TestTopicsLookup(t *testing.T) {
	topics := map[string]string{"alert": "dev.solsynth.solian", "voip": "dev.solsynth.solian.voip"}
	if got := topicsLookup(topics, "Alert"); got != "dev.solsynth.solian" {
		t.Fatalf("Alert lookup = %q", got)
	}
	if got := topicsLookup(topics, "VoIP"); got != "dev.solsynth.solian.voip" {
		t.Fatalf("VoIP lookup = %q", got)
	}
	if got := topicsLookup(topics, "alert"); got != "dev.solsynth.solian" {
		t.Fatalf("exact alert lookup = %q", got)
	}
	if got := topicsLookup(map[string]string{}, "Alert"); got != "" {
		t.Fatalf("missing lookup = %q", got)
	}
}
