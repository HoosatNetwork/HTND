package router

import (
	"testing"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/pkg/errors"
)

func TestNewRouteSetsDefaultCapacityHandler(t *testing.T) {
	route := NewRoute("test route")

	if route.onCapacityReachedHandler == nil {
		t.Fatal("expected new route to have a default capacity handler")
	}
}

func TestRouteEnqueueCallsCapacityHandler(t *testing.T) {
	route := NewRouteWithCapacity("test route", 1)
	firstMessage := &appmessage.MsgPing{Nonce: 1}
	secondMessage := &appmessage.MsgPing{Nonce: 2}

	if err := route.Enqueue(firstMessage); err != nil {
		t.Fatalf("expected first enqueue to succeed: %s", err)
	}

	called := 0
	var gotRoute *Route
	var gotMessage appmessage.Message
	route.SetOnCapacityReachedHandler(func(route *Route, message appmessage.Message) {
		called++
		gotRoute = route
		gotMessage = message
	})

	err := route.Enqueue(secondMessage)
	if !errors.Is(err, ErrRouteCapacityReached) {
		t.Fatalf("expected ErrRouteCapacityReached, got %v", err)
	}
	if called != 1 {
		t.Fatalf("expected capacity handler to be called once, got %d", called)
	}
	if gotRoute != route {
		t.Fatalf("expected handler route %p, got %p", route, gotRoute)
	}
	if gotMessage != secondMessage {
		t.Fatalf("expected handler message %p, got %p", secondMessage, gotMessage)
	}
}

func TestRouteMaybeEnqueueReturnsNilWhenFull(t *testing.T) {
	route := NewRouteWithCapacity("test route", 1)
	if err := route.Enqueue(&appmessage.MsgPing{Nonce: 1}); err != nil {
		t.Fatalf("expected first enqueue to succeed: %s", err)
	}

	err := route.MaybeEnqueue(&appmessage.MsgPing{Nonce: 2})
	if err != nil {
		t.Fatalf("expected MaybeEnqueue to swallow capacity error, got %v", err)
	}
}

func TestSetOnCapacityReachedHandlerNilRestoresDefault(t *testing.T) {
	route := NewRoute("test route")
	route.onCapacityReachedHandler = nil

	route.SetOnCapacityReachedHandler(nil)

	if route.onCapacityReachedHandler == nil {
		t.Fatal("expected nil handler assignment to restore the default handler")
	}
}
