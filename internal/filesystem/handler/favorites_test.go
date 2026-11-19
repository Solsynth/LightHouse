package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/filesystem/config"
	"src.solsynth.dev/sosys/filesystem/database"
	"src.solsynth.dev/sosys/filesystem/service"
)

func favoritesTestRouter(t *testing.T, db *gorm.DB, accountID uuid.UUID) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	files := service.NewFileService(&database.DB{DB: db}, nil)
	r := gin.New()
	r.Use(testAuthMiddleware(accountID))
	RegisterRoutes(r, Deps{
		Config: &config.Config{},
		Files:  files,
		Tasks:  service.NewTaskService(&database.DB{DB: db}),
		Quota:  service.NewQuotaService(&database.DB{DB: db}),
	})
	return r
}

func favoritesTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return openHandlerTestDB(t,
		&database.CloudFile{}, &database.FileObject{}, &database.FilePool{},
		&database.FilePermission{}, &database.CloudFileFavorite{},
	)
}

func TestFavoriteFileRoundTripsThroughListAndUnfavorite(t *testing.T) {
	db := favoritesTestDB(t)
	accountID := uuid.New()
	file := database.CloudFile{ID: database.NewID(), Name: "roadmap.pdf", AccountID: accountID, Indexed: true}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	r := favoritesTestRouter(t, db, accountID)

	favorite := httptest.NewRecorder()
	r.ServeHTTP(favorite, httptest.NewRequest(http.MethodPost, "/api/files/"+file.ID+"/favorite", nil))
	if favorite.Code != http.StatusOK {
		t.Fatalf("favorite status = %d, want %d, body = %s", favorite.Code, http.StatusOK, favorite.Body.String())
	}

	// Favoriting twice keeps a single row; the endpoint is idempotent.
	again := httptest.NewRecorder()
	r.ServeHTTP(again, httptest.NewRequest(http.MethodPost, "/api/files/"+file.ID+"/favorite", nil))
	if again.Code != http.StatusOK {
		t.Fatalf("second favorite status = %d, body = %s", again.Code, again.Body.String())
	}
	var rows int64
	if err := db.Model(&database.CloudFileFavorite{}).Where("account_id = ?", accountID).Count(&rows).Error; err != nil {
		t.Fatalf("count favorites: %v", err)
	}
	if rows != 1 {
		t.Fatalf("favorite rows = %d, want 1", rows)
	}

	listed := httptest.NewRecorder()
	r.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/files/favorites", nil))
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listed.Code, listed.Body.String())
	}
	var items []database.CloudFile
	if err := json.Unmarshal(listed.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode favorites: %v", err)
	}
	if len(items) != 1 || items[0].ID != file.ID {
		t.Fatalf("favorites = %+v, want the single file %s", items, file.ID)
	}
	if got := listed.Header().Get("X-Total"); got != "1" {
		t.Fatalf("X-Total = %q, want 1", got)
	}

	removed := httptest.NewRecorder()
	r.ServeHTTP(removed, httptest.NewRequest(http.MethodDelete, "/api/files/"+file.ID+"/favorite", nil))
	if removed.Code != http.StatusOK {
		t.Fatalf("unfavorite status = %d, body = %s", removed.Code, removed.Body.String())
	}

	empty := httptest.NewRecorder()
	r.ServeHTTP(empty, httptest.NewRequest(http.MethodGet, "/api/files/favorites", nil))
	if empty.Code != http.StatusOK {
		t.Fatalf("list after unfavorite status = %d, body = %s", empty.Code, empty.Body.String())
	}
	if err := json.Unmarshal(empty.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode favorites after unfavorite: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("favorites after unfavorite = %+v, want none", items)
	}
}

func TestFavoriteFileRequiresReadAccess(t *testing.T) {
	db := favoritesTestDB(t)
	accountID := uuid.New()
	ownerID := uuid.New()
	file := database.CloudFile{ID: database.NewID(), Name: "private.txt", AccountID: ownerID, Indexed: true}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	r := favoritesTestRouter(t, db, accountID)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/files/"+file.ID+"/favorite", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestListFavoriteFilesScopesToWorkspace(t *testing.T) {
	db := favoritesTestDB(t)
	accountID := uuid.New()
	workspaceID := database.NewID()
	personal := database.CloudFile{ID: database.NewID(), Name: "personal.txt", AccountID: accountID, Indexed: true}
	workspace := database.CloudFile{ID: database.NewID(), Name: "shared.txt", AccountID: accountID, Indexed: true, WorkspaceID: &workspaceID}
	for _, file := range []database.CloudFile{personal, workspace} {
		if err := db.Create(&file).Error; err != nil {
			t.Fatalf("create file: %v", err)
		}
		if err := db.Create(&database.CloudFileFavorite{ID: database.NewID(), AccountID: accountID, FileID: file.ID}).Error; err != nil {
			t.Fatalf("create favorite: %v", err)
		}
	}
	r := favoritesTestRouter(t, db, accountID)

	var items []database.CloudFile
	personalOnly := httptest.NewRecorder()
	r.ServeHTTP(personalOnly, httptest.NewRequest(http.MethodGet, "/api/files/favorites", nil))
	if personalOnly.Code != http.StatusOK {
		t.Fatalf("personal list status = %d, body = %s", personalOnly.Code, personalOnly.Body.String())
	}
	if err := json.Unmarshal(personalOnly.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode personal favorites: %v", err)
	}
	if len(items) != 1 || items[0].ID != personal.ID {
		t.Fatalf("personal favorites = %+v, want only %s", items, personal.ID)
	}

	scoped := httptest.NewRecorder()
	r.ServeHTTP(scoped, httptest.NewRequest(http.MethodGet, "/api/files/favorites?workspace_id="+workspaceID, nil))
	if scoped.Code != http.StatusOK {
		t.Fatalf("workspace list status = %d, body = %s", scoped.Code, scoped.Body.String())
	}
	if err := json.Unmarshal(scoped.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode workspace favorites: %v", err)
	}
	if len(items) != 1 || items[0].ID != workspace.ID {
		t.Fatalf("workspace favorites = %+v, want only %s", items, workspace.ID)
	}
}
