package bucket

import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/matryer/is"
)

const (
	S3AccessKey       = "xxx"
	S3SecretAccessKey = "xxx"
	S3Endpoint        = "fra1.digitaloceanspaces.com"
	bucketName        = "grbpwr"
	bucketLocation    = "fra-1"
	imageStorePrefix  = "grbpwr-com"

	baseFolder = "solutions"

	jpgFilePath = "files/test.jpg"
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
		ImageStorePrefix:  imageStorePrefix,
		BaseFolder:        "solutions",
	}
	return c.Init()
}

func imageToB64ByPath(filePath string) (string, error) {
	bytes, err := ioutil.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	var base64Encoding string

	// Determine the content type of the image file
	mimeType := http.DetectContentType(bytes)

	base64Encoding += fmt.Sprintf("data:%s;base64,", mimeType)

	// Append the base64 encoded output
	base64Encoding += base64.StdEncoding.EncodeToString(bytes)

	return base64Encoding, nil
}

func TestUploadContentImage(t *testing.T) {
	skipCI(t)

	is := is.New(t)

	b, err := BucketFromConst()
	is.NoErr(err)

	jpg, err := imageToB64ByPath(jpgFilePath)
	is.NoErr(err)

	i, err := b.UploadContentImage(jpg, "test", "test")
	is.NoErr(err)
	fmt.Printf("%+v", i)
}

func TestGetB64FromUrl(t *testing.T) {
	url := "https://grbpwr.fra1.digitaloceanspaces.com/grbpwr-com/2022/April/1650908019115367000-og.jpg"
	is := is.New(t)
	rawImage, err := getImageB64(url)
	is.NoErr(err)

	fmt.Println("--- b64", rawImage.B64Image)
	fmt.Println("--- ext", rawImage.Extension)

}