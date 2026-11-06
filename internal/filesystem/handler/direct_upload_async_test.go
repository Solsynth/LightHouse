package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/filesystem/config"
	"src.solsynth.dev/sosys/filesystem/database"
	"src.solsynth.dev/sosys/filesystem/service"
	"src.solsynth.dev/sosys/filesystem/storage"
)

func newDirectUploadTestRouter(t *testing.T, dispatcher *recordingDispatcher) (*gin.Engine, *gorm.DB, *service.FileService, string) {
	t.Helper()
	endpoint := startNoAuthMockS3(t, "testbucket")
	db := openHandlerTestDB(t, &database.CloudFile{}, &database.FileObject{}, &database.FilePool{}, &database.FilePermission{}, &database.PersistentTask{})
	files := service.NewFileService(&database.DB{DB: db}, storage.NewLocalBackend(t.TempDir()))
	tasks := service.NewTaskService(&database.DB{DB: db})
	quota := service.NewQuotaService(&database.DB{DB: db})
	accountID := uuid.New()

	poolID := database.NewID()
	pool := database.FilePool{
		ID:        poolID,
		Name:      "test-s3",
		AccountID: accountID,
		StorageConfig: datatypes.JSON([]byte(fmt.Sprintf(
			`{"enable_signed":true,"enable_ssl":false,"endpoint":%q,"bucket":"testbucket","secret_id":"ak","secret_key":"sk"}`,
			endpoint))),
		BillingConfig: datatypes.JSON([]byte(`{}`)),
		PolicyConfig:  datatypes.JSON([]byte(`{}`)),
	}
	if err := db.Create(&pool).Error; err != nil {
		t.Fatalf("create pool: %v", err)
	}

	r := gin.New()
	r.Use(testAuthMiddleware(accountID))
	RegisterRoutes(r, Deps{
		Config:     &config.Config{},
		Files:      files,
		Tasks:      tasks,
		Quota:      quota,
		Dispatcher: dispatcher,
		Analysis:   analysisRegistryFor(files),
	})
	return r, db, files, poolID
}

func completeDirectTestUpload(t *testing.T, r *gin.Engine, prepareBody string, body []byte) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/files/upload/prepare", strings.NewReader(prepareBody))
	req.Header.Set("Content-Type", "application/json")
	wRec := httptest.NewRecorder()
	r.ServeHTTP(wRec, req)
	if wRec.Code != http.StatusOK {
		t.Fatalf("prepare status = %d, body = %s", wRec.Code, wRec.Body.String())
	}
	var prepared struct {
		TaskID    string `json:"task_id"`
		UploadURL string `json:"upload_url"`
	}
	if err := json.Unmarshal(wRec.Body.Bytes(), &prepared); err != nil {
		t.Fatalf("decode prepare response: %v", err)
	}
	if prepared.TaskID == "" || prepared.UploadURL == "" {
		t.Fatalf("prepare response = %s, want task_id and upload_url", wRec.Body.String())
	}
	putURL(t, prepared.UploadURL, body)

	req = httptest.NewRequest(http.MethodPost, "/api/files/upload/"+prepared.TaskID+"/complete", nil)
	wRec = httptest.NewRecorder()
	r.ServeHTTP(wRec, req)
	if wRec.Code != http.StatusOK {
		t.Fatalf("complete status = %d, body = %s", wRec.Code, wRec.Body.String())
	}
	var completed map[string]any
	if err := json.Unmarshal(wRec.Body.Bytes(), &completed); err != nil {
		t.Fatalf("decode complete response: %v", err)
	}
	return completed
}

// A direct upload without client analysis must not read the object back inside
// the completion request; the canonical hash is queued for the rehash sweep
// instead.
func TestCompleteDirectUploadDefersGenericRehash(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dispatcher := &recordingDispatcher{}
	r, db, files, poolID := newDirectUploadTestRouter(t, dispatcher)

	body := []byte("generic upload payload")
	completed := completeDirectTestUpload(t, r, fmt.Sprintf(
		`{"file_name":"data.bin","file_size":%d,"content_type":"application/octet-stream","pool_id":%q}`,
		len(body), poolID,
	), body)

	fileID, _ := completed["id"].(string)
	if fileID == "" {
		t.Fatalf("complete response = %#v, want file id", completed)
	}
	if completed["status"] != float64(database.UploadStatusCompleted) {
		t.Fatalf("file status = %#v, want completed", completed["status"])
	}
	file, err := files.GetFile(fileID)
	if err != nil {
		t.Fatalf("load file: %v", err)
	}
	if file.Object == nil {
		t.Fatal("file object missing")
	}
	if file.Object.Hash != "" {
		t.Fatalf("object hash = %q, want empty (deferred)", file.Object.Hash)
	}
	if !file.Object.NeedsRehash {
		t.Fatal("object was not queued for background rehash")
	}
	if len(dispatcher.uploaded) != 0 {
		t.Fatalf("completion dispatched source processing for a generic file: %#v", dispatcher.uploaded)
	}

	var object database.FileObject
	if err := db.First(&object, "id = ?", *file.ObjectID).Error; err != nil {
		t.Fatalf("load object: %v", err)
	}
	if object.Hash != "" || !object.NeedsRehash {
		t.Fatalf("stored object = %#v, want deferred rehash", object)
	}
}

// A video whose client supplied probe metadata but no thumbnail must take the
// deferred path: the client metadata is persisted, the file stays Processing
// and the worker is asked for the missing derivative. No inline source read.
func TestCompleteDirectUploadAcceptsVideoAnalysisWithoutThumbnail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dispatcher := &recordingDispatcher{}
	r, _, files, poolID := newDirectUploadTestRouter(t, dispatcher)

	body := []byte("not-a-real-video")
	completed := completeDirectTestUpload(t, r, fmt.Sprintf(
		`{"file_name":"movie.mp4","file_size":%d,"content_type":"video/mp4","pool_id":%q,`+
			`"client_analysis":{"width":1920,"height":1080,"duration_ms":83420}}`,
		len(body), poolID,
	), body)

	fileID, _ := completed["id"].(string)
	if fileID == "" {
		t.Fatalf("complete response = %#v, want file id", completed)
	}
	if completed["status"] != float64(database.UploadStatusProcessing) {
		t.Fatalf("file status = %#v, want processing until the thumbnail lands", completed["status"])
	}
	file, err := files.GetFile(fileID)
	if err != nil {
		t.Fatalf("load file: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(file.Object.Meta, &meta); err != nil {
		t.Fatalf("decode object metadata: %v", err)
	}
	if meta["analysis_source"] != "client" || meta["width"] != float64(1920) {
		t.Fatalf("object metadata = %#v, want client analysis", meta)
	}
	if !file.Object.NeedsReanalysis {
		t.Fatal("client-assisted upload did not queue background reanalysis")
	}
	if len(dispatcher.uploaded) != 1 {
		t.Fatalf("uploaded events = %#v, want one derivative job", dispatcher.uploaded)
	}
	if dispatcher.uploaded[0].FileID != fileID || dispatcher.uploaded[0].ContentType != "video/mp4" {
		t.Fatalf("uploaded event = %#v, want derivative job for %s", dispatcher.uploaded[0], fileID)
	}
}
