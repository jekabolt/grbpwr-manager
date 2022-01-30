package bucket

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"io/ioutil"
	"strings"

	"github.com/caarlos0/env/v6"
	"github.com/minio/minio-go"
	"github.com/nfnt/resize"
	"github.com/oliamb/cutter"
)

type Bucket struct {
	*minio.Client

	S3AccessKey       string `env:"S3_ACCESS_KEY" envDefault:"xxx"`
	S3SecretAccessKey string `env:"S3_SECRET_ACCESS_KEY" envDefault:"xxx"`
	S3Endpoint        string `env:"S3_ENDPOINT" envDefault:"fra1.digitaloceanspaces.com"`
	S3BucketName      string `env:"S3_BUCKET_NAME" envDefault:"grbpwr"`
	S3BucketLocation  string `env:"S3_BUCKET_LOCATION" envDefault:"fra-1"`
	ImageStorePrefix  string `env:"IMAGE_STORE_PREFIX" envDefault:"grbpwr-com"`
}

type B64Image struct {
	Content     []byte
	ContentType string
}

func BucketFromEnv() (*Bucket, error) {
	b := &Bucket{}
	err := env.Parse(b)
	return b, err
}

func (b *Bucket) InitBucket() error {
	cli, err := minio.New(b.S3Endpoint, b.S3AccessKey, b.S3SecretAccessKey, true)
	b.Client = cli
	return err
}

func (b *Bucket) UploadToBucket(img io.Reader, contentType string, postfix string) (string, error) {

	fp := b.getImageFullPath(fileExtensionFromContentType(contentType), postfix)

	userMetaData := map[string]string{"x-amz-acl": "public-read"} // make it public
	cacheControl := "max-age=31536000"

	bs, _ := ioutil.ReadAll(img)

	r := bytes.NewReader(bs)

	_, err := b.PutObject(b.S3BucketName, fp, r, int64(len(bs)), minio.PutObjectOptions{ContentType: contentType, CacheControl: cacheControl, UserMetadata: userMetaData})
	if err != nil {
		return "", fmt.Errorf("PutObject:err [%v]", err.Error())
	}

	return b.GetCDNURL(fp), nil
}

func GetB64ImageFromString(rawB64Image string) (*B64Image, error) {
	ss := strings.Split(rawB64Image, ";base64,")
	if len(ss) != 2 {
		return nil, fmt.Errorf("UploadImage:bad base64 image")
	}
	return &B64Image{
		Content:     []byte(ss[1]),
		ContentType: ss[0],
	}, nil

}

func (b *Bucket) UploadImage(rawB64Image string) (string, error) {
	var img image.Image

	b64Img, err := GetB64ImageFromString(rawB64Image)
	if err != nil {
		return "", err
	}

	switch b64Img.ContentType {
	case "data:image/jpeg":
		img, err = JPGFromB64(b64Img.Content)
		if err != nil {
			return "", fmt.Errorf("UploadImage:JPGFromB64: [%v]", err.Error())
		}
	case "data:image/png":
		img, err = PNGFromB64(b64Img.Content)
		if err != nil {
			return "", fmt.Errorf("UploadImage:PNGFromB64: [%v]", err.Error())
		}

	default:
		return "", fmt.Errorf("UploadImage:PNGFromB64: File type is not supported [%s]", b64Img.ContentType)
	}

	// square check
	if img.Bounds().Max.X != img.Bounds().Max.Y {
		return "", fmt.Errorf("UploadImage:image is not square: [%d x %d]", img.Bounds().Max.X, img.Bounds().Max.Y)
	}

	var buf bytes.Buffer
	imgWriter := bufio.NewWriter(&buf)

	err = EncodeJPG(imgWriter, img, 60)
	if err != nil {
		return "", fmt.Errorf("UploadImage:Encode: [%v]", err.Error())
	}

	imgReader := bufio.NewReader(&buf)
	url, err := b.UploadToBucket(imgReader, "image/jpeg", "")
	if err != nil {
		return "", fmt.Errorf("UploadImage:UploadToBucket: [%v]", err.Error())
	}

	return url, nil
}

func (b64Img *B64Image) B64ToImage() (image.Image, error) {
	var img image.Image
	var err error
	switch b64Img.ContentType {
	case "data:image/jpeg":
		img, err = JPGFromB64(b64Img.Content)
		if err != nil {
			return nil, fmt.Errorf("UploadImage:JPGFromB64: [%v]", err.Error())
		}
	case "data:image/png":
		img, err = PNGFromB64(b64Img.Content)
		if err != nil {
			return nil, fmt.Errorf("UploadImage:PNGFromB64: [%v]", err.Error())
		}
	default:
		return nil, fmt.Errorf("UploadImage:PNGFromB64: File type is not supported [%s]", b64Img.ContentType)
	}
	return img, err
}

