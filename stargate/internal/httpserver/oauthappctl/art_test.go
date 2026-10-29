package oauthappctl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/ports"
)

// fakeFiles implements ports.Files: resolved ids map to a fixed reference,
// and an id in failures returns an error.
type fakeFiles struct {
	available bool
	resolved  map[string]*model.SnCloudFileReferenceObject
	failures  map[string]error
	calls     []string
}

func (f *fakeFiles) Available() bool { return f.available }

func (f *fakeFiles) Resolve(_ context.Context, fileID string) (*model.SnCloudFileReferenceObject, error) {
	f.calls = append(f.calls, fileID)
	if err, failed := f.failures[fileID]; failed {
		return nil, err
	}
	if file, ok := f.resolved[fileID]; ok {
		return file, nil
	}
	return nil, errors.New("file not found")
}

func artApp() *model.OAuthApp {
	app := newValidTestApp()
	app.Id = "cccccccc-3333-3333-3333-333333333333"
	app.Picture = &model.SnCloudFileReferenceObject{Id: "existing-picture"}
	app.Background = &model.SnCloudFileReferenceObject{Id: "existing-background"}
	return app
}

func TestApplyRequestArtNilLeavesArtUntouched(t *testing.T) {
	files := &fakeFiles{available: true}
	c := &controller{d: Deps{Ports: &ports.Set{Files: files}}}
	app := artApp()

	if err := c.applyRequest(context.Background(), &appRequest{}, app, nil); err != nil {
		t.Fatalf("apply request: %v", err)
	}
	if app.Picture == nil || app.Picture.Id != "existing-picture" {
		t.Fatalf("picture = %+v", app.Picture)
	}
	if app.Background == nil || app.Background.Id != "existing-background" {
		t.Fatalf("background = %+v", app.Background)
	}
	if len(files.calls) != 0 {
		t.Fatalf("resolution calls = %v; want none", files.calls)
	}
}

func TestApplyRequestArtEmptyStringClears(t *testing.T) {
	files := &fakeFiles{available: true}
	c := &controller{d: Deps{Ports: &ports.Set{Files: files}}}
	app := artApp()

	if err := c.applyRequest(context.Background(), &appRequest{PictureId: new("")}, app, nil); err != nil {
		t.Fatalf("apply request: %v", err)
	}
	if app.Picture != nil {
		t.Fatalf("picture = %+v; want cleared", app.Picture)
	}
	// Only the named field changes.
	if app.Background == nil || app.Background.Id != "existing-background" {
		t.Fatalf("background = %+v", app.Background)
	}
	if len(files.calls) != 0 {
		t.Fatalf("resolution calls = %v; want none", files.calls)
	}
}

func TestApplyRequestArtUnavailableProvider(t *testing.T) {
	c := &controller{d: Deps{Ports: &ports.Set{Files: &fakeFiles{available: false}}}}
	app := artApp()

	err := c.applyRequest(context.Background(), &appRequest{PictureId: new("file-1")}, app, nil)
	if !errors.Is(err, errArtUnavailable) {
		t.Fatalf("error = %v; want errArtUnavailable", err)
	}
	if app.Picture == nil || app.Picture.Id != "existing-picture" {
		t.Fatalf("failed resolution dropped existing art: %+v", app.Picture)
	}

	// No ports at all is equally unavailable.
	c = &controller{}
	if err := c.applyRequest(context.Background(), &appRequest{BackgroundId: new("file-1")}, app, nil); !errors.Is(err, errArtUnavailable) {
		t.Fatalf("error with nil ports = %v; want errArtUnavailable", err)
	}
}

func TestApplyRequestArtResolvesReference(t *testing.T) {
	ref := &model.SnCloudFileReferenceObject{Id: "file-1", Name: "art.png", Url: "https://cdn.example/art.png"}
	files := &fakeFiles{available: true, resolved: map[string]*model.SnCloudFileReferenceObject{"file-1": ref}}
	c := &controller{d: Deps{Ports: &ports.Set{Files: files}}}
	app := artApp()

	request := &appRequest{PictureId: new("file-1"), BackgroundId: new("file-1")}
	if err := c.applyRequest(context.Background(), request, app, nil); err != nil {
		t.Fatalf("apply request: %v", err)
	}
	if app.Picture == nil || app.Picture.Id != ref.Id || app.Picture.Url != ref.Url {
		t.Fatalf("picture = %+v", app.Picture)
	}
	if app.Background == nil || app.Background.Id != ref.Id {
		t.Fatalf("background = %+v", app.Background)
	}
	if len(files.calls) != 2 || files.calls[0] != "file-1" || files.calls[1] != "file-1" {
		t.Fatalf("resolution calls = %v", files.calls)
	}
}

// TestRespondErrorArtUnavailable pins the wire code and status a failed art
// resolution produces.
func TestRespondErrorArtUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	respondError(c, errArtUnavailable)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", recorder.Code)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Code != "OAUTH_APP_ART_UNAVAILABLE" {
		t.Fatalf("code = %q", body.Code)
	}
}
