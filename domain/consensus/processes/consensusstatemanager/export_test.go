package consensusstatemanager

// SetReverseUTXODiffsInterruptHook installs a hook that ReverseUTXODiffs calls after each diff it commits, with the
// number committed so far; an error from the hook stops the reversal there, as a crash between two of its commits
// would. It returns a function that restores the previous hook.
func SetReverseUTXODiffsInterruptHook(hook func(committedDiffs int) error) (restore func()) {
	previous := reverseUTXODiffsInterruptHook
	reverseUTXODiffsInterruptHook = hook
	return func() { reverseUTXODiffsInterruptHook = previous }
}
