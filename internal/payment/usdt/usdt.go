package usdt

import (
	"context"
	"fmt"
	"sync"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type Config struct {
	Addresses []string `mapstructure:"addresses"`
	Node      string   `mapstructure:"node"`
}

type Processor struct {
	c     *Config
	pm    *entity.PaymentMethod
	addrs map[string]bool
	mu    sync.Mutex
	rep   dependency.Repository
}

func New(c *Config, rep dependency.Repository) *Processor {
	pm, _ := rep.Cache().GetPaymentMethodsByName(entity.Usdt)

	addrs := make(map[string]bool, len(c.Addresses))
	for _, addr := range c.Addresses {
		addrs[addr] = true // Initialize all addresses as free
	}

	return &Processor{
		c:     c,
		pm:    &pm,
		rep:   rep,
		addrs: addrs,
	}
}

func (p *Processor) getFreeAddress() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for addr, free := range p.addrs {
		if free {
			p.addrs[addr] = false
			return addr, nil
		}
	}
	return "", fmt.Errorf("no free addresses available")
}

func (p *Processor) GetOrderInvoice(ctx context.Context, orderUUID string) (*entity.PaymentInsert, error) {

	pAddr, err := p.getFreeAddress()
	if err != nil {
		return nil, fmt.Errorf("can't get free address: %w", err)
	}

	pi, err := p.rep.Order().InsertOrderInvoice(ctx, orderUUID, pAddr, p.pm.ID)
	if err != nil {
		return nil, fmt.Errorf("can't insert order invoice: %w", err)
	}

	return pi, nil
}
