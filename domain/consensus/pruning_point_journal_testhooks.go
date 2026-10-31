//go:build pruningtest

package consensus

type pruningJournalTestConfig struct {
	PruningPointJournalTestHook func(string)
	PruningLegacyFixture        bool
}

func (c *Config) pruningJournalHook() func(string) { return c.PruningPointJournalTestHook }

func (c *Config) legacyPruningFixture() bool { return c.PruningLegacyFixture }
