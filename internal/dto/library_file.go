package dto

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxLibraryFileNameLen mirrors the column width; a longer name is a mistake,
// not something to silently truncate.
const maxLibraryFileNameLen = 255

// maxLibraryTopicNameLen mirrors file_topic.name.
const maxLibraryTopicNameLen = 64

// inlineSafeContentTypes is the allowlist of types the library will hand back an
// IN-PLACE view url for. Everything else — including svg and html — gets a
// download url only.
//
// This is the whole XSS story of the feature. A presigned url points at the
// bucket's own origin, so an inline-rendered svg or html document would execute
// scripts in that origin's context, with whatever that origin is trusted for. A
// library accepts arbitrary file types by design, so the safety cannot live at
// the upload gate — it lives here, at the moment a url is minted.
var inlineSafeContentTypes = map[string]bool{
	"application/pdf": true,
	"image/jpeg":      true,
	"image/png":       true,
	"image/webp":      true,
	"image/gif":       true,
	"image/avif":      true,
	"video/mp4":       true,
	"video/webm":      true,
	"text/plain":      true,
}

// IsInlineSafeContentType reports whether a stored content type may be served
// with an inline (viewable) url. Parameters are stripped ("text/plain; charset=
// utf-8"), and the comparison is case-insensitive, because both come off a
// client-declared header.
func IsInlineSafeContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return inlineSafeContentTypes[ct]
}

// ValidateLibraryFileName trims and bounds a file name.
func ValidateLibraryFileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("file name is required")
	}
	if len([]rune(name)) > maxLibraryFileNameLen {
		return "", fmt.Errorf("file name must be at most %d characters", maxLibraryFileNameLen)
	}
	// A stored name reaches a Content-Disposition header at presign time, where
	// the sanitiser drops separators and control characters. Refusing them here
	// too means the stored value and the served value never disagree.
	if strings.ContainsAny(name, "/\\\"\r\n\x00") {
		return "", fmt.Errorf("file name must not contain slashes, quotes or control characters")
	}
	return name, nil
}

// ValidateLibraryTopicName trims and bounds a topic name.
func ValidateLibraryTopicName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("topic name is required")
	}
	if len([]rune(name)) > maxLibraryTopicNameLen {
		return "", fmt.Errorf("topic name must be at most %d characters", maxLibraryTopicNameLen)
	}
	if strings.ContainsAny(name, "\r\n\x00") {
		return "", fmt.Errorf("topic name must not contain control characters")
	}
	return name, nil
}

// ConvertEntityFileTopicToPb converts one topic label (without a count).
func ConvertEntityFileTopicToPb(t entity.FileTopic) *pb_admin.FileTopic {
	return &pb_admin.FileTopic{
		Id:          int32(t.Id),
		Name:        t.Name,
		Description: t.Description.String,
	}
}

// ConvertEntityFileTopicsWithCountToPb converts the rail: topics with their
// file counts, already ordered by usage.
func ConvertEntityFileTopicsWithCountToPb(topics []entity.FileTopicWithCount) []*pb_admin.FileTopic {
	out := make([]*pb_admin.FileTopic, 0, len(topics))
	for _, t := range topics {
		out = append(out, &pb_admin.FileTopic{
			Id:          int32(t.Id),
			Name:        t.Name,
			Description: t.Description.String,
			FilesCount:  int32(t.FilesCount),
		})
	}
	return out
}

// ConvertEntityLibraryFileToPb converts the stored metadata. It deliberately
// leaves the three url fields EMPTY: minting them needs the bucket and a policy
// decision about inline safety, which belongs to the API layer, not here.
func ConvertEntityLibraryFileToPb(f *entity.LibraryFile) *pb_admin.LibraryFile {
	if f == nil {
		return nil
	}
	topics := make([]*pb_admin.FileTopic, 0, len(f.Topics))
	for _, t := range f.Topics {
		topics = append(topics, ConvertEntityFileTopicToPb(t))
	}
	return &pb_admin.LibraryFile{
		Id:          int32(f.Id),
		FileName:    f.FileName,
		ContentType: f.ContentType,
		SizeBytes:   f.SizeBytes,
		Sha256:      f.Sha256,
		UploadedBy:  f.UploadedBy,
		Topics:      topics,
		CreatedAt:   timestamppb.New(f.CreatedAt),
	}
}

// ConvertPbTopicSelectionToEntity normalises the (topic_ids, new_topics) pair
// that both the upload and the update paths carry: ids are deduped and bounded,
// names are trimmed, validated and deduped case-insensitively so "Brand" typed
// twice does not try to create two topics in one request.
func ConvertPbTopicSelectionToEntity(topicIDs []int32, newTopics []string) ([]int, []string, error) {
	ids := make([]int, 0, len(topicIDs))
	seenID := make(map[int]bool, len(topicIDs))
	for _, id := range topicIDs {
		if id <= 0 {
			return nil, nil, fmt.Errorf("topic id must be positive")
		}
		if seenID[int(id)] {
			continue
		}
		seenID[int(id)] = true
		ids = append(ids, int(id))
	}
	names := make([]string, 0, len(newTopics))
	seenName := make(map[string]bool, len(newTopics))
	for _, n := range newTopics {
		name, err := ValidateLibraryTopicName(n)
		if err != nil {
			return nil, nil, err
		}
		key := strings.ToLower(name)
		if seenName[key] {
			continue
		}
		seenName[key] = true
		names = append(names, name)
	}
	return ids, names, nil
}
