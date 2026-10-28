package realtime

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// errObjectNotFound marks a missing voice object.
var errObjectNotFound = errors.New("voice object not found")

// s3Settings mirrors the C# ChatVoiceS3Configuration.
type s3Settings struct {
	ServiceURL    string
	Endpoint      string
	PublicBaseURL string
	Region        string
	AccessKey     string
	SecretKey     string
	Bucket        string
	EnableSSL     bool
}

// s3Storage is the lazily built S3 client for voice clips. The client is only
// constructed once a clip actually has to be stored or read.
type s3Storage struct {
	settings s3Settings

	mu     sync.Mutex
	client *s3.Client
}

func (s *s3Storage) configured() bool {
	return strings.TrimSpace(s.settings.Bucket) != ""
}

// publicURL renders the proxy URL for a stored key, or "" when the deployment
// has no public base URL configured (the API then serves the clip itself).
func (s *s3Storage) publicURL(key string) string {
	base := strings.TrimSpace(s.settings.PublicBaseURL)
	if base == "" || key == "" {
		return ""
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	relative := &url.URL{Path: key}
	joined, err := url.Parse(base)
	if err != nil {
		return base + key
	}
	return joined.ResolveReference(relative).String()
}

// baseEndpoint resolves the S3 origin, falling back to ServiceUrl exactly like
// the C# configuration does.
func (s *s3Storage) baseEndpoint() (string, error) {
	endpoint := strings.TrimSpace(s.settings.Endpoint)
	useSSL := s.settings.EnableSSL
	if endpoint == "" {
		serviceURL := strings.TrimSpace(s.settings.ServiceURL)
		if serviceURL == "" {
			return "", errors.New("voice s3 endpoint or serviceUrl is required")
		}
		parsed, err := url.Parse(serviceURL)
		if err != nil {
			return "", err
		}
		endpoint = parsed.Host
		useSSL = strings.EqualFold(parsed.Scheme, "https")
	}
	if strings.Contains(endpoint, "://") {
		return strings.TrimRight(endpoint, "/"), nil
	}
	scheme := "http"
	if useSSL {
		scheme = "https"
	}
	return scheme + "://" + strings.TrimRight(endpoint, "/"), nil
}

func (s *s3Storage) clientFor(ctx context.Context) (*s3.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	if !s.configured() {
		return nil, errVoiceStorageUnavailable
	}
	endpoint, err := s.baseEndpoint()
	if err != nil {
		return nil, err
	}
	region := strings.TrimSpace(s.settings.Region)
	if region == "" {
		region = "us-east-1"
	}
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if s.settings.AccessKey != "" || s.settings.SecretKey != "" {
		options = append(options, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(s.settings.AccessKey, s.settings.SecretKey, ""),
		))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		// The C# deployment defaults to path-style addressing (MinIO).
		options.UsePathStyle = true
	})
	s.client = client
	return client, nil
}

func (s *s3Storage) put(ctx context.Context, key, contentType string, body io.Reader, size int64) error {
	client, err := s.clientFor(ctx)
	if err != nil {
		return err
	}
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.settings.Bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if size > 0 {
		input.ContentLength = aws.Int64(size)
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	_, err = client.PutObject(ctx, input)
	return err
}

// open returns the object body; errObjectNotFound when the key is gone.
func (s *s3Storage) open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	client, err := s.clientFor(ctx)
	if err != nil {
		return nil, 0, err
	}
	output, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.settings.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNoSuchKey(err) {
			return nil, 0, errObjectNotFound
		}
		return nil, 0, err
	}
	var size int64
	if output.ContentLength != nil {
		size = *output.ContentLength
	}
	return output.Body, size, nil
}

func (s *s3Storage) delete(ctx context.Context, key string) error {
	client, err := s.clientFor(ctx)
	if err != nil {
		return err
	}
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.settings.Bucket),
		Key:    aws.String(key),
	})
	if err != nil && isNoSuchKey(err) {
		return nil
	}
	return err
}

func isNoSuchKey(err error) bool {
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		code := apiError.ErrorCode()
		return code == "NoSuchKey" || code == "NotFound"
	}
	return false
}
