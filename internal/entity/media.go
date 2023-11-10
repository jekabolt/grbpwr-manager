package entity

type Media struct {
	Id int `db:"id"`
	MediaInsert
}

type MediaInsert struct {
	FullSize   string `db:"full_size"`
	Thumbnail  string `db:"thumbnail"`
	Compressed string `db:"compressed"`
	Finalized  bool   `db:"finalized"`
}
