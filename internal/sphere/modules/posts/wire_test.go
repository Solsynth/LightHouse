package posts

import (
	"encoding/json"
	"testing"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
)

// The Island composer sends post bodies with snake_case keys (see
// ComposeLogic._saveCloudDraft and the sphere SDK). These payloads are copied
// from the client so a DTO tag drift fails here instead of silently dropping
// embed views, scheduling or collection membership.
func TestClientPostPayloadBinds(t *testing.T) {
	const payload = `{
		"title": "Probe",
		"description": "",
		"content": "hello",
		"visibility": 0,
		"type": 0,
		"attachments": [],
		"tags": [],
		"categories": [],
		"realm_id": "11111111-1111-1111-1111-111111111111",
		"thumbnail_id": "file-thumb",
		"embed_view": {"uri": "https://example.com", "aspect_ratio": 1.5, "renderer": 0},
		"embeds": [{"type": "link", "url": "https://example.com"}],
		"collection_ids": ["22222222-2222-2222-2222-222222222222"],
		"drafted_at": "2026-10-30T12:00:00Z",
		"published_at": null,
		"replied_post_id": "33333333-3333-3333-3333-333333333333",
		"auto_chain": true,
		"location_name": "Shanghai",
		"location_address": "Somewhere",
		"location_wkt": "POINT(0 0)"
	}`

	var request postRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if request.EmbedView == nil {
		t.Error("embed_view was not bound")
	}
	if request.DraftedAt == nil {
		t.Error("drafted_at was not bound")
	}
	if request.RealmID == nil {
		t.Error("realm_id was not bound")
	}
	if request.ThumbnailID == nil || *request.ThumbnailID != "file-thumb" {
		t.Errorf("thumbnail_id = %v, want file-thumb", request.ThumbnailID)
	}
	if len(request.Embeds) != 1 {
		t.Errorf("embeds = %d, want 1", len(request.Embeds))
	}
	if len(request.CollectionIDs) != 1 {
		t.Errorf("collection_ids = %d, want 1", len(request.CollectionIDs))
	}
	if request.RepliedPostID == nil {
		t.Error("replied_post_id was not bound")
	}
	if request.AutoChain == nil || !*request.AutoChain {
		t.Error("auto_chain was not bound")
	}
	if request.LocationName == nil || *request.LocationName != "Shanghai" {
		t.Errorf("location_name = %v, want Shanghai", request.LocationName)
	}
}

func TestClientCollectionPayloadsBind(t *testing.T) {
	var create createCollectionRequest
	if err := json.Unmarshal([]byte(
		`{"slug":"probe","background_id":"file-bg","icon_id":"file-icon"}`,
	), &create); err != nil {
		t.Fatalf("unmarshal create: %v", err)
	}
	if create.BackgroundID == nil || *create.BackgroundID != "file-bg" {
		t.Errorf("background_id = %v, want file-bg", create.BackgroundID)
	}
	if create.IconID == nil || *create.IconID != "file-icon" {
		t.Errorf("icon_id = %v, want file-icon", create.IconID)
	}

	var batch batchCollectionPostsRequest
	if err := json.Unmarshal([]byte(
		`{"post_ids":["22222222-2222-2222-2222-222222222222"]}`,
	), &batch); err != nil {
		t.Fatalf("unmarshal batch: %v", err)
	}
	if len(batch.PostIDs) != 1 {
		t.Errorf("post_ids = %d, want 1", len(batch.PostIDs))
	}

	var add addCollectionPostRequest
	if err := json.Unmarshal([]byte(
		`{"post_id":"22222222-2222-2222-2222-222222222222","order":3}`,
	), &add); err != nil {
		t.Fatalf("unmarshal add: %v", err)
	}
	if add.PostID.String() != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("post_id = %v", add.PostID)
	}
}

func TestThreadedReplyNodeUsesParentId(t *testing.T) {
	var node model.ThreadedReplyNode
	if err := json.Unmarshal([]byte(`{"parent_id":"abc","depth":1}`), &node); err != nil {
		t.Fatalf("unmarshal node: %v", err)
	}
	if node.ParentID == nil || *node.ParentID != "abc" {
		t.Errorf("parent_id = %v, want abc", node.ParentID)
	}
}
