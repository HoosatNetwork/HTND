package router

import (
	"sync"
	"time"

	"github.com/Hoosat-Oy/HTND/app/protocol/protocolerrors"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/pkg/errors"
)

const (
	// DefaultMaxMessages is the default capacity for a route with a capacity defined
	DefaultMaxMessages = 5000
)

var (
	// ErrTimeout signifies that one of the router functions had a timeout.
	ErrTimeout = protocolerrors.New(false, "timeout expired")

	// ErrRouteClosed indicates that a route was closed while reading/writing.
	ErrRouteClosed = errors.New("route is closed")

	// ErrRouteCapacityReached indicates that route's capacity has been reached
	ErrRouteCapacityReached = protocolerrors.New(false, "route capacity has been reached")
)

func defaultOnRouteCapacityReachedHandler(route *Route, message appmessage.Message) {
	log.Infof("Route '%s' is full (%d/%d). Dropping outgoing '%s' message",
		route.name, len(route.channel), route.capacity, message.Command())
}

// Route represents an incoming or outgoing Router route
type Route struct {
	name       string
	channel    chan appmessage.Message
	closedChan chan struct{}
	// closed and closeLock are used to protect us from writing to a closed channel
	// reads use the channel's built-in mechanism to check if the channel is closed
	closed                   bool
	closeLock                sync.Mutex
	capacity                 int
	onCapacityReachedHandler OnRouteCapacityReachedHandler
}

// NewRoute create a new Route
func NewRoute(name string) *Route {
	return NewRouteWithCapacity(name, DefaultMaxMessages)
	// return &Route{
	// 	name:     name,
	// 	channel:  make(chan appmessage.Message),
	// 	closed:   false,
	// 	capacity: -1,
	// }
}

func NewRouteWithCapacity(name string, capacity int) *Route {
	return &Route{
		name:                     name,
		channel:                  make(chan appmessage.Message, capacity),
		closedChan:               make(chan struct{}),
		closed:                   false,
		capacity:                 capacity,
		onCapacityReachedHandler: defaultOnRouteCapacityReachedHandler,
	}
}

// Reset prepares a route for reuse without reallocating the underlying channel.
// It drains any queued messages, reopens the route, and resets its name and capacity handler.
func (r *Route) Reset(name string) {
	r.closeLock.Lock()
	defer r.closeLock.Unlock()

	r.name = name

	// Drain any leftover messages.
	for {
		select {
		case <-r.channel:
			continue
		default:
			goto drained
		}
	}

drained:
	r.closed = false
	r.closedChan = make(chan struct{})
	r.onCapacityReachedHandler = defaultOnRouteCapacityReachedHandler
}

// Enqueue enqueues a message to the Route
func (r *Route) Enqueue(message appmessage.Message) error {
	r.closeLock.Lock()
	if r.closed {
		r.closeLock.Unlock()
		return errors.WithStack(ErrRouteClosed)
	}
	if len(r.channel) == r.capacity {
		handler := r.onCapacityReachedHandler
		r.closeLock.Unlock()
		if handler != nil {
			handler(r, message)
		}
		return errors.Wrapf(ErrRouteCapacityReached, "route '%s' reached capacity of %d", r.name, r.capacity)
	}
	r.channel <- message
	r.closeLock.Unlock()
	return nil
}

// MaybeEnqueue enqueues a message to the route, but doesn't throw an error
// if it's closed or its capacity has been reached.
func (r *Route) MaybeEnqueue(message appmessage.Message) error {
	err := r.Enqueue(message)
	if errors.Is(err, ErrRouteClosed) {
		log.Infof("Couldn't send message to closed route '%s'", r.name)
		return nil
	}

	if errors.Is(err, ErrRouteCapacityReached) {
		return nil
	}

	return err
}

// Dequeue dequeues a message from the Route
func (r *Route) Dequeue() (appmessage.Message, error) {
	// Fast-path: if closed, don't dequeue any pending messages.
	select {
	case <-r.closedChan:
		return nil, errors.Wrapf(ErrRouteClosed, "route '%s' is closed", r.name)
	default:
	}

	select {
	case <-r.closedChan:
		return nil, errors.Wrapf(ErrRouteClosed, "route '%s' is closed", r.name)
	case message := <-r.channel:
		return message, nil
	}
}

// DequeueWithTimeout attempts to dequeue a message from the Route
// and returns an error if the given timeout expires first.
func (r *Route) DequeueWithTimeout(timeout time.Duration) (appmessage.Message, error) {
	select {
	case <-r.closedChan:
		return nil, errors.WithStack(ErrRouteClosed)
	case <-time.After(timeout):
		return nil, errors.Wrapf(ErrTimeout, "route '%s' got timeout after %s", r.name, timeout)
	case message := <-r.channel:
		return message, nil
	}
}

func (r *Route) DequeueWithTimeoutAndRetry(timeout time.Duration, maxRetries int) (appmessage.Message, error) {
	for range maxRetries {
		select {
		case <-r.closedChan:
			return nil, errors.WithStack(ErrRouteClosed)
		case <-time.After(timeout):
			continue
		case message := <-r.channel:
			return message, nil
		}
	}
	return nil, errors.Wrapf(ErrTimeout, "route '%s' got timeout after %s and %d retries", r.name, timeout, maxRetries)
}

// Close closes this route
func (r *Route) Close() {
	r.closeLock.Lock()
	defer r.closeLock.Unlock()

	if r.closed {
		return
	}

	r.closed = true
	close(r.closedChan)
}

// Name returns the route name.
func (r *Route) Name() string {
	return r.name
}

// Length returns the current number of queued messages.
func (r *Route) Length() int {
	return len(r.channel)
}

// Capacity returns the maximum number of queued messages.
func (r *Route) Capacity() int {
	return r.capacity
}

// SetOnCapacityReachedHandler registers a callback for queue saturation.
func (r *Route) SetOnCapacityReachedHandler(handler OnRouteCapacityReachedHandler) {
	r.closeLock.Lock()
	defer r.closeLock.Unlock()

	if handler == nil {
		handler = defaultOnRouteCapacityReachedHandler
	}
	r.onCapacityReachedHandler = handler
}
