package worker

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/filesystem/database"
	"src.solsynth.dev/sosys/filesystem/eventbus"
	"src.solsynth.dev/sosys/filesystem/service"
	"src.solsynth.dev/sosys/filesystem/storage"
)

// Processing a file kind with no derivative handler must complete the upload
// without pulling the source object out of storage. The storage key here does
// not exist, so any read attempt would fail the upload.
func TestProcessUploadedFileSkipsStorageReadWithoutDerivativeHandler(t *testing.T) {
	tmp := t.TempDir()
	db := openWorkerTestDB(t, &database.CloudFile{}, &database.FileObject{}, &database.FilePool{})
	stor := storage.NewLocalBackend(tmp)
	svc := service.NewFileService(&database.DB{DB: db}, stor)
	seedWorkerDefaultPool(t, db, tmp)

	objectID := database.NewID()
	storageKey := "uploads/missing-object"
	if err := db.Create(&database.FileObject{
		ID:         objectID,
		Size:       16,
		MimeType:   "application/octet-stream",
		StorageKey: &storageKey,
		Meta:       datatypes.JSON([]byte(`{}`)),
	}).Error; err != nil {
		t.Fatalf("create object: %v", err)
	}
	fileID := database.NewID()
	if err := db.Create(&database.CloudFile{
		ID:           fileID,
		Name:         "archive.bin",
		AccountID:    uuid.New(),
		ObjectID:     &objectID,
		StorageKey:   &storageKey,
		UploadStatus: database.UploadStatusProcessing,
	}).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}

	w := newTestWorker(svc, db, tmp)
	if err := w.ProcessUploadedFile(context.Background(), eventbus.FileUploadedEvent{
		FileID:      fileID,
		ContentType: "application/octet-stream",
	}); err != nil {
		t.Fatalf("ProcessUploadedFile() error = %v, want no storage read", err)
	}

	var file database.CloudFile
	if err := db.First(&file, "id = ?", fileID).Error; err != nil {
		t.Fatalf("load file: %v", err)
	}
	if file.UploadStatus != database.UploadStatusCompleted {
		t.Fatalf("upload status = %v, want completed", file.UploadStatus)
	}
}
