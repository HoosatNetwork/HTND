package router

import (
	"fmt"
	"sync"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/pkg/errors"
)

const outgoingRouteMaxMessages = appmessage.MaxInvPerMsg + DefaultMaxMessages

// OnRouteCapacityReachedHandler is called when a route cannot accept another message.
type OnRouteCapacityReachedHandler func(route *Route, message appmessage.Message)

// Router routes messages by type to their respective
// input channels
type Router struct {
	incomingRoutes     map[appmessage.MessageCommand]*Route
	incomingRoutesLock sync.RWMutex

	reusableIncomingRoutes []*Route
	unmappedRoutes         map[*Route]struct{}

	outgoingRoute *Route
}

// NewRouter creates a new empty router
func NewRouter(name string) *Router {
	router := Router{
		incomingRoutes: make(map[appmessage.MessageCommand]*Route),
		unmappedRoutes: make(map[*Route]struct{}),
		outgoingRoute:  NewRouteWithCapacity(fmt.Sprintf("%s - outgoing", name), outgoingRouteMaxMessages),
	}
	return &router
}

// Reset prepares the router for reuse. It clears all incoming routes (while retaining
// their underlying buffers for reuse) and resets the outgoing route without reallocating
// its buffered channel.
func (r *Router) Reset(name string) {
	r.incomingRoutesLock.Lock()
	defer r.incomingRoutesLock.Unlock()

	// Move unique incoming routes into the reusable pool.
	uniqueRoutes := make(map[*Route]struct{})
	for _, route := range r.incomingRoutes {
		uniqueRoutes[route] = struct{}{}
	}
	for route := range r.unmappedRoutes {
		uniqueRoutes[route] = struct{}{}
	}
	for route := range uniqueRoutes {
		route.Close()
		r.reusableIncomingRoutes = append(r.reusableIncomingRoutes, route)
	}

	// Clear the incoming routes map.
	for messageType := range r.incomingRoutes {
		delete(r.incomingRoutes, messageType)
	}
	for route := range r.unmappedRoutes {
		delete(r.unmappedRoutes, route)
	}

	// Reset outgoing route in-place.
	r.outgoingRoute.Reset(fmt.Sprintf("%s - outgoing", name))
}

// AddIncomingRoute registers the messages of types `messageTypes` to
// be routed to the given `route`
func (r *Router) AddIncomingRoute(name string, messageTypes []appmessage.MessageCommand) (*Route, error) {
	return r.AddIncomingRouteWithCapacity(name, DefaultMaxMessages, messageTypes)
}

// AddIncomingRouteWithCapacity registers the messages of types `messageTypes` to
// be routed to the given `route` with a capacity of `capacity`
func (r *Router) AddIncomingRouteWithCapacity(name string, capacity int, messageTypes []appmessage.MessageCommand) (*Route, error) {
	routeName := fmt.Sprintf("%s - incoming", name)

	r.incomingRoutesLock.Lock()
	defer r.incomingRoutesLock.Unlock()

	// Some one-time flows don't expect to receive any messages and therefore
	// register with an empty messageTypes slice. Historically this was valid and
	// returned a route that isn't mapped to any message command.
	if len(messageTypes) == 0 {
		route := r.getReusableIncomingRoute(routeName, capacity)
		if route == nil {
			route = NewRouteWithCapacity(routeName, capacity)
		} else {
			route.Reset(routeName)
		}
		r.unmappedRoutes[route] = struct{}{}
		return route, nil
	}

	// If all messageTypes already have routes, return the first existing one
	// without allocating anything.
	var existingRoute *Route
	allExist := true
	for _, messageType := range messageTypes {
		if route, ok := r.incomingRoutes[messageType]; ok {
			if existingRoute == nil {
				existingRoute = route
			}
			continue
		}
		allExist = false
	}
	if allExist {
		return existingRoute, nil
	}

	route := r.getReusableIncomingRoute(routeName, capacity)
	if route == nil {
		route = NewRouteWithCapacity(routeName, capacity)
	} else {
		route.Reset(routeName)
	}

	for _, messageType := range messageTypes {
		if _, ok := r.incomingRoutes[messageType]; ok {
			log.Debugf("a route for '%s' already exists", messageType)
			continue
		}
		r.incomingRoutes[messageType] = route
	}

	return route, nil
}

