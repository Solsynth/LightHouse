package bangumi

import (
	"testing"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

func TestSubjectTypeAndDomainRoundTrip(t *testing.T) {
	for _, domain := range []string{model.DomainBook, model.DomainAnime, model.DomainGaming, model.DomainVideo} {
		subjectType, ok := SubjectTypeForDomain(domain)
		if !ok {
			t.Fatalf("SubjectTypeForDomain(%q) not found", domain)
		}
		if got := DomainForSubjectType(subjectType); got != domain {
			t.Fatalf("round trip %q -> %d -> %q", domain, subjectType, got)
		}
	}
	if _, ok := SubjectTypeForDomain(model.DomainMusic); ok {
		t.Fatal("music must not map to a Bangumi subject type")
	}
}

func TestCollectionStatusRoundTrip(t *testing.T) {
	cases := []struct {
		subjectType int
		status      string
	}{
		{CollectionWish, model.StatusPlanToWatch},
		{CollectionDone, model.StatusCompleted},
		{CollectionDoing, model.StatusWatching},
		{CollectionOnHold, model.StatusOnHold},
		{CollectionDropped, model.StatusDropped},
	}
	for _, tc := range cases {
		status, ok := StatusFromCollectionType(tc.subjectType)
		if !ok || status != tc.status {
			t.Fatalf("StatusFromCollectionType(%d) = %q, %v", tc.subjectType, status, ok)
		}
		if got := CollectionTypeFromStatus(tc.status); got != tc.subjectType {
			t.Fatalf("CollectionTypeFromStatus(%q) = %d", tc.status, got)
		}
	}
}

func TestFingerprintsTrackAgreement(t *testing.T) {
	rating := 8
	note := "rewatch"
	progress := 3
	local := LocalState{Status: model.StatusWatching, Rating: &rating, Note: &note, Progress: &progress}
	if LocalFingerprint(local) != LocalFingerprint(local) {
		t.Fatal("local fingerprint is not stable")
	}
	remote := RemoteState{Type: CollectionDoing, Rate: 8, Comment: "rewatch", EpStatus: 3}
	if RemoteFingerprint(remote) != RemoteFingerprint(remote) {
		t.Fatal("remote fingerprint is not stable")
	}

	empty := NotePointer("")
	if LocalFingerprint(LocalState{Note: empty}) == LocalFingerprint(LocalState{}) {
		t.Fatal("an explicitly cleared note must differ from an absent note")
	}
	if RemoteFingerprint(RemoteState{Rate: 0}) != RemoteFingerprint(RemoteState{}) {
		t.Fatal("a zero remote rate means no rating on both sides")
	}
}

func TestApplyRemoteAndPayload(t *testing.T) {
	item := &model.CollectionItem{}
	ApplyRemote(item, RemoteState{Type: CollectionDone, Rate: 9, Comment: "great", EpStatus: 12})
	if item.Status == nil || *item.Status != model.StatusCompleted {
		t.Fatalf("status = %v", item.Status)
	}
	if item.Rating == nil || *item.Rating != 9 {
		t.Fatalf("rating = %v", item.Rating)
	}
	if item.Note == nil || *item.Note != "great" {
		t.Fatalf("note = %v", item.Note)
	}
	if item.Progress == nil || *item.Progress != 12 {
		t.Fatalf("progress = %v", item.Progress)
	}

	payload := PayloadFromLocal(LocalState{Status: model.StatusDropped, Rating: item.Rating, Note: item.Note, Progress: item.Progress}, model.DomainBook, false)
	if payload.Type == nil || *payload.Type != CollectionDropped {
		t.Fatalf("payload type = %v", payload.Type)
	}
	if payload.EpStatus == nil || *payload.EpStatus != 12 {
		t.Fatalf("book progress = %v", payload.EpStatus)
	}
	anime := PayloadFromLocal(LocalState{Progress: item.Progress}, model.DomainAnime, false)
	if anime.EpStatus != nil {
		t.Fatal("non-book progress must not be written directly")
	}
}

// NotePointer keeps the test readable without another helper package.
func NotePointer(value string) *string { return &value }
