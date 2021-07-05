package bucket

import (
	"fmt"

	"github.com/minio/minio-go"
)

type Bucket struct {
	*minio.Client

	DOAccessKey       string `env:"DO_ACCESS_KEY" envDefault:"key"`
	DOSecretAccessKey string `env:"DO_SECRET_ACCESS_KEY" envDefault:"key"`
	DOEndpoint        string `env:"DO_ENDPOINT" envDefault:"fra1.digitaloceanspaces.com"`
	DOBucketName      string `env:"DO_BUCKET_NAME" envDefault:"grbpwr"`
	DOBucketLocation  string `env:"DO_BUCKET_LOCATION" envDefault:"fra-1"`
}

func (b *Bucket) GetBucket() (*Bucket, error) {
	cli, err := minio.New(b.DOEndpoint, b.DOAccessKey, b.DOSecretAccessKey, true)
	if err != nil {
		return nil, err
	}
	return &Bucket{
		Client: cli,
	}, nil
}

func (b *Bucket) UploadImage(b64Image string) (string, error) {
	r, ft, err := B64ToImage(b64Image)
	if err != nil {
		return "", fmt.Errorf("B64ToImage:err [%v]", err.Error())
	}

	fp := getImageFullPath(ft.Extension)
	if err != nil {
		return "", fmt.Errorf("getImageFullPath:err [%v]", err.Error())
	}

	_, err = b.PutObject(b.DOBucketName, fp, r, r.Size(), minio.PutObjectOptions{ContentType: ft.MIMEType})
	if err != nil {
		return "", fmt.Errorf("PutObject:err [%v]", err.Error())
	}

	return fp, nil
}
