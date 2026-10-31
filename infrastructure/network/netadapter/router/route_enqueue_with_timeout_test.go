package router

import (
	"sync"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/pkg/errors"
)

func fillRoute(t *testing.T, route *Route) {
	t.Helper()
	for i := 0; i < route.Capacity(); i++ {
		if err := route.Enqueue(&appmessage.MsgPing{Nonce: uint64(i)}); err != nil {
			t.Fatalf("Enqueue %d: %+v", i, err)
		}
	}
}

// TestEnqueueWithTimeoutWaitsForTheReader pins that EnqueueWithTimeout on a full route waits for the reader
// instead of failing like Enqueue, and goes through once the reader has drained the route.
func TestEnqueueWithTimeoutWaitsForTheReader(t *testing.T) {
	route := NewRouteWithCapacity("test route", 4)
	fillRoute(t, route)

	done := make(chan error, 1)
	go func() { done <- route.EnqueueWithTimeout(&appmessage.MsgPing{Nonce: 100}, 10*time.Second) }()
	select {
	case err := <-done:
		t.Fatalf("EnqueueWithTimeout returned (%v) while the route was full", err)
	case <-time.After(100 * time.Millisecond):
	}

	for i := 0; i < route.Capacity(); i++ {
		if _, err := route.Dequeue(); err != nil {
			t.Fatalf("Dequeue: %+v", err)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EnqueueWithTimeout: %+v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EnqueueWithTimeout did not return after the reader drained the route")
	}
	message, err := route.Dequeue()
	if err != nil {
		t.Fatalf("Dequeue: %+v", err)
	}
	if message.(*appmessage.MsgPing).Nonce != 100 {
		t.Fatalf("got message %v, want the one EnqueueWithTimeout waited with", message)
	}
}

// TestEnqueueWithTimeoutTimesOut pins that EnqueueWithTimeout gives up once the route has not been drained within
// the timeout, with the same non-ban ErrRouteCapacityReached and capacity handler call as Enqueue on a full route.
func TestEnqueueWithTimeoutTimesOut(t *testing.T) {
	route := NewRouteWithCapacity("test route", 2)
	fillRoute(t, route)
	called := 0
	route.SetOnCapacityReachedHandler(func(*Route, appmessage.Message) { called++ })

	const timeout = 50 * time.Millisecond
	start := time.Now()
	err := route.EnqueueWithTimeout(&appmessage.MsgPing{}, timeout)
	if !errors.Is(err, ErrRouteCapacityReached) {
		t.Fatalf("got %v, want ErrRouteCapacityReached", err)
	}
	if elapsed := time.Since(start); elapsed < timeout {
		t.Fatalf("returned after %s, before the %s timeout", elapsed, timeout)
	}
	if called != 1 {
		t.Fatalf("capacity handler called %d times, want 1", called)
	}
}

// TestEnqueueWithTimeoutReturnsWhenTheRouteCloses pins that a wait on a full route ends with ErrRouteClosed, and
// without a panic, when the route is closed meanwhile, and that a closed route is refused at once.
func TestEnqueueWithTimeoutReturnsWhenTheRouteCloses(t *testing.T) {
	route := NewRouteWithCapacity("test route", 2)
	fillRoute(t, route)

	done := make(chan error, 1)
	go func() { done <- route.EnqueueWithTimeout(&appmessage.MsgPing{}, 10*time.Second) }()
	time.Sleep(50 * time.Millisecond)
	route.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRouteClosed) {
			t.Fatalf("got %v, want ErrRouteClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EnqueueWithTimeout did not return after the route was closed")
	}

	if err := route.EnqueueWithTimeout(&appmessage.MsgPing{}, 10*time.Second); !errors.Is(err, ErrRouteClosed) {
		t.Fatalf("got %v on a closed route, want ErrRouteClosed", err)
	}
}

// TestEnqueueWithTimeoutConcurrentWithClose runs waiting senders, a reader and Close concurrently, for the race
// detector: every send either goes through or ends with ErrRouteClosed, and none panics.
func TestEnqueueWithTimeoutConcurrentWithClose(t *testing.T) {
	for round := 0; round < 20; round++ {
		route := NewRouteWithCapacity("test route", 8)
		var senders sync.WaitGroup
		for i := 0; i < 8; i++ {
			senders.Add(1)
			go func() {
				defer senders.Done()
				for j := 0; j < 100; j++ {
					err := route.EnqueueWithTimeout(&appmessage.MsgPing{Nonce: uint64(j)}, 10*time.Second)
					if errors.Is(err, ErrRouteClosed) {
						return
					}
					if err != nil {
						t.Errorf("EnqueueWithTimeout: %+v", err)
						return
					}
				}
			}()
		}
		go func() {
			for {
				if _, err := route.Dequeue(); err != nil {
					return
				}
			}
		}()
		time.Sleep(time.Duration(round) * time.Millisecond)
		route.Close()
		senders.Wait()
	}
}

// TestEnqueueWithTimeoutLeavesRoomForEnqueue pins that a sender waiting in EnqueueWithTimeout does not keep the
// route full: the other flows of the peer share it through the non-blocking Enqueue, which fails on a full route and
// disconnects the peer, so a long answer must leave them room while it drains.
func TestEnqueueWithTimeoutLeavesRoomForEnqueue(t *testing.T) {
	route := NewRouteWithCapacity("test route", 10)
	defer route.Close()

	go func() {
		for i := 0; i < 1000; i++ {
			if err := route.EnqueueWithTimeout(&appmessage.MsgPing{Nonce: uint64(i)}, 10*time.Second); err != nil {
				return
			}
		}
	}()
	go func() {
		for {
			time.Sleep(time.Millisecond)
			if _, err := route.Dequeue(); err != nil {
				return
			}
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for 2*route.Length() < route.Capacity() {
		if time.Now().After(deadline) {
			t.Fatal("the waiting sender did not fill the route")
		}
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < route.Capacity()/2-1; i++ {
		if err := route.Enqueue(&appmessage.MsgPong{Nonce: uint64(i)}); err != nil {
			t.Fatalf("Enqueue %d while EnqueueWithTimeout was waiting: %+v", i, err)
		}
	}
}
