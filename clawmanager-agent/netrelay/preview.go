package netrelay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

func RunWithPreview(ctx context.Context, cfg Config, transparent net.Listener, ingress []net.Listener, preview net.Listener) error {
	return serve(ctx, cfg, transparent, ingress, originalDestination, preview)
}

// PreviewTarget accepts only canonical literal loopback addresses. DNS and all
// fixed helper/native service ports are excluded on both sides of the tunnel.
func PreviewTarget(target string, nativePorts []int) bool {
	if !validAddress(target) {
		return false
	}
	host, raw, _ := net.SplitHostPort(target)
	ip := net.ParseIP(host)
	if !ip.IsLoopback() {
		return false
	}
	port, _ := strconv.Atoi(raw)
	if port == 53 || port == 15000 || (port >= 31280 && port <= 31283) {
		return false
	}
	for _, p := range nativePorts {
		if port == p {
			return false
		}
	}
	return true
}

type bufferedConn struct {
	net.Conn
	reader io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *bufferedConn) CloseWrite() error {
	return c.Conn.(interface{ CloseWrite() error }).CloseWrite()
}

func servePreview(ctx context.Context, c net.Conn, nativePorts []int) {
	c.SetDeadline(time.Now().Add(5 * time.Second))
	limited := &headerReader{r: c, remaining: 1024}
	r := bufio.NewReader(limited)
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	fields := strings.Split(strings.TrimSuffix(line, "\r\n"), " ")
	if len(fields) != 3 || fields[0] != "CONNECT" || fields[2] != "HTTP/1.1" || !PreviewTarget(fields[1], nativePorts) {
		return
	}
	target := fields[1]
	host, err := r.ReadString('\n')
	if err != nil || host != "Host: "+target+"\r\n" {
		return
	}
	end, err := r.ReadString('\n')
	if err != nil || end != "\r\n" {
		return
	}
	up, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", target)
	if err != nil {
		return
	}
	defer up.Close()
	if _, err = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	c.SetDeadline(time.Time{})
	limited.unlimited = true
	bridge(ctx, &bufferedConn{Conn: c, reader: r}, up, up)
}

// DialPreview uses only the trusted parent's private socket path. The helper
// receives no management credentials or caller-selected namespace arguments.
func DialPreview(ctx context.Context, socket, target string) (net.Conn, error) {
	if !PreviewTarget(target, nil) {
		return nil, errors.New("netrelay: invalid preview target")
	}
	c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			c.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(c)
	line, err := reader.ReadString('\n')
	if err != nil || line != "HTTP/1.1 200 Connection Established\r\n" {
		return nil, errors.New("netrelay: preview refused")
	}
	line, err = reader.ReadString('\n')
	if err != nil || line != "\r\n" {
		return nil, errors.New("netrelay: invalid preview response")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	c.SetDeadline(time.Time{})
	ok = true
	return &bufferedConn{Conn: c, reader: reader}, nil
}
