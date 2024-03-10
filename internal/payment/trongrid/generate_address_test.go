package trongrid

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

const (
	addressFrom = "TMknEhd2ASboQ1LCFZfvJTufSLHEL9S5xW"
	addressTo   = "TULivK5zBnouAaKyt9hxTHJQspPHNpVoKV"
)

func TestGetTransactionsShasta(t *testing.T) {
	tg := New(&Config{
		APIKey:  "test",
		BaseURL: ShastaTestnet,
		Timeout: time.Minute,
	})

	res, err := tg.GetAddressTransactions(addressTo)
	assert.NoError(t, err)

	bs, err := json.Marshal(res)
	assert.NoError(t, err)

	fmt.Println(string(bs))

}
