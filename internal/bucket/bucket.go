package bucket

import (
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/minio/minio-go"
)

type Config struct {
	S3AccessKey       string `mapstructure:"s3AccessKey"`
	S3SecretAccessKey string `mapstructure:"s3SecretAccessKey"`
	S3Endpoint        string `mapstructure:"s3Endpoint"`
	S3BucketName      string `mapstructure:"s3BucketName"`
	S3BucketLocation  string `mapstructure:"s3BucketLocation"`
	BaseFolder        string `mapstructure:"baseFolder"`
	ImageStorePrefix  string `mapstructure:"imageStorePrefix"`
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
