package utxobaseline

import (
	"sync"

	"github.com/HoosatNetwork/HTND/v2/domain"
)

var (
	mu    sync.RWMutex
	token = "/utxobase:unknown/"
	ready bool
)

func Token() string {
	mu.RLock()
	defer mu.RUnlock()
	return token
}

func Refresh(d domain.Domain) {
	next := "/utxobase:unknown/"
	if d != nil {
		health, err := d.Consensus().UTXOSetHealth()
		if err == nil && health != nil && health.Checked {
			if health.BaselineVerified {
				next = "/utxobase:ok/"
			} else {
				next = "/utxobase:bad/"
			}
		}
	}
	mu.Lock()
	token = next
	ready = true
	mu.Unlock()
}

func EnsureRefresh(d domain.Domain) {
	mu.RLock()
	ok := ready
	mu.RUnlock()
	if !ok {
		Refresh(d)
	}
}
