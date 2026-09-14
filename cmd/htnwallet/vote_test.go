package main

import "testing"

// TestValidateVoteConfig pins that invalid vote flags are errors. A missing --from-address used to be indexed
// before any check and panicked, and the checks returned errors.Wrap of a nil error - nil - so an invalid vote
// exited successfully without doing anything.
func TestValidateVoteConfig(t *testing.T) {
	tests := map[string]struct {
		conf    voteConfig
		wantErr bool
	}{
		"valid":                {voteConfig{FromAddresses: []string{"hoosat:addr"}, PollID: "poll", Votes: []int{0}}, false},
		"no from address flag": {voteConfig{PollID: "poll", Votes: []int{0}}, true},
		"empty from address":   {voteConfig{FromAddresses: []string{""}, PollID: "poll", Votes: []int{0}}, true},
		"no poll id":           {voteConfig{FromAddresses: []string{"hoosat:addr"}, Votes: []int{0}}, true},
		"no votes":             {voteConfig{FromAddresses: []string{"hoosat:addr"}, PollID: "poll"}, true},
		"vote -1":              {voteConfig{FromAddresses: []string{"hoosat:addr"}, PollID: "poll", Votes: []int{-1}}, true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateVoteConfig(&test.conf)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateVoteConfig error = %v, want error %t", err, test.wantErr)
			}
		})
	}
}
