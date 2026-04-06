package router

import (
	"testing"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
)

func TestAddIncomingRouteWithEmptyMessageTypesReturnsRoute(t *testing.T) {
	router := NewRouter("test")

	route, err := router.AddIncomingRouteWithCapacity("one-time", DefaultMaxMessages, nil)
	if err != nil {
		t.Fatalf("expected no error for empty messageTypes, got %v", err)
	}
	if route == nil {
		t.Fatal("expected route for empty messageTypes")
	}
}

func TestReleaseRouteReusesUnmappedRoute(t *testing.T) {
	router := NewRouter("test")

	firstRoute, err := router.AddIncomingRouteWithCapacity("one-time", DefaultMaxMessages, nil)
	if err != nil {
		t.Fatalf("expected no error creating first empty route, got %v", err)
	}

	if err := firstRoute.Enqueue(&appmessage.MsgPing{Nonce: 1}); err != nil {
		t.Fatalf("expected enqueue to succeed, got %v", err)
	}

	router.ReleaseRoute(firstRoute)

	secondRoute, err := router.AddIncomingRouteWithCapacity("one-time", DefaultMaxMessages, nil)
	if err != nil {
		t.Fatalf("expected no error creating second empty route, got %v", err)
	}

	if firstRoute != secondRoute {
		t.Fatal("expected unmapped route to be reused")
	}
	if secondRoute.Length() != 0 {
		t.Fatalf("expected reused route to be drained, got length %d", secondRoute.Length())
	}
	if secondRoute.Name() != "one-time - incoming" {
		t.Fatalf("expected reused route name to be reset, got %q", secondRoute.Name())
	}
	if err := secondRoute.Enqueue(&appmessage.MsgPing{Nonce: 2}); err != nil {
		t.Fatalf("expected reused route to accept new messages, got %v", err)
	}
	if secondRoute.Length() != 1 {
		t.Fatalf("expected reused route to contain one message, got %d", secondRoute.Length())
	}
	if _, err := secondRoute.Dequeue(); err != nil {
		t.Fatalf("expected dequeue on reused route to succeed, got %v", err)
	}
	if secondRoute.Length() != 0 {
		t.Fatalf("expected reused route to be empty after dequeue, got %d", secondRoute.Length())
	}
}

func TestResetReusesMappedRoutes(t *testing.T) {
	router := NewRouter("test")

	firstRoute, err := router.AddIncomingRoute("flow", []appmessage.MessageCommand{appmessage.CmdPing})
	if err != nil {
		t.Fatalf("expected no error creating mapped route, got %v", err)
	}

	router.Reset("test")

	secondRoute, err := router.AddIncomingRoute("flow", []appmessage.MessageCommand{appmessage.CmdPing})
	if err != nil {
		t.Fatalf("expected no error recreating mapped route after reset, got %v", err)
	}

	if firstRoute != secondRoute {
		t.Fatal("expected mapped route to be reused after reset")
	}
}
