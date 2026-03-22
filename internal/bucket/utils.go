package bucket

import (
	"fmt"
	"path"
	"strings"
	"time"
)

type ContentType string

func (ct *ContentType) String() string {
	return string(*ct)
}

const (
	contentTypeJPEG ContentType = "image/jpeg"
	contentTypePNG  ContentType = "image/png"
	contentTypeWEBP ContentType = "image/webp"
	contentTypeJSON ContentType = "application/json"
	contentTypeMP4  ContentType = "video/mp4"
	contentTypeWEBM ContentType = "video/webm"
)

var mimeTypeToFileExtension = map[ContentType]string{
	contentTypeJPEG: "jpg",
	contentTypePNG:  "png",
	contentTypeJSON: "json",
	contentTypeMP4:  "mp4",
	contentTypeWEBM: "webm",
	contentTypeWEBP: "webp",
}

func fileExtensionFromContentType(contentType ContentType) (string, error) {
	if ext, ok := mimeTypeToFileExtension[contentType]; ok {
		return ext, nil
	}
	return "", fmt.Errorf("unsupported MIME type %s", contentType)
}

func (b *Bucket) constructFullPath(folder, fileName, ext string) string {
	now := time.Now().UTC()
	year := fmt.Sprintf("%d", now.Year())
	month := strings.ToLower(now.Month().String())
	return path.Clean(strings.Join([]string{b.BaseFolder, folder, year, month, fileName + "." + ext}, "/"))
}

func (b *Bucket) getOriginEndpoint(filePath string) string {
	return fmt.Sprintf("https://%s.%s/%s", b.S3BucketName, b.S3Endpoint, filePath)
}

func (b *Bucket) getCDNURL(filePath string) string {
	return fmt.Sprintf("https://%s/%s", b.SubdomainEndpoint, filePath)
}
