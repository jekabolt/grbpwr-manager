package bucket

import (
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
	MediaStorePrefix  string `mapstructure:"media_store_prefix"`
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

// getMediaName generates a file name based on a high-precision current timestamp
// It follows the convention: yyyyMMddHHmmssSSS
// Where SSS is milliseconds, ensuring uniqueness even within high-frequency upload scenarios
func GetMediaName() string {
	// Current time with milliseconds precision
	currentTime := time.Now().Format("20060102150405.000")

	// Remove the decimal point from the milliseconds for a compact representation
	currentTime = currentTime[:14] + currentTime[15:]

	return currentTime
}
