package logger

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

type bufferWriteCloser struct {
	lock sync.Mutex
	bytes.Buffer
}

func (w *bufferWriteCloser) Write(p []byte) (int, error) {
	w.lock.Lock()
	defer w.lock.Unlock()
	return w.Buffer.Write(p)
}

func (w *bufferWriteCloser) Close() error { return nil }

func (w *bufferWriteCloser) String() string {
	w.lock.Lock()
	defer w.lock.Unlock()
	return w.Buffer.String()
}

// TestLoggingAfterCloseDoesNotPanic logs through a backend after it was closed, and closes it twice. Goroutines that
// are still running when htnd closes its log backend - all of them, when a shutdown timed out and panics.Exit closes
// it - can still log. That must not panic.
func TestLoggingAfterCloseDoesNotPanic(t *testing.T) {
	backend := NewBackend()
	writer := &bufferWriteCloser{}
	if err := backend.AddLogWriter(writer, LevelInfo); err != nil {
		t.Fatal(err)
	}
	if err := backend.Run(); err != nil {
		t.Fatal(err)
	}
	log := backend.Logger("TEST")
	log.SetLevel(LevelInfo)
	log.Infof("before Close")
	backend.Close()
	if !strings.Contains(writer.String(), "before Close") {
		t.Fatalf("a line logged before Close was not written: %q", writer.String())
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("logging after Close panicked: %v", r)
			}
		}()
		log.Infof("after Close")
		log.Info("after Close")
		backend.Close()
	}()
}

// TestLoggingConcurrentWithCloseDoesNotPanic logs from several goroutines while the backend is closed.
func TestLoggingConcurrentWithCloseDoesNotPanic(t *testing.T) {
	backend := NewBackend()
	if err := backend.AddLogWriter(&bufferWriteCloser{}, LevelInfo); err != nil {
		t.Fatal(err)
	}
	if err := backend.Run(); err != nil {
		t.Fatal(err)
	}
	log := backend.Logger("TEST")
	log.SetLevel(LevelInfo)

	panics := make(chan any, 8)
	var loggers sync.WaitGroup
	for range 8 {
		loggers.Add(1)
		go func() {
			defer loggers.Done()
			defer func() {
				if r := recover(); r != nil {
					panics <- r
				}
			}()
			for range 50 {
				log.Infof("logging while the backend closes")
			}
		}()
	}
	backend.Close()
	loggers.Wait()
	close(panics)
	for r := range panics {
		t.Fatalf("logging while the backend was closed panicked: %v", r)
	}
}

// heldWriter holds its first Write until released.
type heldWriter struct {
	bufferWriteCloser
	entered, release chan struct{}
	once             sync.Once
}

func (w *heldWriter) Write(p []byte) (int, error) {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	return w.bufferWriteCloser.Write(p)
}

// TestConcurrentCloseWaitsForTheDrain closes the backend twice concurrently while its writer goroutine is still
// writing a line logged before. panics.Exit closes the backend and then exits the process, and two of them can run
// at once: neither Close may return before the line has been written.
func TestConcurrentCloseWaitsForTheDrain(t *testing.T) {
	backend := NewBackend()
	writer := &heldWriter{entered: make(chan struct{}), release: make(chan struct{})}
	if err := backend.AddLogWriter(writer, LevelInfo); err != nil {
		t.Fatal(err)
	}
	if err := backend.Run(); err != nil {
		t.Fatal(err)
	}
	log := backend.Logger("TEST")
	log.SetLevel(LevelInfo)
	log.Infof("accepted before Close")
	<-writer.entered

	closed := make(chan struct{}, 2)
	for range 2 {
		go func() {
			backend.Close()
			closed <- struct{}{}
		}()
	}
	returned := 0
	select {
	case <-closed:
		returned++
		t.Errorf("a Close returned while a line logged before it was still being written")
	case <-time.After(100 * time.Millisecond):
	}

	close(writer.release)
	for ; returned < 2; returned++ {
		<-closed
	}
	if !strings.Contains(writer.String(), "accepted before Close") {
		t.Fatalf("a line logged before Close was not written: %q", writer.String())
	}
}
