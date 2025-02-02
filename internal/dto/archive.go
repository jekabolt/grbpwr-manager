package dto

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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
		Heading:     pbArchiveInsert.Heading,
		Description: pbArchiveInsert.Description,
		Tag:         pbArchiveInsert.Tag,
		MediaIds:    mids,
		VideoId:     sql.NullInt32{Int32: int32(pbArchiveInsert.VideoId), Valid: pbArchiveInsert.VideoId != 0},
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
		Id:          int32(af.Id),
		Heading:     af.Heading,
		Description: af.Description,
		Tag:         af.Tag,
		CreatedAt:   timestamppb.New(af.CreatedAt),
		Media:       mediaPb,
		Slug:        af.Slug,
		NextSlug:    af.NextSlug,
		Video:       ConvertEntityToCommonMedia(&af.Video),
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
	var sb strings.Builder
	sb.WriteString("/archive/")
	sb.WriteString(clean(title))
	sb.WriteString("/")
	sb.WriteString(clean(tag))
	sb.WriteString("/")
	sb.WriteString(fmt.Sprint(id))

	return sb.String()
}

func GetIdFromSlug(slug string) (int, error) {
	parts := strings.Split(slug, "/")
	if len(parts) < 4 {
		return 0, errors.New("slug is too short")
	}
	id, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0, err
	}
	return id, nil
}
