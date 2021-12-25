package bucket

import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/matryer/is"
)

const S3AccessKey = "xxx"
const S3SecretAccessKey = "xxx"
const S3Endpoint = "fra1.digitaloceanspaces.com"
const bucketName = "grbpwr"
const bucketLocation = "fra-1"
const imageStorePrefix = "grbpwr-com"

const objectName = "test.jpg"

const jpgFilePath = "files/test.jpg"
const pngFilePath = "files/test.png"
const tifFilePath = "files/test.tif"

const jpgContentType = "image/jpeg"
const pngContentType = "image/png"
const tifContentType = "image/tiff"

const urlPrefix = "https://grbpwr.fra1.digitaloceanspaces.com/"

func skipCI(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping testing in CI environment")
	}
}

func BucketFromConst() *Bucket {
	return &Bucket{
		S3AccessKey:       S3AccessKey,
		S3SecretAccessKey: S3SecretAccessKey,
		S3Endpoint:        S3Endpoint,
		S3BucketName:      bucketName,
		S3BucketLocation:  bucketLocation,
		ImageStorePrefix:  imageStorePrefix,
	}
}

func imageToB64_v2(filePath string) (string, error) {
	ff, err := ioutil.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	b := make([]byte, len(ff)*2)
	base64.StdEncoding.Encode(b, ff)
	return string(b), nil
}

func imageToB64(filePath string) (string, error) {
	bytes, err := ioutil.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	var base64Encoding string

	// Determine the content type of the image file
	mimeType := http.DetectContentType(bytes)

	base64Encoding += fmt.Sprintf("data:%s;base64,", mimeType)

	// Append the base64 encoded output
	base64Encoding += toBase64(bytes)

	return base64Encoding, nil
}

func toBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func getObjNameFromUrl(url string) string {
	return strings.TrimPrefix(url, urlPrefix)
}

func TestUpload(t *testing.T) {
	skipCI(t)

	is := is.New(t)

	b := BucketFromConst()
	err := b.InitBucket()
	is.NoErr(err)

	spaces, err := b.ListBuckets()
	is.NoErr(err)

	for _, space := range spaces {
		fmt.Println(space.Name)
	}

	jpg, err := imageToB64(jpgFilePath)
	is.NoErr(err)

	png, err := imageToB64(pngFilePath)
	is.NoErr(err)

	tif, err := imageToB64(tifFilePath)
	is.NoErr(err)

	jpgUrl, err := b.UploadImage(jpg)
	is.NoErr(err)

	pngUrl, err := b.UploadImage(png)
	is.NoErr(err)

	_, err = b.UploadImage(tif)
	is.NoErr(err)

	t.Logf("jpgUrl %s", jpgUrl)

	t.Logf("pngUrl %s", pngUrl)

	err = b.Client.RemoveObject(b.S3BucketName, getObjNameFromUrl(jpgUrl))
	is.NoErr(err)

	err = b.Client.RemoveObject(b.S3BucketName, getObjNameFromUrl(pngUrl))
	is.NoErr(err)
}
