package bangumi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testClient(t *testing.T, api *httptest.Server) *Client {
	t.Helper()
	return NewClient(Config{
		APIBaseURL: api.URL,
		UserAgent:  "PulsarTest/1.0",
		Interval:   time.Millisecond,
		HTTPClient: api.Client(),
	})
}

func TestListCollectionsPagingAndUpdate(t *testing.T) {
	var patched CollectionPayload
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/users/alice/collections":
			if got := r.URL.Query().Get("subject_type"); got != "2" {
				t.Errorf("subject_type = %q", got)
			}
			if got := r.URL.Query().Get("offset"); got != "100" {
				t.Errorf("offset = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer access-value" {
				t.Errorf("authorization = %q", got)
			}
			_ = json.NewEncoder(w).Encode(CollectionPage{
				Total:  101,
				Limit:  100,
				Offset: 100,
				Data: []SubjectCollection{{
					SubjectID: 8,
					Type:      CollectionDoing,
					Rate:      8,
					Comment:   "watching",
					Subject:   SlimSubject{ID: 8, Name: "Kanon"},
				}},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/users/-/collections/8":
			if got := r.Header.Get("Authorization"); got != "Bearer access-value" {
				t.Errorf("authorization = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&patched); err != nil {
				t.Errorf("decode patch: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	client := testClient(t, api)
	page, err := client.ListCollections(context.Background(), "access-value", "alice", SubjectTypeAnime, 100, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 101 || len(page.Data) != 1 || page.Data[0].SubjectID != 8 {
		t.Fatalf("page = %+v", page)
	}

	rate := 9
	comment := "done"
	err = client.UpsertCollection(context.Background(), "access-value", 8, CollectionPayload{Rate: &rate, Comment: &comment}, false)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if patched.Rate == nil || *patched.Rate != 9 {
		t.Fatalf("patched rate = %v", patched.Rate)
	}
	if patched.Comment == nil || *patched.Comment != "done" {
		t.Fatalf("patched comment = %v", patched.Comment)
	}
}

func TestErrorsMapToSentinels(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/users/-/collections/1":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	client := testClient(t, api)

	if _, err := client.GetCollection(context.Background(), "access-value", "alice", 1); err != ErrNotFound {
		t.Fatalf("collection error = %v", err)
	}
	if err := client.UpsertCollection(context.Background(), "access-value", 1, CollectionPayload{}, false); err != ErrUnauthorized {
		t.Fatalf("update error = %v", err)
	}
}
