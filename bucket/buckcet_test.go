package bucket

import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"
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
	b := BucketFromConst()
	if err := b.InitBucket(); err != nil {
		t.Fatal("TestClient:InitBucket ", err)
	}

	spaces, err := b.ListBuckets()
	if err != nil {
		t.Fatal("TestClient:ListBuckets ", err)
	}

	for _, space := range spaces {
		fmt.Println(space.Name)
	}

	jpg, err := imageToB64(jpgFilePath)
	if err != nil {
		t.Fatal("imageToB64 err ", err)
	}

	png, err := imageToB64(pngFilePath)
	if err != nil {
		t.Fatal("imageToB64 err ", err)
	}

	tif, err := imageToB64(tifFilePath)
	if err != nil {
		t.Fatal("imageToB64 err ", err)
	}

	jpgUrl, err := b.UploadImage(jpg)
	if err != nil {
		t.Fatal("UploadImage jpg ", err)
	}

	pngUrl, err := b.UploadImage(png)
	if err != nil {
		t.Fatal("UploadImage png ", err)
	}

	_, err = b.UploadImage(tif)
	if err == nil {
		t.Fatal("tif is not supported ", err)
	}

	t.Logf("jpgUrl %s", jpgUrl)

	t.Logf("pngUrl %s", pngUrl)

	err = b.Client.RemoveObject(b.S3BucketName, getObjNameFromUrl(jpgUrl))
	if err != nil {
		t.Fatal("UploadImage jpg ", err)
	}

	err = b.Client.RemoveObject(b.S3BucketName, getObjNameFromUrl(pngUrl))
	if err != nil {
		t.Fatal("UploadImage png ", err)
	}
}
