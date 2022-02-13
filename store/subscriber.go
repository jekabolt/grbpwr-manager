package store

import (
	"encoding/json"
	"fmt"
	"net/mail"
)

type Subscriber struct {
	Email    string `json:"email"`
	IP       string `json:"ip,omitempty"`
	City     string `json:"city,omitempty"`
	Region   string `json:"region,omitempty"`
	Country  string `json:"country,omitempty"`
	Loc      string `json:"loc,omitempty"`
	Org      string `json:"org,omitempty"`
	Postal   string `json:"postal,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

func (nl *Subscriber) String() string {
	bs, _ := json.Marshal(nl)
	return string(bs)
}

func GetSubscriberFromString(ns string) *Subscriber {
	subscriber := &Subscriber{}
	json.Unmarshal([]byte(ns), subscriber)
	return subscriber
}

func (n *Subscriber) Validate() error {
	if _, err := mail.ParseAddress(n.Email); err != nil {
		return fmt.Errorf("bad email %s", err.Error())
	}
	return nil
}
