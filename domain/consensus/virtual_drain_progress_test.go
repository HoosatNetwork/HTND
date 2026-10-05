package consensus

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func newTestVirtualDrainProgress(startDAAScore uint64) (*virtualDrainProgress, *time.Time, *[]string) {
	clock := time.Unix(1_000_000, 0)
	var lines []string
	progress := &virtualDrainProgress{
		reason:        "before inserting a block",
		start:         clock,
		lastReport:    clock,
		startDAAScore: startDAAScore,
		now:           func() time.Time { return clock },
		logf: func(format string, args ...any) {
			lines = append(lines, fmt.Sprintf(format, args...))
		},
	}
	return progress, &clock, &lines
}

// TestVirtualDrainProgressStaysQuietForAShortDrain pins that a drain finishing within one interval
// logs nothing: most drains are a single chunk, and they happen on every relayed block.
func TestVirtualDrainProgressStaysQuietForAShortDrain(t *testing.T) {
	progress, clock, lines := newTestVirtualDrainProgress(1000)
	for i := 1; i <= 3; i++ {
		*clock = clock.Add(time.Second)
		progress.chunkResolved(1000+uint64(i)*100, 5000)
	}
	progress.finished(1300)
	if len(*lines) != 0 {
		t.Fatalf("a drain shorter than the interval logged %q", *lines)
	}
}

// TestVirtualDrainProgressReportsALongDrain pins the reporting of a drain that runs for minutes: one
// line per interval with the estimated percentage and rate, and a closing line.
func TestVirtualDrainProgressReportsALongDrain(t *testing.T) {
	progress, clock, lines := newTestVirtualDrainProgress(1000)
	daaScore := uint64(1000)
	for i := 0; i < 30; i++ {
		*clock = clock.Add(time.Second)
		daaScore += 50
		progress.chunkResolved(daaScore, 4000)
	}
	progress.finished(daaScore)

	if len(*lines) != 4 {
		t.Fatalf("30 s of chunks with a 10 s interval logged %d lines, want 3 progress lines and 1 closing line: %q",
			len(*lines), *lines)
	}
	if want := "DAA score 1500 of about 4000 (17%), 10 chunks in 10s, 50 DAA/s"; !strings.Contains((*lines)[0], want) {
		t.Fatalf("first progress line %q does not contain %q", (*lines)[0], want)
	}
	if want := "Resolved pending virtual before inserting a block: DAA score 1000 to 2500, 30 chunks in 30s"; (*lines)[3] != want {
		t.Fatalf("closing line %q, want %q", (*lines)[3], want)
	}
}

// TestVirtualDrainProgressWithoutATarget pins that an unknown or stale target drops the percentage
// instead of printing a meaningless one.
func TestVirtualDrainProgressWithoutATarget(t *testing.T) {
	for _, target := range []uint64{0, 900} {
		progress, clock, lines := newTestVirtualDrainProgress(1000)
		*clock = clock.Add(virtualDrainProgressInterval)
		progress.chunkResolved(1200, target)
		if len(*lines) != 1 || strings.Contains((*lines)[0], "%") || !strings.Contains((*lines)[0], "DAA score 1200,") {
			t.Fatalf("target %d: got %q, want one line with the score and no percentage", target, *lines)
		}
	}
}
