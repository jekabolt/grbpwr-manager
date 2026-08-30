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
	contentTypePDF  ContentType = "application/pdf"
	// contentTypeDXF is accepted ONLY by the pattern upload path (UploadPatternFile);
	// the image/video media paths gate on their own allowlists and never reach it.
	contentTypeDXF ContentType = "image/vnd.dxf"
	// contentTypeSVG and contentTypeGLB are accepted ONLY by the non-raster path
	// (UploadContentNonRaster, see nonraster.go), which checks the bytes before it stores
	// them — an SVG through recraft.InspectSVG, a GLB through its container header. The
	// raster paths sniff for a picture format and can never arrive at either: sniffImageType
	// does not recognise them, so they fall into its "unrecognized" refusal.
	//
	// contentTypeSVG is spelled the same as recraft.SVGContentType, and it has to be: the
	// media row, the bucket object and the browser must all be told the same thing.
	contentTypeSVG ContentType = "image/svg+xml"
	// contentTypeGLB is the IANA type for a glTF binary. The browser needs THIS one — a model
	// served as application/octet-stream is a download, not something a viewer can open.
	contentTypeGLB ContentType = "model/gltf-binary"

	// Image formats identified by content sniffing. Only JPEG/PNG/WebP/HEIC are
	// decodable; AVIF/HEIF/GIF are recognized solely to emit a precise error.
	contentTypeHEIC ContentType = "image/heic"
	contentTypeHEIF ContentType = "image/heif"
	contentTypeAVIF ContentType = "image/avif"
	contentTypeGIF  ContentType = "image/gif"
)

var mimeTypeToFileExtension = map[ContentType]string{
	contentTypeJPEG: "jpg",
	contentTypePNG:  "png",
	contentTypeJSON: "json",
	contentTypeMP4:  "mp4",
	contentTypeWEBM: "webm",
	contentTypeWEBP: "webp",
	contentTypePDF:  "pdf",
	contentTypeGIF:  "gif",
	contentTypeDXF:  "dxf",
	contentTypeSVG:  "svg",
	contentTypeGLB:  "glb",
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
