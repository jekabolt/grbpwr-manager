package bucket

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/matryer/is"
)

const (
	S3AccessKey       = "YEYEN6TU2NCOPNPICGY3"
	S3SecretAccessKey = "lyvzQ6f20TxiGE2hadU3Og7Er+f8j0GfUAB3GnZkreE"
	S3Endpoint        = "fra1.digitaloceanspaces.com"
	bucketName        = "grbpwr"
	bucketLocation    = "fra-1"
	mediaStorePrefix  = "grbpwr-com"

	baseFolder = "grbpwr-com"

	jpgFilePath = "files/test.jpg"
)

func skipCI(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping testing in CI environment")
	}
}

func cleanup(ctx context.Context, bucket dependency.FileStore, objectKey string) {
	err := bucket.DeleteFromBucket(ctx, objectKey)
	if err != nil {
		fmt.Printf("Failed to cleanup: %v", err)
	}
}

func BucketFromConst() (dependency.FileStore, error) {
	c := &Config{
		S3AccessKey:       S3AccessKey,
		S3SecretAccessKey: S3SecretAccessKey,
		S3Endpoint:        S3Endpoint,
		S3BucketName:      bucketName,
		S3BucketLocation:  bucketLocation,
		MediaStorePrefix:  mediaStorePrefix,
		BaseFolder:        baseFolder,
	}
	return c.Init()
}

func fileToB64ByPath(filePath string) (string, error) {
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	var base64Encoding string

	// Determine the content type of the file
	mimeType := http.DetectContentType(bytes)

	base64Encoding += fmt.Sprintf("data:%s;base64,", mimeType)

	// Append the base64 encoded output
	base64Encoding += base64.StdEncoding.EncodeToString(bytes)

	return base64Encoding, nil
}

func TestUploadContentImage(t *testing.T) {
	skipCI(t)
	ctx := context.Background()

	is := is.New(t)

	b, err := BucketFromConst()
	is.NoErr(err)

	jpg, err := fileToB64ByPath(jpgFilePath)
	is.NoErr(err)

	i, err := b.UploadContentImage(ctx, jpg, "test", "test")
	is.NoErr(err)
	fmt.Printf("%+v", i)
}

func TestGetB64FromUrl(t *testing.T) {
	url := "https://grbpwr.fra1.digitaloceanspaces.com/grbpwr-com/2022/April/1650908019115367000-og.jpg"
	is := is.New(t)
	rawImage, err := getMediaB64(url)
	is.NoErr(err)

	fmt.Println("--- b64", rawImage.B64Image)
	fmt.Println("--- ext", rawImage.Extension)

}
