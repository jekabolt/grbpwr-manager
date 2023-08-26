package bucket

import (
	"fmt"

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
}
type Bucket struct {
	*minio.Client
	*Config
}

type B64Image struct {
	Content     []byte
	ContentType string
}

func (c *Config) Init() (dependency.FileStore, error) {
	cli, err := minio.New(c.S3Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(c.S3AccessKey, c.S3SecretAccessKey, ""),
		Secure: true, // assuming you're using SSL/TLS; set to false if not
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize MinIO client: %w", err)
	}
	return &Bucket{
		Client: cli,
		Config: c,
	}, nil
}
