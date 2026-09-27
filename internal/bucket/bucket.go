package bucket

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Config struct {
	S3AccessKey       string `mapstructure:"s3_access_key"`
	S3SecretAccessKey string `mapstructure:"s3_secret_access_key"`
	S3Endpoint        string `mapstructure:"s3_endpoint"`
	S3BucketName      string `mapstructure:"s3_bucket_name"`
	S3BucketLocation  string `mapstructure:"s3_bucket_location"`
	BaseFolder        string `mapstructure:"base_folder"`
	SubdomainEndpoint string `mapstructure:"subdomain_endpoint"`
}

// String renders the config with the S3 credentials redacted so an accidental %v/%+v/%s of the bucket
// config — in a log line, an error, or a test print — can never leak the access/secret keys (problem
// 007). fmt routes %v, %+v and %s through Stringer, so all three are covered.
func (c Config) String() string {
	return fmt.Sprintf("bucket.Config{S3AccessKey:%s S3SecretAccessKey:%s S3Endpoint:%s S3BucketName:%s "+
		"S3BucketLocation:%s BaseFolder:%s SubdomainEndpoint:%s}",
		redactSecret(c.S3AccessKey), redactSecret(c.S3SecretAccessKey), c.S3Endpoint, c.S3BucketName,
		c.S3BucketLocation, c.BaseFolder, c.SubdomainEndpoint)
}

// redactSecret masks a secret value while preserving whether it was set (empty stays empty so an
// unconfigured field is still visible as such).
func redactSecret(s string) string {
	if s == "" {
		return ""
	}
	return "***REDACTED***"
}

type Bucket struct {
	*minio.Client
	*Config
	ms dependency.Media
}

func New(c *Config, mediaStore dependency.Media) (dependency.FileStore, error) {
	cli, err := minio.New(c.S3Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(c.S3AccessKey, c.S3SecretAccessKey, ""),
		Secure: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize MinIO client: %w", err)
	}

	// Validate credentials/endpoint connectivity at boot so the app fails fast
	// instead of appearing healthy and failing on the first upload at runtime.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exists, err := cli.BucketExists(ctx, c.S3BucketName)
	if err != nil {
		return nil, fmt.Errorf("bucket connectivity check failed: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("bucket connectivity check failed: configured bucket %q does not exist", c.S3BucketName)
	}
	slog.Default().InfoContext(ctx, "bucket connectivity verified",
		slog.String("bucket", c.S3BucketName),
		slog.String("endpoint", c.S3Endpoint),
	)

	return &Bucket{
		Client: cli,
		Config: c,
		ms:     mediaStore,
	}, nil
}

func (b *Bucket) GetBaseFolder() string {
	return b.BaseFolder
}

// ObjectKeyFromStoredURL достаёт ключ объекта из СОХРАНЁННОГО https-url — того, что лежит в
// колонке медиа/выкройки и был записан этим же бакетом.
//
// ⚠ ЭТО РАЗБОР ПУТИ, А НЕ РЕШЕНИЕ О ПРАВЕ ЧИТАТЬ. Гард стоит там, где происходит обращение к S3:
// GetManagedObject отказывает на ключе вне разрешённых сегментов ДО сети, а DeleteObjects идёт
// через managedObjectKeyFromURL, который сверяет хост с настроенным. Здесь проверяется ровно то,
// без чего ключа нет вовсе: https, непустой хост, непустой путь.
//
// ФУНКЦИЯ ЖИВЁТ ЗДЕСЬ, ПОТОМУ ЧТО ЧИТАТЕЛЕЙ У НЕЁ ТРИ И ОНИ В РАЗНЫХ ЯРУСАХ: экспорт архива
// тех-карты, кроп кадра в admin и производные плейграунда в designgen. Копия в apisrv/admin
// заставила бы designgen импортировать слой API ради двадцати строк разбора url — то есть
// перевернуть зависимость ради функции, которая про бакет и ни про что больше.
func ObjectKeyFromStoredURL(rawURL string) (string, error) {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return "", errors.New("the row carries no object url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse object url %q: %w", raw, err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("object url %q is not a managed https url", raw)
	}
	key := strings.Trim(u.Path, "/")
	if key == "" {
		return "", fmt.Errorf("object url %q carries no key", raw)
	}
	return key, nil
}

// objectKeyFromURL derives the S3 object key encoded in a URL path. Ownership is deliberately not
// decided here; managedObjectKeyFromURL additionally verifies the configured CDN/origin host before
// DeleteObjects performs an external side effect.
//
// ОДНА РЕАЛИЗАЦИЯ НА ОБА ИМЕНИ: единственный вызывающий (managedObjectKeyFromURL) уже потребовал
// https и настроенный хост, так что строгость экспортированной версии здесь ничего не отнимает, а
// две почти одинаковые функции разбора url в одном пакете разъехались бы молча.
func objectKeyFromURL(rawURL string) (string, error) {
	return ObjectKeyFromStoredURL(rawURL)
}

func (b *Bucket) managedObjectKeyFromURL(rawURL string) (string, error) {
	return ManagedObjectKeyFromURL(b.Config, rawURL)
}

// ManagedObjectKeyFromURL is the HOST-CHECKED key of a media url: https, no userinfo, and a host
// that is this bucket's CDN subdomain or its virtual-hosted origin — anything else is refused.
// Exported for the readers that must turn a url into bytes WITHOUT fetching it (the Gemini chat
// transport inlines pictures, and a url it could not vouch for would be a fetch of our choosing
// on someone else's behalf); the key still passes GetManagedObject's segment gate afterwards.
// A nil config manages no url.
func ManagedObjectKeyFromURL(c *Config, rawURL string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("media url %q: no bucket is configured", rawURL)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse media url %q: %w", rawURL, err)
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("media url %q is not a managed https url", rawURL)
	}
	cdnHost := configuredURLHost(c.SubdomainEndpoint)
	endpointHost := configuredURLHost(c.S3Endpoint)
	originHost := ""
	if c.S3BucketName != "" && endpointHost != "" {
		originHost = strings.ToLower(c.S3BucketName + "." + endpointHost)
	}
	requestHost := strings.ToLower(u.Host)
	if requestHost != cdnHost && requestHost != originHost {
		return "", fmt.Errorf("media url host %q is not a configured bucket host", u.Host)
	}
	return objectKeyFromURL(rawURL)
}

