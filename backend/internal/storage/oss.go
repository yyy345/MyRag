package storage

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"regexp"
	"strings"
	"time"

	"ai-library/backend/internal/ports"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

type Config struct {
	Region, Bucket, AccessKeyID, AccessKeySecret, SecurityToken string
}

func (Config) String() string { return "OSSConfig{credentials:redacted}" }

var regionPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)+$`)
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Unset storage is permitted during migration; partial configuration is an error.
func LoadConfig(getenv func(string) string) (Config, bool, error) {
	c := Config{Region: getenv("OSS_REGION"), Bucket: getenv("OSS_BUCKET"), AccessKeyID: getenv("OSS_ACCESS_KEY_ID"), AccessKeySecret: getenv("OSS_ACCESS_KEY_SECRET"), SecurityToken: getenv("OSS_SECURITY_TOKEN")}
	if c.Region == "" && c.Bucket == "" && c.AccessKeyID == "" && c.AccessKeySecret == "" && c.SecurityToken == "" {
		return c, false, nil
	}
	c.Region = strings.TrimPrefix(c.Region, "oss-")
	if !regionPattern.MatchString(c.Region) || !bucketPattern.MatchString(c.Bucket) || c.AccessKeyID == "" || c.AccessKeySecret == "" ||
		strings.IndexFunc(c.AccessKeyID+c.AccessKeySecret+c.SecurityToken, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return Config{}, false, errors.New("OSS configuration must include valid region, bucket and printable credentials")
	}
	return c, true, nil
}

type Store struct {
	client             *oss.Client
	bucket, publicBase string
}

func (*Store) String() string { return "OSSStore{credentials:redacted}" }

func New(c Config) (*Store, error) {
	values := map[string]string{"OSS_REGION": c.Region, "OSS_BUCKET": c.Bucket, "OSS_ACCESS_KEY_ID": c.AccessKeyID, "OSS_ACCESS_KEY_SECRET": c.AccessKeySecret, "OSS_SECURITY_TOKEN": c.SecurityToken}
	c, enabled, err := LoadConfig(func(k string) string { return values[k] })
	if err != nil || !enabled {
		return nil, errors.New("OSS configuration is required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cfg := oss.NewConfig().WithRegion(c.Region).WithCredentialsProvider(credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.AccessKeySecret, c.SecurityToken)).WithHttpClient(client).WithRetryMaxAttempts(1).WithLogLevel(oss.LogOff)
	return &Store{client: oss.NewClient(cfg), bucket: c.Bucket, publicBase: "https://" + c.Bucket + ".oss-" + c.Region + ".aliyuncs.com"}, nil
}

// Decode bounds before full decoding, preventing compressed image memory bombs.
// Content-Type must match the actual PNG/JPEG format; MIME alone is not trusted.
func ValidateImage(data []byte, mediaType string) error {
	if len(data) == 0 || len(data) > ports.MaxImageBytes || (mediaType != "image/png" && mediaType != "image/jpeg") {
		return ports.ErrInvalidImage
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 25_000_000 || mediaType != "image/"+format {
		return ports.ErrInvalidImage
	}
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return ports.ErrInvalidImage
	}
	return nil
}

func imageKey(user, entry, extension string) (string, error) {
	if !uuidPattern.MatchString(user) || !uuidPattern.MatchString(entry) {
		return "", ports.ErrInvalidImage
	}
	return "ai-library/images/" + strings.ToLower(user) + "/" + strings.ToLower(entry) + "." + extension, nil
}

func (s *Store) PutImage(ctx context.Context, user, entry string, data []byte, mediaType string) (string, error) {
	if err := ValidateImage(data, mediaType); err != nil {
		return "", err
	}
	ext := "jpg"
	if mediaType == "image/png" {
		ext = "png"
	}
	key, err := imageKey(user, entry, ext)
	if err != nil {
		return "", err
	}
	_, err = s.client.PutObject(ctx, &oss.PutObjectRequest{Bucket: oss.Ptr(s.bucket), Key: oss.Ptr(key), Body: bytes.NewReader(data), ContentType: oss.Ptr(mediaType), ContentLength: oss.Ptr(int64(len(data))), Acl: oss.ObjectACLPublicRead})
	if err != nil {
		return "", safeError(ctx, err)
	}
	return s.publicBase + "/" + key, nil
}

// Try both historical extensions. Failure of one does not prevent the other.
// Missing keys are already successful idempotent DELETEs in OSS.
func (s *Store) DeleteImage(ctx context.Context, user, entry string) error {
	var failures []error
	for _, ext := range []string{"jpg", "png"} {
		key, err := imageKey(user, entry, ext)
		if err != nil {
			return err
		}
		_, err = s.client.DeleteObject(ctx, &oss.DeleteObjectRequest{Bucket: oss.Ptr(s.bucket), Key: oss.Ptr(key)})
		if err != nil {
			var service *oss.ServiceError
			if errors.As(err, &service) && service.Code == "NoSuchKey" {
				continue
			}
			failures = append(failures, safeError(ctx, err))
		}
	}
	return errors.Join(failures...)
}

func safeError(ctx context.Context, _ error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ports.ErrStorageUnavailable
}
