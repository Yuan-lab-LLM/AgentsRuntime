package netbroker

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"
)

var errDNS = errors.New("netbroker: DNS unavailable")

const dnsTimeout = 2 * time.Second

// ServeDNS takes ownership of both listeners and joins all socket workers on
// cancellation. upstream must be a trusted literal IP:port from outer resolv.conf;
// this function never reads business DNS configuration or opens other targets.
func ServeDNS(parent context.Context, childIP, upstream string, udp net.PacketConn, tcp net.Listener) error {
	child, err := netip.ParseAddr(childIP)
	host, _, addressErr := net.SplitHostPort(upstream)
	upstreamIP, ipErr := netip.ParseAddr(host)
	if err != nil || child.Zone() != "" || child.IsUnspecified() || child.IsMulticast() || addressErr != nil || ipErr != nil || upstreamIP.Zone() != "" || !validTarget(upstream) || udp == nil || tcp == nil {
		return errDNS
	}
	child = child.Unmap()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var mu sync.Mutex
	active := map[net.Conn]bool{}
	stop := joinedAfter(ctx, func() {
		udp.Close()
		tcp.Close()
		mu.Lock()
		defer mu.Unlock()
		for conn := range active {
			conn.Close()
		}
	})
	defer stop()
	failures := make(chan error, 1)
	fail := func() {
		if ctx.Err() == nil {
			select {
			case failures <- errDNS:
			default:
			}
			cancel()
		}
	}
	limit := make(chan struct{}, 16)
	var loops, workers sync.WaitGroup
	loops.Add(2)
	go func() {
		defer loops.Done()
		packet := make([]byte, 65535)
		for {
			n, peer, err := udp.ReadFrom(packet)
			if err != nil {
				fail()
				return
			}
			if n < 12 || !samePeer(peer.String(), child) {
				continue
			}
			select {
			case limit <- struct{}{}:
			default:
				continue
			}
			query := append([]byte(nil), packet[:n]...)
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-limit }()
				response, err := dnsExchange(ctx, "udp", upstream, query)
				if err == nil {
					udp.WriteTo(response, peer)
				}
			}()
		}
	}()
	go func() {
		defer loops.Done()
		for {
			conn, err := tcp.Accept()
			if err != nil {
				fail()
				return
			}
			if !samePeer(conn.RemoteAddr().String(), child) {
				conn.Close()
				continue
			}
			select {
			case limit <- struct{}{}:
			default:
				conn.Close()
				continue
			}
			mu.Lock()
			if ctx.Err() != nil {
				mu.Unlock()
				conn.Close()
				<-limit
				return
			}
			active[conn] = true
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { conn.Close(); mu.Lock(); delete(active, conn); mu.Unlock(); <-limit }()
				for {
					conn.SetReadDeadline(time.Now().Add(dnsTimeout))
					query, err := readDNSFrame(conn)
					if err != nil {
						return
					}
					response, err := dnsExchange(ctx, "tcp", upstream, query)
					if err != nil {
						return
					}
					conn.SetWriteDeadline(time.Now().Add(dnsTimeout))
					if writeDNSFrame(conn, response) != nil {
						return
					}
				}
			}()
		}
	}()
	<-ctx.Done()
	loops.Wait()
	workers.Wait()
	select {
	case err := <-failures:
		return err
	default:
		return nil
	}
}
func readDNSFrame(reader io.Reader) ([]byte, error) {
	var size [2]byte
	if _, err := io.ReadFull(reader, size[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(size[:]))
	if n < 12 {
		return nil, errDNS
	}
	packet := make([]byte, n)
	if _, err := io.ReadFull(reader, packet); err != nil {
		return nil, err
	}
	return packet, nil
}
func writeDNSFrame(writer io.Writer, packet []byte) error {
	if len(packet) < 12 || len(packet) > 65535 {
		return errDNS
	}
	frame := make([]byte, 2+len(packet))
	binary.BigEndian.PutUint16(frame, uint16(len(packet)))
	copy(frame[2:], packet)
	_, err := io.Copy(writer, bytes.NewReader(frame))
	return err
}
func dnsExchange(parent context.Context, network, upstream string, query []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, dnsTimeout)
	defer cancel()
	conn, err := (&net.Dialer{Timeout: dnsTimeout}).DialContext(ctx, network, upstream)
	if err != nil {
		return nil, errDNS
	}
	defer conn.Close()
	stop := joinedAfter(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	var response []byte
	if network == "tcp" {
		if writeDNSFrame(conn, query) != nil {
			return nil, errDNS
		}
		response, err = readDNSFrame(conn)
	} else {
		if _, err := conn.Write(query); err != nil {
			return nil, errDNS
		}
		packet := make([]byte, 65535)
		var n int
		n, err = conn.Read(packet)
		response = packet[:n]
	}
	if err != nil || ctx.Err() != nil || len(response) < 12 || response[0] != query[0] || response[1] != query[1] {
		return nil, errDNS
	}
	return response, nil
}
