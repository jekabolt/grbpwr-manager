package store

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/tidwall/buntdb"
)

func TestCreateDB(t *testing.T) {
	db, err := buntdb.Open("data.db")
	if err != nil {
		fmt.Println(" err ", err)
	}
	defer db.Close()

	db.CreateIndex("date", "*", buntdb.IndexJSON("date"))
	db.CreateIndex("price", "*", buntdb.IndexJSON("price.usd"))
	db.Update(func(tx *buntdb.Tx) error {
		tx.Set("1", `{"id":1,"date":1624440128,"mainImage":"http://kek.com/img.jpg","name":"item","price":{"usd":33,"rub":0,"byn":100},"availableSizes":{"xxs":0,"xs":0,"s":0,"m":0,"l":0,"xl":0,"xxl":0,"os":11},"description":"kek item","categories":["kek","item"],"imageURLs":["http://kek.com/img1.jpg","http://kek.com/img2.jpg"]}`, nil)
		tx.Set("2", `{"id":2,"date":1624440129,"mainImage":"http://lel.com/img.jpg","name":"item2","price":{"usd":34,"rub":0,"byn":102},"availableSizes":{"xxs":0,"xs":0,"s":0,"m":0,"l":0,"xl":0,"xxl":0,"os":10},"description":"lel item","categories":["lel","item"],"imageURLs":["http://lel.com/img1.jpg","http://lel.com/img2.jpg"]}`, nil)
		tx.Set("3", `{"id":1,"date":1624440128,"mainImage":"http://kek.com/img.jpg","name":"item","price":{"usd":33,"rub":0,"byn":100},"availableSizes":{"xxs":0,"xs":0,"s":0,"m":0,"l":0,"xl":0,"xxl":0,"os":11},"description":"kek item","categories":["kek","item"],"imageURLs":["http://kek.com/img1.jpg","http://kek.com/img2.jpg"]}`, nil)
		return nil
	})

	db.View(func(tx *buntdb.Tx) error {
		fmt.Println("Order by date")
		tx.Ascend("date", func(key, value string) bool {
			fmt.Printf("Ascend %s: %s\n", key, value)
			return true
		})
		return nil
	})

	fmt.Printf("\n\n\n")

	db.View(func(tx *buntdb.Tx) error {
		fmt.Println("Order by priceprice")
		tx.Ascend("price", func(key, value string) bool {
			fmt.Printf("Ascend %s: %s\n", key, value)
			return true
		})
		tx.Descend("price", func(key, value string) bool {
			fmt.Printf("Descend %s: %s\n", key, value)
			return true
		})
		return nil
	})
}

func FindAllInCategory() {

}

func TestCreat(t *testing.T) {
	b, _ := json.Marshal(Product{
		Id:          1,
		DateCreated: time.Now().UnixNano(),
		MainImage:   "http://kek.com/img.jpg",
		Name:        "item",
		Price: &Price{
			BYN: 100,
			USD: 33,
			RUB: 22,
			EUR: 11,
		},
		Description: "kek item",
		Categories: []string{
			"kek",
			"item",
		},
		ProductImages: []string{
			"http://kek.com/img1.jpg",
			"http://kek.com/img2.jpg",
		},
		AvailableSizes: &Size{
			OS: 11,
		},
	})
	fmt.Printf("%s", b)
}
