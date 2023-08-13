package bucket

import (
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/minio/minio-go"
)

type Config struct {
	S3AccessKey       string `mapstructure:"s3_access_key"`
	S3SecretAccessKey string `mapstructure:"s3_secret_access_key"`
	S3Endpoint        string `mapstructure:"s3_endpoint"`
	S3BucketName      string `mapstructure:"s3_bucket_name"`
	S3BucketLocation  string `mapstructure:"s3_bucket_location"`
	BaseFolder        string `mapstructure:"base_folder"`
	ImageStorePrefix  string `mapstructure:"image_store_prefix"`
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
	cli, err := minio.New(c.S3Endpoint, c.S3AccessKey, c.S3SecretAccessKey, true)
	return &Bucket{
		Client: cli,
		Config: c,
	}, err
}
