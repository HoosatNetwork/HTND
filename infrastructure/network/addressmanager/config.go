package addressmanager

import (
	"net"
	"time"

	"github.com/Hoosat-Oy/HTND/infrastructure/config"
)

// Config is a descriptor which specifies the AddressManager instance configuration.
type Config struct {
	AcceptUnroutable bool
	BanDuration      time.Duration
	DefaultPort      string
	ExternalIPs      []string
	Listeners        []string
	Lookup           func(string) ([]net.IP, error)
}

// NewConfig returns a new address manager Config.
func NewConfig(cfg *config.Config) *Config {
	return &Config{
		AcceptUnroutable: cfg.NetParams().AcceptUnroutable,
		BanDuration:      cfg.BanDuration,
		DefaultPort:      cfg.NetParams().DefaultPort,
		ExternalIPs:      cfg.ExternalIPs,
		Listeners:        cfg.Listeners,
		Lookup:           cfg.Lookup,
	}
}
