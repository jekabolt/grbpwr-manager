package bucket

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
}

func fileExtensionFromContentType(contentType ContentType) (string, error) {
	if ext, ok := mimeTypeToFileExtension[contentType]; ok {
		return ext, nil
	}
	return "", fmt.Errorf("unsupported MIME type %s", contentType)
}

type FileType struct {
	Extension string
	MIMEType  string
}

func (b *Bucket) constructFullPath(folder, fileName, ext string) string {
	// Get the current date
	now := time.Now()
	year := fmt.Sprintf("%d", now.Year())
	month := now.Month().String()

	// Convert the month to lowercase to match your example URL
	month = strings.ToLower(month)

	// Assuming that the BaseFolder contains "https://files.grbpwr.com/grbpwr-com/"
	return path.Clean(strings.Join([]string{b.BaseFolder, folder, year, month, fileName + "." + ext}, "/"))
}

func (b *Bucket) getOriginEndpoint(filePath string) string {
	return fmt.Sprintf("https://%s.%s/%s", b.S3BucketName, b.S3Endpoint, filePath)
}

func (b *Bucket) getCDNURL(filePath string) string {
	return fmt.Sprintf("https://%s/%s", b.SubdomainEndpoint, filePath)
}

type rawImage struct {
	B64Image  string `json:"b64Image"`
	MIMEType  string `json:"mimeType"`
	Extension string `json:"Extension"`
}

func GetExtensionFromB64String(b64 string) (string, error) {
	u, err := url.Parse(b64)
	if err != nil {
		return "", err
	}
	if u.Scheme != "data" {
		return "", fmt.Errorf("GetExtensionFromB64String: bad b64 string: [%s]", b64)
	}
	mimeType := strings.Split(u.Path, ";")[0]

	return fileExtensionFromContentType(ContentType(mimeType))
}

// image URL to base64 string
func getMediaB64(url string) (*rawImage, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("url: [%s] statusCode: [%d]", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	mimeType := http.DetectContentType(body)
	extension, err := fileExtensionFromContentType(ContentType(mimeType))
	if err != nil {
		return nil, err
	}

	base64Encoding := fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(body))

	return &rawImage{
		B64Image:  base64Encoding,
		MIMEType:  mimeType,
		Extension: extension,
	}, nil
}
