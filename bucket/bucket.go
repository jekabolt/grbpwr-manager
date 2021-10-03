package bucket

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"io"
	"io/ioutil"
	"strings"

	"github.com/caarlos0/env/v6"
	"github.com/minio/minio-go"
)

type Bucket struct {
	*minio.Client

	DOAccessKey       string `env:"DO_ACCESS_KEY" envDefault:"xxx"`
	DOSecretAccessKey string `env:"DO_SECRET_ACCESS_KEY" envDefault:"xxx"`
	DOEndpoint        string `env:"DO_ENDPOINT" envDefault:"fra1.digitaloceanspaces.com"`
	DOBucketName      string `env:"DO_BUCKET_NAME" envDefault:"grbpwr"`
	DOBucketLocation  string `env:"DO_BUCKET_LOCATION" envDefault:"fra-1"`
	ImageStorePrefix  string `env:"IMAGE_STORE_PREFIX" envDefault:"grbpwr-com"`
}

func InitBucket() (*Bucket, error) {
	b := &Bucket{}
	err := env.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("BuntDB:InitDB:env.Parse: %s ", err.Error())
	}
	cli, err := minio.New(b.DOEndpoint, b.DOAccessKey, b.DOSecretAccessKey, true)
	b.Client = cli
	return b, err
}

// func (b *Bucket) UploadToBucket(img *bufio.Reader, contentType string) (string, error) {
func (b *Bucket) UploadToBucket(img io.Reader, contentType string) (string, error) {

	fp := b.getImageFullPath(fileExtensionFromContentType(contentType))

	userMetaData := map[string]string{"x-amz-acl": "public-read"} // make it public
	cacheControl := "max-age=31536000"

	bs, _ := ioutil.ReadAll(img)

	r := bytes.NewReader(bs)

	_, err := b.PutObject(b.DOBucketName, fp, r, int64(len(bs)), minio.PutObjectOptions{ContentType: contentType, CacheControl: cacheControl, UserMetadata: userMetaData})
	if err != nil {
		return "", fmt.Errorf("PutObject:err [%v]", err.Error())
	}

	return b.GetCDNURL(fp), nil
}

func (b *Bucket) UploadImage(rawB64Image string) (string, error) {
	var img image.Image
	var err error

	ss := strings.Split(rawB64Image, ";base64,")
	if len(ss) != 2 {
		return "", fmt.Errorf("UploadImage:bad base64 image")
	}

	b64Image := ss[1]
	contentType := ss[0]

	switch contentType {
	case "data:image/jpeg":
		img, err = JPGFromB64([]byte(b64Image))
		if err != nil {
			return "", fmt.Errorf("UploadImage:JPGFromB64: [%v]", err.Error())
		}
	case "data:image/png":
		img, err = PNGFromB64([]byte(b64Image))
		if err != nil {
			return "", fmt.Errorf("UploadImage:PNGFromB64: [%v]", err.Error())
		}

	default:
		return "", fmt.Errorf("UploadImage:PNGFromB64: File type is not supported [%s]", contentType)
	}

	var buf bytes.Buffer
	imgWriter := bufio.NewWriter(&buf)

	err = EncodeJPG(imgWriter, img)
	if err != nil {
		return "", fmt.Errorf("UploadImage:Encode: [%v]", err.Error())
	}

	imgReader := bufio.NewReader(&buf)
	url, err := b.UploadToBucket(imgReader, "image/jpeg")
	if err != nil {
		return "", fmt.Errorf("UploadImage:UploadToBucket: [%v]", err.Error())
	}

	return url, nil
}
