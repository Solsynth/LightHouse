package bangumi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, api *httptest.Server, oauth *httptest.Server) *Client {
	t.Helper()
	return NewClient(Config{
		APIBaseURL:   api.URL,
		AuthorizeURL: oauth.URL + "/oauth/authorize",
		TokenURL:     oauth.URL + "/oauth/access_token",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RedirectURL:  "https://pulsar.example/api/bangumi/oauth/callback",
		UserAgent:    "PulsarTest/1.0",
		Interval:     time.Millisecond,
	})
}

func TestAuthorizeURL(t *testing.T) {
	client := NewClient(Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RedirectURL:  "https://pulsar.example/callback",
	})
	got := client.AuthorizeURL("state-value")
	for _, want := range []string{
		"https://bgm.tv/oauth/authorize",
		"client_id=client-id",
		"response_type=code",
		"redirect_uri=https%3A%2F%2Fpulsar.example%2Fcallback",
		"state=state-value",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("authorize URL %q does not contain %q", got, want)
		}
	}
}

func TestExchangeCodeAndMe(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/me" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-1" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "PulsarTest/1.0" {
			t.Errorf("user-agent = %q", got)
		}
		_ = json.NewEncoder(w).Encode(User{
			ID:       42,
			Username: "alice",
			Nickname: "Alice",
			Avatar:   Images{Large: "https://img.example/a.jpg"},
		})
	}))
	defer api.Close()

	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		checks := map[string]string{
			"grant_type":    "authorization_code",
			"client_id":     "client-id",
			"client_secret": "client-secret",
			"code":          "code-1",
			"redirect_uri":  "https://pulsar.example/api/bangumi/oauth/callback",
		}
		for key, want := range checks {
			if got := r.Form.Get(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		_ = json.NewEncoder(w).Encode(Token{
			AccessToken:  "token-1",
			RefreshToken: "refresh-token-1",
			ExpiresIn:    3600,
			UserID:       42,
		})
	}))
	defer oauth.Close()

	client := testClient(t, api, oauth)
	token, err := client.ExchangeCode(context.Background(), "code-1")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if token.AccessToken != "token-1" || token.RefreshToken != "refresh-token-1" {
		t.Fatalf("token = %+v", token)
	}
	user, err := client.Me(context.Background(), token.AccessToken)
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	if user.Username != "alice" || user.ID != 42 {
		t.Fatalf("user = %+v", user)
	}
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
			if got := r.Header.Get("Authorization"); got != "Bearer token" {
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

	client := testClient(t, api, api)
	page, err := client.ListCollections(context.Background(), "token", "alice", SubjectTypeAnime, 100, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 101 || len(page.Data) != 1 || page.Data[0].SubjectID != 8 {
		t.Fatalf("page = %+v", page)
	}

	rate := 9
	comment := "done"
	err = client.UpsertCollection(context.Background(), "token", 8, CollectionPayload{Rate: &rate, Comment: &comment}, false)
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
		case "/v0/me":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	client := testClient(t, api, api)

	if _, err := client.Me(context.Background(), "token"); err != ErrUnauthorized {
		t.Fatalf("me error = %v", err)
	}
	if _, err := client.GetCollection(context.Background(), "token", "alice", 1); err != ErrNotFound {
		t.Fatalf("collection error = %v", err)
	}
}
