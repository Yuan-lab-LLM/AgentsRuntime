package netbroker

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var dnsQuery = []byte{0x12, 0x34, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}

func dnsReply() []byte { b := append([]byte(nil), dnsQuery...); b[2] |= 0x80; return b }
func dnsUpstream(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		tcp.Close()
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		b := make([]byte, 65535)
		for {
			_, peer, err := udp.ReadFrom(b)
			if err != nil {
				return
			}
			calls.Add(1)
			udp.WriteTo(dnsReply(), peer)
		}
	}()
	go func() {
		defer wg.Done()
		for {
			conn, err := tcp.Accept()
			if err != nil {
				return
			}
			conn.SetDeadline(time.Now().Add(2 * time.Second))
			var prefix [2]byte
			if _, err = io.ReadFull(conn, prefix[:]); err == nil {
				b := make([]byte, int(binary.BigEndian.Uint16(prefix[:])))
				if _, err = io.ReadFull(conn, b); err == nil {
					calls.Add(1)
					response := dnsReply()
					binary.BigEndian.PutUint16(prefix[:], uint16(len(response)))
					conn.Write(append(prefix[:], response...))
				}
			}
			conn.Close()
		}
	}()
	t.Cleanup(func() { tcp.Close(); udp.Close(); wg.Wait() })
	return tcp.Addr().String(), calls
}
func dnsListeners(t *testing.T) (net.PacketConn, net.Listener) {
	t.Helper()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		udp.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { udp.Close(); tcp.Close() })
	return udp, tcp
}
func TestDNSFixedUpstreamUDPAndTCPFraming(t *testing.T) {
	upstream, calls := dnsUpstream(t)
	udp, tcp := dnsListeners(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeDNS(ctx, "127.0.0.1", upstream, udp, tcp) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("DNS workers did not join")
		}
	}()
	client, err := net.Dial("udp", udp.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(time.Second))
	client.Write(dnsQuery)
	b := make([]byte, 100)
	n, err := client.Read(b)
	if err != nil || !bytes.Equal(b[:n], dnsReply()) {
		t.Fatalf("UDP reply=%x err=%v", b[:n], err)
	}
	stream, err := net.Dial("tcp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(time.Second))
	for range 2 {
		var prefix [2]byte
		binary.BigEndian.PutUint16(prefix[:], uint16(len(dnsQuery)))
		stream.Write(append(prefix[:], dnsQuery...))
		if _, err = io.ReadFull(stream, prefix[:]); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, int(binary.BigEndian.Uint16(prefix[:])))
		if _, err = io.ReadFull(stream, response); err != nil || !bytes.Equal(response, dnsReply()) {
			t.Fatalf("TCP frame=%x err=%v", response, err)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("unexpected exchange count %d", calls.Load())
	}
}
func TestDNSWrongSourceAndCancellation(t *testing.T) {
	upstream, calls := dnsUpstream(t)
	udp, tcp := dnsListeners(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeDNS(ctx, "127.0.0.2", upstream, udp, tcp) }()
	client, err := net.Dial("udp", udp.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(100 * time.Millisecond))
	client.Write(dnsQuery)
	b := make([]byte, 20)
	if _, err = client.Read(b); err == nil {
		t.Fatal("unauthorized DNS UDP source answered")
	}
	stream, err := net.Dial("tcp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(time.Second))
	if _, err = stream.Read(b); err == nil {
		t.Fatal("unauthorized DNS TCP source accepted")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Fatal("DNS cancellation retained workers")
	}
	if calls.Load() != 0 {
		t.Fatalf("wrong-source queries reached upstream %d times", calls.Load())
	}
}
func TestDNSCancelReleasesPartialTCPFrame(t *testing.T) {
	upstream, _ := dnsUpstream(t)
	udp, tcp := dnsListeners(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeDNS(ctx, "127.0.0.1", upstream, udp, tcp) }()
	stream, err := net.Dial("tcp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(time.Second))
	stream.Write([]byte{0, 12, 1})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Fatal("partial DNS TCP frame blocked shutdown")
	}
	var b [1]byte
	if _, err := stream.Read(b[:]); err == nil {
		t.Fatal("canceled DNS connection stayed open")
	}
}

func TestDNSBoundsConcurrentExchangesAndCancelsPending(t *testing.T) {
	upstream, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	arrived := make(chan struct{}, 32)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		var b [100]byte
		for {
			if _, _, err := upstream.ReadFrom(b[:]); err != nil {
				return
			}
			arrived <- struct{}{}
		}
	}()
	defer func() { upstream.Close(); <-readerDone }()
	udp, tcp := dnsListeners(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeDNS(ctx, "127.0.0.1", upstream.LocalAddr().String(), udp, tcp) }()
	client, err := net.Dial("udp", udp.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for range 17 {
		client.Write(dnsQuery)
	}
	for range 16 {
		select {
		case <-arrived:
		case <-time.After(time.Second):
			t.Fatal("DNS exchange did not arrive")
		}
	}
	select {
	case <-arrived:
		t.Error("DNS concurrency exceeded 16")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending DNS exchanges outlived context")
	}
}
