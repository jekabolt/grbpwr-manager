package usdt

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type Config struct {
	Addresses []string `mapstructure:"addresses"`
	Node      string   `mapstructure:"node"`
}

type Processor struct {
	c   *Config
	rep dependency.Repository
}

func New(c *Config, rep dependency.Repository) *Processor {
	return &Processor{
		c:   c,
		rep: rep,
	}
}

func (p *Processor) GetOrderInvoice(ctx context.Context, orderUUID string) (*entity.Payment, error) {

	return &entity.Payment{}, nil
}

// func (p *Processor) GotPayed(ctx context.Context, orderUUID string) (bool, error) {
// 	return "", nil
// }
