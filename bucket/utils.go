package bucket

import (
	"fmt"
	"strings"
	"time"
)

type FileType struct {
	Extension string
	MIMEType  string
}

func fileExtensionFromContentType(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	default:
		ss := strings.Split(contentType, "/")
		if len(ss) > 0 {
			return ss[1]
		}
		return contentType
	}
}

func (b *Bucket) getImageFullPath(filenameExtension string) string {
	now := time.Now()
	if len(b.ImageStorePrefix) > 0 {
		return fmt.Sprintf("%s/%d/%s/%d.%s", b.ImageStorePrefix, now.Year(), now.Month().String(), now.UnixNano(), filenameExtension)
	}
	return fmt.Sprintf("%d/%s/%d.%s", now.Year(), now.Month().String(), now.UnixNano(), filenameExtension)
}

func (b *Bucket) GetCDNURL(path string) string {
	return fmt.Sprintf("https://%s.%s/%s", b.S3BucketName, b.S3Endpoint, path)
}
