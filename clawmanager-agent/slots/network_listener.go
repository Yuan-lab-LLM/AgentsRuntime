package slots

import (
	"net"
	"sync"
)

// The session owns every accepted socket, including idle HTTP and hijacked
// streams. A shared budget covers all of its broker/native HTTP listeners.
type sessionHTTPListener struct {
	net.Listener
	budget chan struct{}
	mu     sync.Mutex
	closed bool
	active map[*sessionHTTPConnection]bool
}
type sessionHTTPConnection struct {
	net.Conn
	owner *sessionHTTPListener
	once  sync.Once
}

func newSessionHTTPListener(listener net.Listener, budget chan struct{}) *sessionHTTPListener {
	return &sessionHTTPListener{Listener: listener, budget: budget, active: map[*sessionHTTPConnection]bool{}}
}
func (l *sessionHTTPListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			conn.Close()
			return nil, net.ErrClosed
		}
		select {
		case l.budget <- struct{}{}:
			c := &sessionHTTPConnection{Conn: conn, owner: l}
			l.active[c] = true
			l.mu.Unlock()
			return c, nil
		default:
			l.mu.Unlock()
			conn.Close()
		}
	}
}
func (l *sessionHTTPListener) Close() error {
	l.mu.Lock()
	l.closed = true
	var active []*sessionHTTPConnection
	for c := range l.active {
		active = append(active, c)
	}
	l.mu.Unlock()
	err := l.Listener.Close()
	for _, c := range active {
		c.Close()
	}
	return err
}
func (c *sessionHTTPConnection) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.owner.mu.Lock(); delete(c.owner.active, c); <-c.owner.budget; c.owner.mu.Unlock() })
	return err
}
func (c *sessionHTTPConnection) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return c.Close()
}