func (b *Bucket) Upload(img image.Image, quality int, postfix string) (string, error) {
	var buf bytes.Buffer
	imgWriter := bufio.NewWriter(&buf)

	err := EncodeJPG(imgWriter, img, quality)
	if err != nil {
		return "", fmt.Errorf("Upload:EncodeJPG: [%v]", err.Error())
	}

	imgReader := bufio.NewReader(&buf)
	url, err := b.UploadToBucket(imgReader, "image/jpeg", postfix)
	if err != nil {
		return "", fmt.Errorf("Upload:UploadToBucket: [%v]", err.Error())
	}
	return url, nil
}

func imageFromString(rawB64Image string) (image.Image, error) {
	b64Img, err := GetB64ImageFromString(rawB64Image)
	if err != nil {
		return nil, err
	}
	return b64Img.B64ToImage()
}

func (b *Bucket) UploadProductImage(rawB64Image string) (*Image, error) {
	imgObj := &Image{}

	img, err := imageFromString(rawB64Image)
	if err != nil {
		return nil, err
	}

	// make it centered and 1x1
	croppedImg, err := cutter.Crop(img, cutter.Config{
		Width:  img.Bounds().Max.X,
		Height: img.Bounds().Max.X,
		Mode:   cutter.Centered,
	})
	if err != nil {
		return nil, fmt.Errorf("UploadProductImage:cutter.Crop: [%v]", err.Error())
	}

	imgObj.FullSize, err = b.Upload(croppedImg, 100, "og")
	if err != nil {
		return nil, fmt.Errorf("UploadProductImage:Upload:FullSize [%v]", err.Error())
	}

	resizedImage := resize.Resize(1000, 1000, croppedImg, resize.Lanczos3)
	imgObj.Compressed, err = b.Upload(resizedImage, 60, "compressed")
	if err != nil {
		return nil, fmt.Errorf("UploadProductImage:Upload:Compressed [%v]", err.Error())
	}

	resizedImage = resize.Resize(500, 500, croppedImg, resize.Lanczos3)
	imgObj.Thumbnail, err = b.Upload(resizedImage, 70, "thumb")
	if err != nil {
		return nil, fmt.Errorf("UploadProductImage:Upload: [%v]", err.Error())
	}

	return imgObj, nil
}

func (b *Bucket) UploadProductMainImage(rawB64Image string) (*MainImage, error) {
	imgObj := &MainImage{}

	img, err := imageFromString(rawB64Image)
	if err != nil {
		return nil, err
	}

	// make it centered and 1x1
	croppedImg, err := cutter.Crop(img, cutter.Config{
		Width:  img.Bounds().Max.X,
		Height: img.Bounds().Max.X,
		Mode:   cutter.Centered,
	})
	if err != nil {
		return nil, fmt.Errorf("UploadProductImage:cutter.Crop: [%v]", err.Error())
	}

	imgObj.FullSize, err = b.Upload(croppedImg, 100, "og")
	if err != nil {
		return nil, fmt.Errorf("UploadProductImage:Upload:FullSize [%v]", err.Error())
	}

	bounds := image.Rect(0, 0, 1200, 630).Bounds()
	merged := image.NewRGBA(bounds)
	draw.Draw(merged, merged.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	resizedImageMeta := resize.Resize(630, 630, croppedImg, resize.Lanczos3)
	offset := image.Pt(300, 0)
	draw.Draw(merged, resizedImageMeta.Bounds().Add(offset), resizedImageMeta, image.Point{}, draw.Over)

	if err != nil {
		return nil, fmt.Errorf("UploadProductImage:cutter.Crop [%v]", err.Error())
	}

	imgObj.MetaImage, err = b.Upload(merged, 60, "meta")
	if err != nil {
		return nil, fmt.Errorf("UploadProductImage:Upload:Compressed [%v]", err.Error())
	}

	resizedImage := resize.Resize(630, 630, croppedImg, resize.Lanczos3)
	imgObj.Compressed, err = b.Upload(resizedImage, 60, "compressed")
	if err != nil {
		return nil, fmt.Errorf("UploadProductImage:Upload:Compressed [%v]", err.Error())
	}

	resizedImage = resize.Resize(500, 500, croppedImg, resize.Lanczos3)
	imgObj.Thumbnail, err = b.Upload(resizedImage, 70, "thumb")
	if err != nil {
		return nil, fmt.Errorf("UploadProductImage:Upload: [%v]", err.Error())
	}

	return imgObj, nil
}