func (r *Router) getReusableIncomingRoute(name string, minCapacity int) *Route {
	// NOTE: caller must hold incomingRoutesLock.
	for i := len(r.reusableIncomingRoutes) - 1; i >= 0; i-- {
		candidate := r.reusableIncomingRoutes[i]
		if candidate == nil {
			continue
		}
		if candidate.capacity < minCapacity {
			continue
		}
		// Remove from slice.
		r.reusableIncomingRoutes[i] = r.reusableIncomingRoutes[len(r.reusableIncomingRoutes)-1]
		r.reusableIncomingRoutes = r.reusableIncomingRoutes[:len(r.reusableIncomingRoutes)-1]
		candidate.name = name
		return candidate
	}
	return nil
}

// RemoveRoute unregisters the messages of types `messageTypes` from
// the router
func (r *Router) RemoveRoute(messageTypes []appmessage.MessageCommand) error {
	r.incomingRoutesLock.Lock()
	defer r.incomingRoutesLock.Unlock()

	// Track routes that might have become unused after we delete messageType mappings.
	possiblyUnused := make(map[*Route]struct{})
	for _, messageType := range messageTypes {
		route, ok := r.incomingRoutes[messageType]
		if !ok {
			log.Debugf("a route for '%s' does not exist", messageType)
			continue
		}
		delete(r.incomingRoutes, messageType)
		possiblyUnused[route] = struct{}{}
	}

	// If a route is no longer referenced by any message type, close and recycle it.
	for route := range possiblyUnused {
		stillUsed := false
		for _, existingRoute := range r.incomingRoutes {
			if existingRoute == route {
				stillUsed = true
				break
			}
		}
		if !stillUsed {
			route.Close()
			r.reusableIncomingRoutes = append(r.reusableIncomingRoutes, route)
		}
	}
	return nil
}

// ReleaseRoute recycles a route that isn't mapped to any message type anymore.
// This is primarily used for one-time flows that register with an empty
// messageTypes slice.
func (r *Router) ReleaseRoute(route *Route) {
	r.incomingRoutesLock.Lock()
	defer r.incomingRoutesLock.Unlock()

	if route == nil {
		return
	}

	if _, ok := r.unmappedRoutes[route]; ok {
		delete(r.unmappedRoutes, route)
		route.Close()
		r.reusableIncomingRoutes = append(r.reusableIncomingRoutes, route)
	}
}

// EnqueueIncomingMessage enqueues the given message to the
// appropriate route
func (r *Router) EnqueueIncomingMessage(message appmessage.Message) error {
	route, ok := r.incomingRoute(message.Command())
	if !ok {
		return errors.Errorf("a route for '%s' does not exist", message.Command())
	}
	return route.Enqueue(message)
}

// OutgoingRoute returns the outgoing route
func (r *Router) OutgoingRoute() *Route {
	return r.outgoingRoute
}

// Close shuts down the router by closing all registered
// incoming routes and the outgoing route
func (r *Router) Close() {
	r.incomingRoutesLock.Lock()
	defer r.incomingRoutesLock.Unlock()

	incomingRoutes := make(map[*Route]struct{})
	for _, route := range r.incomingRoutes {
		incomingRoutes[route] = struct{}{}
	}
	for route := range r.unmappedRoutes {
		incomingRoutes[route] = struct{}{}
	}
	for route := range incomingRoutes {
		route.Close()
	}
	r.outgoingRoute.Close()
}

func (r *Router) incomingRoute(messageType appmessage.MessageCommand) (*Route, bool) {
	r.incomingRoutesLock.RLock()
	defer r.incomingRoutesLock.RUnlock()

	route, ok := r.incomingRoutes[messageType]
	return route, ok
}

func (r *Router) doesIncomingRouteExist(messageType appmessage.MessageCommand) bool {
	r.incomingRoutesLock.RLock()
	defer r.incomingRoutesLock.RUnlock()

	_, ok := r.incomingRoutes[messageType]
	return ok
}

func (r *Router) setIncomingRoute(messageType appmessage.MessageCommand, route *Route) {
	r.incomingRoutesLock.Lock()
	defer r.incomingRoutesLock.Unlock()

	r.incomingRoutes[messageType] = route
}

func (r *Router) deleteIncomingRoute(messageType appmessage.MessageCommand) {
	r.incomingRoutesLock.Lock()
	defer r.incomingRoutesLock.Unlock()

	delete(r.incomingRoutes, messageType)
}
