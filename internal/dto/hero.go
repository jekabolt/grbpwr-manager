package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func ConvertCommonHeroInsertToEntity(hi *pb_common.HeroInsert) entity.HeroInsert {
	return entity.HeroInsert{
		ContentLink: hi.ContentLink,
		ContentType: hi.ContentType,
		ExploreLink: hi.ExploreLink,
		ExploreText: hi.ExploreText,
	}
}

func ConvertEntityHeroInsertToCommon(hi *entity.HeroInsert) *pb_common.HeroInsert {
	return &pb_common.HeroInsert{
		ContentLink: hi.ContentLink,
		ContentType: hi.ContentType,
		ExploreLink: hi.ExploreLink,
		ExploreText: hi.ExploreText,
	}
}

func ConvertEntityHeroFullToCommon(hf *entity.HeroFull) (*pb_common.HeroFull, error) {

	ads := make([]*pb_common.HeroInsert, 0, len(hf.Ads))
	for _, ad := range hf.Ads {
		ads = append(ads, ConvertEntityHeroInsertToCommon(&ad))
	}
	prdsFeatured := make([]*pb_common.Product, 0, len(hf.ProductsFeatured))
	for _, prd := range hf.ProductsFeatured {
		prd, err := ConvertEntityProductToCommon(&prd)
		if err != nil {
			return nil, fmt.Errorf("failed to convert product: %w", err)
		}
		prdsFeatured = append(prdsFeatured, prd)
	}
	return &pb_common.HeroFull{
		Id:               int32(hf.Id),
		CreatedAt:        timestamppb.New(hf.CreatedAt),
		Main:             ConvertEntityHeroInsertToCommon(&hf.Main),
		Ads:              ads,
		ProductsFeatured: prdsFeatured,
	}, nil
}
