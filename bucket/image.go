package bucket

import "fmt"

type Image struct {
	FullSize   string `json:"fullSize"`
	Thumbnail  string `json:"thumbnail"`
	Compressed string `json:"compressed"`
}

func (i *Image) Validate() error {
	if i == nil {
		return fmt.Errorf("missing Image")
	}
	if len(i.FullSize) == 0 {
		return fmt.Errorf("missing Image FullSize")
	}
	if len(i.Thumbnail) == 0 {
		return fmt.Errorf("missing Image Thumbnail")
	}
	if len(i.Compressed) == 0 {
		return fmt.Errorf("missing Image Compressed")
	}
	return nil
}

type MainImage struct {
	Image
	MetaImage string `json:"metaImage"`
}

func (mi *MainImage) Validate() error {
	if mi == nil {
		return fmt.Errorf("missing MetaImage")
	}
	if len(mi.MetaImage) == 0 {
		return fmt.Errorf("missing Image MetaImage")
	}
	return mi.Image.Validate()
}
