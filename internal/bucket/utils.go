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

// ─────────────────── WHAT A STORED OBJECT IS, READ BACK OFF ITS ADDRESS ───────────────────

// extensionToMimeType is mimeTypeToFileExtension INVERTED, built from that map rather than typed
// out a second time. A hand-written copy is how "the bucket writes .glb" and "the reader knows
// .glb" come to disagree — and this pair is read at a money door, where disagreeing means either
// a paid call to a vendor that cannot read the file or a refusal of a run that was fine.
//
// The map is injective today and the probe holds it to that (a duplicate extension would make one
// of two types unrecoverable, silently).
var extensionToMimeType = func() map[string]ContentType {
	out := make(map[string]ContentType, len(mimeTypeToFileExtension))
	for ct, ext := range mimeTypeToFileExtension {
		out[ext] = ct
	}
	return out
}()

// ObjectMediaType names the content type of a STORED object, read back from the extension this
// package itself put on it (constructFullPath appends exactly one, from the closed map above).
//
// ⚠ WHY THE ADDRESS AND NOT THE MEDIA ROW'S DIMENSIONS. A caller asking "is this object a picture"
// has two candidate facts about a media row: the extension, and width/height being zero. MEASURED
// on beta's 195 rows: zero dimensions identifies the GLB models and the video, and MISSES EVERY
// SVG — a stored vector carries the size it declared in its viewBox (svgPixelSize), so all three
// SVG rows read 502×865, 528×851, 528×851. Zero dimensions is a CONSEQUENCE of non-rasterness for
// some types and not for others; the extension is the fact the storage path actually stated.
//
// ok = false means "not an extension this package writes", and the caller must treat that as
// UNKNOWN rather than as any particular type: a legacy row, or a url that came from somewhere else.
// Guessing on that is how a guard comes to refuse something legitimate.
func ObjectMediaType(objectURL string) (string, bool) {
	u := strings.TrimSpace(objectURL)
	// A query string or a fragment is not part of the object name. Neither appears on our own CDN
	// urls today; both are cheap to survive and expensive to meet unprepared at a money door.
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(u), "."))
	if ext == "" {
		return "", false
	}
	ct, ok := extensionToMimeType[ext]
	return string(ct), ok
}
