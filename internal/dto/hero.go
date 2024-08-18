package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func ConvertCommonHeroInsertToEntity(hi *pb_common.HeroItemInsert) entity.HeroInsert {
	return entity.HeroInsert{
		MediaId:     int(hi.MediaId),
		ExploreLink: hi.ExploreLink,
		ExploreText: hi.ExploreText,
		IsMain:      hi.IsMain,
	}
}

func ConvertEntityHeroItemInsertToCommon(hi *entity.HeroInsert) *pb_common.HeroItemInsert {
	return &pb_common.HeroItemInsert{
		MediaId:     int32(hi.MediaId),
		ExploreLink: hi.ExploreLink,
		ExploreText: hi.ExploreText,
	}
}

func ConvertEntityHeroItemToCommon(hi *entity.HeroItem) *pb_common.HeroItem {
	return &pb_common.HeroItem{
		Media:       ConvertEntityToCommonMedia(hi.Media),
		ExploreLink: hi.ExploreLink,
		ExploreText: hi.ExploreText,
		IsMain:      hi.IsMain,
	}
}

func ConvertEntityHeroFullToCommon(hf *entity.HeroFull) (*pb_common.HeroFull, error) {

	ads := make([]*pb_common.HeroItem, 0, len(hf.Ads))
	for _, ad := range hf.Ads {
		ads = append(ads, ConvertEntityHeroItemToCommon(&ad))
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
		Ads:              ads,
		ProductsFeatured: prdsFeatured,
	}, nil
}