// ManagedHosts returns the hosts a stored media/pattern url may legitimately point at:
// the CDN subdomain and the bucket's virtual-hosted origin. It is the single definition of
// "our bucket" — write validation (dto) uses it so a pattern row can never carry a foreign
// url, and it is computed here because this package owns the bucket config.
func ManagedHosts(c *Config) []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, 2)
	if cdn := configuredURLHost(c.SubdomainEndpoint); cdn != "" {
		out = append(out, cdn)
	}
	if endpoint := configuredURLHost(c.S3Endpoint); endpoint != "" && c.S3BucketName != "" {
		out = append(out, strings.ToLower(c.S3BucketName+"."+endpoint))
	}
	return out
}

func configuredURLHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// DeleteObjects best-effort removes the S3 objects behind the given media URLs. Only URLs on the
// configured CDN or bucket-origin host are eligible. Empty and duplicate URLs are skipped (a video
// row stores the same URL in all three variant fields). It attempts every distinct key and returns
// the first error, so a transient failure on one object does not skip the others.
func (b *Bucket) DeleteObjects(ctx context.Context, urls ...string) error {
	seen := make(map[string]struct{}, len(urls))
	var firstErr error
	for _, raw := range urls {
		if raw == "" {
			continue
		}
		key, err := b.managedObjectKeyFromURL(raw)
		if err != nil {
			slog.Default().ErrorContext(ctx, "refusing to delete object for unmanaged media url",
				slog.String("url", raw), slog.String("err", err.Error()))
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if err := b.Client.RemoveObject(ctx, b.S3BucketName, key, minio.RemoveObjectOptions{}); err != nil {
			slog.Default().ErrorContext(ctx, "can't remove object from bucket",
				slog.String("key", key), slog.String("err", err.Error()))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// GetMediaName generates a unique file name based on the current UTC timestamp with millisecond
// precision plus a 4-character random hex suffix to prevent collisions within the same millisecond.
// Format: yyyyMMddHHmmssSSS<4hex>  (21 characters)
func GetMediaName() string {
	ts := time.Now().UTC().Format("20060102150405.000")
	ts = ts[:14] + ts[15:]
	buf := make([]byte, 2)
	rand.Read(buf) //nolint:errcheck // crypto/rand.Read never returns an error on supported platforms
	return ts + hex.EncodeToString(buf)
}
