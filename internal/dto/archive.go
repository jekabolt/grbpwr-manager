package dto

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Convert a protobuf ArchiveNew to an entity ArchiveNew
func ConvertPbArchiveInsertToEntity(pbArchiveInsert *pb_common.ArchiveInsert) (*entity.ArchiveInsert, error) {
	if pbArchiveInsert == nil {
		return nil, errors.New("archive insert is nil")
	}

	if len(pbArchiveInsert.MediaIds) == 0 {
		return nil, errors.New("archive media ids must not be empty")
	}

	mids := make([]int, 0, len(pbArchiveInsert.MediaIds))
	for _, mid := range pbArchiveInsert.MediaIds {
		mids = append(mids, int(mid))
	}

	return &entity.ArchiveInsert{
		Title:       pbArchiveInsert.Title,
		Description: pbArchiveInsert.Description,
		Tag:         pbArchiveInsert.Tag,
		MediaIds:    mids,
	}, nil
}

// Convert an entity ArchiveFull to a protobuf ArchiveFull
func ConvertArchiveFullEntityToPb(af *entity.ArchiveFull) *pb_common.ArchiveFull {
	if af == nil {
		return nil
	}

	mediaPb := make([]*pb_common.MediaFull, 0, len(af.Media))
	for _, m := range af.Media {
		mediaPb = append(mediaPb, ConvertEntityToCommonMedia(&m))
	}

	return &pb_common.ArchiveFull{
		Title:       af.Title,
		Description: af.Description,
		Tag:         af.Tag,
		CreatedAt:   timestamppb.New(af.CreatedAt),
		Media:       mediaPb,
	}
}

// TODO:
var aSreg = regexp.MustCompile("[^a-zA-Z0-9]+")

func GetArchiveSlug(id int, title, tag string) string {
	clean := func(part string) string {
		// Replace all non-alphanumeric characters with an empty string
		return aSreg.ReplaceAllString(part, "")
	}

	// Use strings.Builder for efficient string concatenation
	var sb strings.Builder.
	sb.WriteString("/archive/")
	sb.WriteString(gender)
	sb.WriteString("/")
	sb.WriteString(clean(brand))
	sb.WriteString("/")
	sb.WriteString(clean(name))
	sb.WriteString("/")
	sb.WriteString(fmt.Sprint(id))

	return sb.String()
}
