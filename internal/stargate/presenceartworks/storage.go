package presenceartworks

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var errInvalidEndpoint = errors.New("presenceartworks: S3 endpoint is invalid")

// Storage is the MinIO/S3 object store. Callers gate on Service.Storage
// before using it; an unconfigured deployment keeps it nil.
type Storage struct {
	client *minio.Client
	bucket string
}

// NewStorage builds the S3 client. It returns (nil, nil) when the S3
// configuration is incomplete, matching the C# service that only throws once
// the lazy client is first used; the HTTP layer answers 503 in that state.
func NewStorage(settings Settings) (*Storage, error) {
	if !settings.Configured() {
		return nil, nil
	}
	host, secure, err := settings.endpoint()
	if err != nil {
		return nil, err
	}
	var transport http.RoundTripper
	if settings.S3.InsecureSkipVerify {
		if base, ok := http.DefaultTransport.(*http.Transport); ok {
			cloned := base.Clone()
			cloned.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // fleet self-signed CA
			transport = cloned
		}
	}
	lookup := minio.BucketLookupAuto
	if settings.S3.ForcePathStyle {
		lookup = minio.BucketLookupPath
	}
	client, err := minio.New(host, &minio.Options{
		Creds:        credentials.NewStaticV4(settings.S3.AccessKey, settings.S3.SecretKey, ""),
		Secure:       secure,
		Region:       settings.S3.Region,
		BucketLookup: lookup,
		Transport:    transport,
	})
	if err != nil {
		return nil, err
	}
	return &Storage{client: client, bucket: settings.S3.Bucket}, nil
}

// Put uploads one object with the original MIME type.
func (s *Storage) Put(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType})
	return err
}

// Open returns the object as a read-seeker so the HTTP layer can serve Range
// requests. A missing object is reported as ErrArtworkStreamNotFound.
func (s *Storage) Open(ctx context.Context, key string) (io.ReadSeekCloser, error) {
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		if isMissingObject(err) {
			return nil, ErrArtworkStreamNotFound
		}
		return nil, err
	}
	if _, err := object.Seek(0, io.SeekStart); err != nil {
		_ = object.Close()
		return nil, err
	}
	return object, nil
}

// Delete removes an object. A missing object is not an error (the C# cleanup
// job treated ObjectNotFoundException as success).
func (s *Storage) Delete(ctx context.Context, key string) error {
	err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	if err != nil && isMissingObject(err) {
		return nil
	}
	return err
}

func isMissingObject(err error) bool {
	if err == nil {
		return false
	}
	response := minio.ToErrorResponse(err)
	if response.Code == "NoSuchKey" || response.Code == "NoSuchObject" || response.Code == "NotFound" {
		return true
	}
	return response.StatusCode == http.StatusNotFound
}
