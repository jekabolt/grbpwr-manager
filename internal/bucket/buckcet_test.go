package bucket

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/stretchr/testify/assert"
)

const (
	S3AccessKey       = "***"
	S3SecretAccessKey = "***"
	S3Endpoint        = "fra1.digitaloceanspaces.com"
	bucketName        = "grbpwr"
	bucketLocation    = "fra-1"
	mediaStorePrefix  = "grbpwr-com"

	baseFolder = "grbpwr-com"

	jpgFilePath  = "files/test.jpg"
	mp4FilePath  = "files/test.mp4"
	webmFilePath = "files/test.webm"

	subdomainEndpoint = "files.grbpwr.com"
)

func skipCI(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping testing in CI environment")
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
		SubdomainEndpoint: subdomainEndpoint,
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

func fileToBytes(filePath string) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	fileStat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	fmt.Println("fileStat ", fileStat.Size())

	return io.ReadAll(file)
}

func TestUploadContentImage(t *testing.T) {
	skipCI(t)
	ctx := context.Background()

	b, err := BucketFromConst()
	assert.NoError(t, err)

	jpg, err := fileToB64ByPath(jpgFilePath)
	assert.NoError(t, err)

	i, err := b.UploadContentImage(ctx, jpg, "test", "test")
	assert.NoError(t, err)
	t.Logf("%+v", i)

	// err = b.DeleteFromBucket(ctx, i.ObjectIds)
	assert.NoError(t, err)
}

func TestUploadContentVideoMP4(t *testing.T) {
	skipCI(t)
	ctx := context.Background()

	b, err := BucketFromConst()
	assert.NoError(t, err)

	mp4, err := fileToBytes(mp4FilePath)
	assert.NoError(t, err)

	media, err := b.UploadContentVideo(ctx, mp4, "test", "test", contentTypeMP4)
	assert.NoError(t, err)
	fmt.Printf("----- %+v", media)

	// err = b.DeleteFromBucket(ctx, i.ObjectIds)
	assert.NoError(t, err)
}

func TestUploadContentVideoWEBM(t *testing.T) {
	skipCI(t)
	ctx := context.Background()

	b, err := BucketFromConst()
	assert.NoError(t, err)

	mp4, err := fileToBytes(webmFilePath)
	assert.NoError(t, err)

	media, err := b.UploadContentVideo(ctx, mp4, "test", "test", contentTypeWEBM)
	assert.NoError(t, err)
	fmt.Printf("----- %+v", media)

	// err = b.DeleteFromBucket(ctx, i.ObjectIds)
	assert.NoError(t, err)
}

func TestListObjects(t *testing.T) {
	skipCI(t)
	ctx := context.Background()

	b, err := BucketFromConst()
	assert.NoError(t, err)

	mediaList, err := b.ListObjects(ctx)
	assert.NoError(t, err)

	for _, m := range mediaList {
		fmt.Println(m.Url)
	}

	// err = b.DeleteFromBucket(ctx, i.ObjectIds)
	assert.NoError(t, err)
}

func TestGetB64FromUrl(t *testing.T) {
	url := "https://grbpwr.fra1.digitaloceanspaces.com/grbpwr-com/2022/April/1650908019115367000-og.jpg"

	rawImage, err := getMediaB64(url)
	assert.NoError(t, err)

	fmt.Println("--- b64", rawImage.B64Image)
	fmt.Println("--- ext", rawImage.Extension)

}
