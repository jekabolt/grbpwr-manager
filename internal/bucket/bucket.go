package bucket

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
	return &Bucket{
		Client: cli,
		Config: c,
		ms:     mediaStore,
	}, nil
}

func (b *Bucket) GetBaseFolder() string {
	return b.BaseFolder
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
