package bucket

type Image struct {
	FullSize   string `json:"fullSize"`
	Thumbnail  string `json:"thumbnail"`
	Compressed string `json:"compressed"`
}

type MainImage struct {
	Image
	MetaImage string `json:"metaImage"`
}
