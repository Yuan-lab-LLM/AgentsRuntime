// Package netrelay implements the unprivileged network-namespace helper. It
// holds no credentials; the trusted parent authenticates its fixed broker path.
package netrelay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type Config struct {
	Broker       string `json:"broker"`
	IngressPorts []int  `json:"ingress_ports,omitempty"`
}

func validAddress(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) == nil {
		return false
	}
	p, err := strconv.Atoi(port)
	return err == nil && p > 0 && p <= 65535 && strconv.Itoa(p) == port
}

func (c Config) validate() error {
	if !validAddress(c.Broker) {
		return errors.New("netrelay: broker must be a literal IP and port")
	}
	if len(c.IngressPorts) > 64 {
		return errors.New("netrelay: too many ingress listeners")
	}
	seen := map[int]bool{}
	for _, p := range c.IngressPorts {
		if p < 1 || p > 65535 || seen[p] {
			return errors.New("netrelay: invalid ingress port")
		}
		seen[p] = true
	}
	return nil
}

// Run takes ownership of a transparent TCP listener and inherited private Unix
// listeners. Their order must match the trusted configuration's IngressPorts.
// No filesystem path or per-request ingress target is accepted from business.
func Run(ctx context.Context, cfg Config, transparent net.Listener, ingress []net.Listener) error {
	return serve(ctx, cfg, transparent, ingress, originalDestination)
}

func serve(ctx context.Context, cfg Config, transparent net.Listener, ingress []net.Listener, destination func(net.Conn) (string, error)) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if transparent == nil || len(ingress) != len(cfg.IngressPorts) {
		return errors.New("netrelay: listener/config mismatch")
	}
	for _, l := range ingress {
		if l == nil || l.Addr().Network() != "unix" {
			return errors.New("netrelay: ingress must be a private Unix listener")
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	listeners := append([]net.Listener{transparent}, ingress...)
	var mu sync.Mutex
	active := map[net.Conn]bool{}
	stop := context.AfterFunc(ctx, func() {
		for _, l := range listeners {
			l.Close()
		}
		mu.Lock()
		defer mu.Unlock()
		for c := range active {
			c.Close()
		}
	})
	defer stop()
	var acceptWG, workerWG sync.WaitGroup
	limit := make(chan struct{}, 256)
	failures := make(chan error, len(listeners))
	for index, l := range listeners {
		acceptWG.Add(1)
		go func() {
			defer acceptWG.Done()
			for {
				c, err := l.Accept()
				if err != nil {
					if ctx.Err() == nil {
						failures <- errors.New("netrelay: listener failed")
						cancel()
					}
					return
				}
				select {
				case limit <- struct{}{}:
				default:
					c.Close()
					continue
				}
				mu.Lock()
				if ctx.Err() != nil {
					mu.Unlock()
					c.Close()
					<-limit
					return
				}
				active[c] = true
				mu.Unlock()
				workerWG.Add(1)
				go func() {
					defer workerWG.Done()
					defer func() { mu.Lock(); delete(active, c); mu.Unlock(); c.Close(); <-limit }()
					if index == 0 {
						target, err := destination(c)
						if err != nil || !validAddress(target) {
							return
						}
						up, r, err := connect(ctx, cfg.Broker, target)
						if err != nil {
							return
						}
						defer up.Close()
						bridge(ctx, c, up, r)
					} else {
						port := strconv.Itoa(cfg.IngressPorts[index-1])
						d := net.Dialer{Timeout: 5 * time.Second}
						up, err := d.DialContext(ctx, "tcp", "127.0.0.1:"+port)
						if err != nil {
							up, err = d.DialContext(ctx, "tcp", "[::1]:"+port)
						}
						if err != nil {
							return
						}
						defer up.Close()
						bridge(ctx, c, up, up)
					}
				}()
			}
		}()
	}
	acceptWG.Wait()
	workerWG.Wait()
	select {
	case err := <-failures:
		return err
	default:
		return nil
	}
}

// The handshake reader is bounded while parsing headers, then becomes an
// unrestricted tunnel reader without discarding bytes already buffered.
type headerReader struct {
	r         io.Reader
	remaining int
	unlimited bool
}

func (r *headerReader) Read(p []byte) (int, error) {
	if !r.unlimited {
		if r.remaining == 0 {
			return 0, errors.New("netrelay: oversized broker response")
		}
		if len(p) > r.remaining {
			p = p[:r.remaining]
		}
	}
	n, e := r.r.Read(p)
	r.remaining -= n
	return n, e
}

func connect(ctx context.Context, broker, target string) (net.Conn, io.Reader, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := d.DialContext(ctx, "tcp", broker)
	if err != nil {
		return nil, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			c.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		return nil, nil, err
	}
	h := &headerReader{r: c, remaining: 32768}
	r := bufio.NewReader(h)
	response, err := http.ReadResponse(r, &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != http.StatusOK {
		return nil, nil, errors.New("netrelay: broker refused connection")
	}
	h.unlimited = true
	if err = c.SetDeadline(time.Time{}); err != nil {
		return nil, nil, err
	}
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	ok = true
	return c, r, nil
}

func bridge(ctx context.Context, a, b net.Conn, fromB io.Reader) {
	stop := context.AfterFunc(ctx, func() { a.Close(); b.Close() })
	defer stop()
	done := make(chan struct{})
	go func() {
		_, err := io.Copy(b, a)
		if err != nil {
			a.Close()
			b.Close()
		} else if c, ok := b.(interface{ CloseWrite() error }); ok {
			c.CloseWrite()
		}
		close(done)
	}()
	_, err := io.Copy(a, fromB)
	if err != nil {
		a.Close()
		b.Close()
	} else if c, ok := a.(interface{ CloseWrite() error }); ok {
		c.CloseWrite()
	}
	<-done
}
