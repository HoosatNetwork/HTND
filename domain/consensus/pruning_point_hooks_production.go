//go:build !pruningtest

package consensus

type pruningJournalTestConfig struct{}

func (*Config) pruningJournalHook() func(string) { return nil }

func (*Config) legacyPruningFixture() bool { return false }
