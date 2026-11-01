package app

import (
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/jessevdk/go-flags"
	"testing"
)

func TestPruningWorkerConfigurationOnEveryNetwork(t *testing.T) {
	for _, params := range []*dagconfig.Params{&dagconfig.MainnetParams, &dagconfig.TestnetParams, &dagconfig.SimnetParams, &dagconfig.DevnetParams} {
		for _, diagnostics := range []bool{false, true} {
			cfg := &config.Config{Flags: &config.Flags{}}
			cfg.ActiveNetParams = params
			cfg.PruningDiagnostics = diagnostics
			got, err := newConsensusConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got.PruningDiagnostics != diagnostics || !got.DeferPruningPointUTXOSetUpdate {
				t.Fatalf("lost diagnostics/owner configuration: %+v", got)
			}
		}
	}
}

func TestPruningDiagnosticsFlag(t *testing.T) {
	cfg := &config.Flags{}
	if cfg.PruningDiagnostics {
		t.Fatal("diagnostics enabled by default")
	}
	if _, err := flags.NewParser(cfg, flags.None).ParseArgs([]string{"--pruning-diagnostics"}); err != nil {
		t.Fatal(err)
	}
	if !cfg.PruningDiagnostics {
		t.Fatal("diagnostics flag lost")
	}
}
