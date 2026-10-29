package slots

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/netbroker"
	"github.com/iamlovingit/clawmanager-agent/netguard"
	"github.com/iamlovingit/clawmanager-agent/netrelay"
	"github.com/iamlovingit/clawmanager-agent/sandbox"
)

type networkResources struct {
	cancel                context.CancelFunc
	credentials           *netbroker.Credentials
	broker                *netbroker.Broker
	guard                 *netguard.Handle
	servers               []*http.Server
	listeners             []io.Closer
	ingress               []*nativeIngress
	helper                *exec.Cmd
	helperDone            chan struct{}
	workers               sync.WaitGroup
	privateDir, publicDir string
	uncertain             bool
	httpConnections       chan struct{}
}

func prepareNetworkSession(parent context.Context, cfg gateway.Config, req gateway.CreateGatewayRequest, port int) (session *networkSession, result error) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return nil, fmt.Errorf("private network requires Linux trusted root")
	}
	token, err := validateNetworkAssignment(cfg, req, port)
	if err != nil {
		return nil, err
	}
	block := cfg.GatewayPortBlockSize
	upstreamData, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil, fmt.Errorf("trusted resolver unavailable")
	}
	upstream, err := networkDNSUpstream(upstreamData)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	resources := &networkResources{cancel: cancel, httpConnections: make(chan struct{}, 256)}
	session = &networkSession{ctx: ctx, cancel: cancel, release: resources.close}
	// Partial ownership is returned even on failure so uncertain cleanup remains
	// attached to the writer fence rather than discarded by an error path.
	defer func() {
		if result != nil {
			cancel()
		}
	}()
	resources.credentials, err = netbroker.NewCredentials(ctx, cfg.BackendURL, cfg.ControlToken, netbroker.Identity{InstanceID: req.InstanceID, UserID: req.UserID, Generation: req.Generation, PodUID: cfg.PodUID})
	if err != nil {
		return session, fmt.Errorf("private network credential acquisition failed")
	}
	context.AfterFunc(resources.credentials.Context(), cancel)
	stateDir := filepath.Join(cfg.AgentDataDir, "network")
	if err = os.Mkdir(stateDir, 0700); err != nil && !os.IsExist(err) {
		return session, fmt.Errorf("private network state unavailable")
	}
	setupCtx, setupCancel := context.WithTimeout(ctx, 20*time.Second)
	defer setupCancel()
	resources.guard, err = netguard.Prepare(setupCtx, netguard.Config{SlotIndex: cfg.NetworkSlotIndex, SlotCount: cfg.NetworkSlotCount, StateDir: stateDir, PodUID: cfg.PodUID}, netguard.Identity{InstanceID: req.InstanceID, Generation: req.Generation})
	if err != nil {
		resources.uncertain = errors.Is(err, netguard.ErrCleanupUnconfirmed)
		return session, fmt.Errorf("private network preparation failed: %w", err)
	}
	session.wrap = resources.guard.Wrap
	resources.publicDir, session.hosts, session.resolver, err = writeNetworkPublicFiles(resources.guard.ParentIP())
	if err != nil {
		return session, err
	}
	resources.broker, err = netbroker.New(ctx, netbroker.Config{ChildIP: resources.guard.ChildIP(), BackendURL: cfg.BackendURL, EgressProxyURL: cfg.NetworkEgressProxyURL, Credentials: resources.credentials})
	if err != nil {
		return session, fmt.Errorf("private network broker unavailable")
	}
	services, ok := any(resources.broker).(interface {
		Services() http.Handler
		ServeTeamRedis(context.Context, net.Listener) error
	})
	if !ok {
		return session, fmt.Errorf("private managed services unavailable")
	}
	parentIP := resources.guard.ParentIP()
	for _, entry := range []struct {
		port    int
		handler http.Handler
	}{{31280, resources.broker.Egress()}, {31281, resources.broker.Model()}, {31282, services.Services()}} {
		listener, e := net.Listen("tcp", net.JoinHostPort(parentIP, strconv.Itoa(entry.port)))
		if e != nil {
			return session, fmt.Errorf("private broker listener unavailable")
		}
		resources.serveHTTP(ctx, listener, entry.handler)
	}
	redisListener, err := net.Listen("tcp", net.JoinHostPort(parentIP, "31283"))
	if err != nil {
		return session, fmt.Errorf("private service listener unavailable")
	}
	resources.listeners = append(resources.listeners, redisListener)
	resources.workers.Add(1)
	go func() {
		defer resources.workers.Done()
		if services.ServeTeamRedis(ctx, redisListener) != nil && ctx.Err() == nil {
			cancel()
		}
	}()
	udp, err := net.ListenPacket("udp", net.JoinHostPort(parentIP, "53"))
	if err != nil {
		return session, fmt.Errorf("private DNS listener unavailable")
	}
	resources.listeners = append(resources.listeners, udp)
	tcp, err := net.Listen("tcp", net.JoinHostPort(parentIP, "53"))
	if err != nil {
		return session, fmt.Errorf("private DNS listener unavailable")
	}
	resources.listeners = append(resources.listeners, tcp)
	resources.workers.Add(1)
	go func() {
		defer resources.workers.Done()
		if netbroker.ServeDNS(ctx, resources.guard.ChildIP(), upstream, udp, tcp) != nil && ctx.Err() == nil {
			cancel()
		}
	}()
	resources.privateDir, err = os.MkdirTemp(stateDir, "ingress-")
	if err != nil {
		return session, fmt.Errorf("private ingress state unavailable")
	}
	var files []*os.File
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()
	var ports []int
	for offset := 0; offset < block; offset++ {
		nativePort := port + offset
		ports = append(ports, nativePort)
		path := filepath.Join(resources.privateDir, strconv.Itoa(nativePort)+".sock")
		listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if e != nil {
			return session, fmt.Errorf("private native listener unavailable")
		}
		listener.SetUnlinkOnClose(false)
		file, e := listener.File()
		listener.Close()
		if e != nil {
			return session, fmt.Errorf("private native descriptor unavailable")
		}
		files = append(files, file)
		if e = os.Chmod(path, 0600); e != nil {
			return session, fmt.Errorf("private native socket permissions unavailable")
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return session, fmt.Errorf("trusted network helper unavailable")
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return session, fmt.Errorf("trusted network helper unavailable")
	}
	helper, err := resources.guard.HelperCommand(ctx, executable, netrelay.Command)
	if err != nil {
		return session, err
	}
	resources.helper = helper
	data, _ := json.Marshal(netrelay.Config{Broker: net.JoinHostPort(parentIP, "31280"), IngressPorts: ports})
	helper.Stdin = bytes.NewReader(data)
	helper.Stderr = io.Discard
	helper.ExtraFiles = files
	output, err := helper.StdoutPipe()
	if err != nil {
		return session, fmt.Errorf("network helper readiness unavailable")
	}
	if err = helper.Start(); err != nil {
		return session, fmt.Errorf("network helper start failed")
	}
	resources.helperDone = make(chan struct{})
	go func() {
		helper.Wait()
		close(resources.helperDone)
		if ctx.Err() == nil {
			cancel()
		}
	}()
	ready := make(chan bool, 1)
	resources.workers.Add(1)
	go func() {
		defer resources.workers.Done()
		line, e := bufio.NewReader(io.LimitReader(output, 256)).ReadString('\n')
		ready <- e == nil && line == "{\"ready\":true}\n"
	}()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case good := <-ready:
		if !good {
			return session, fmt.Errorf("network helper readiness failed")
		}
	case <-ctx.Done():
		return session, fmt.Errorf("network helper unavailable")
	case <-timer.C:
		return session, fmt.Errorf("network helper readiness timeout")
	}
	for _, nativePort := range ports {
		path := filepath.Join(resources.privateDir, strconv.Itoa(nativePort)+".sock")
		proxy := newNativeIngress(ctx, token, func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
		})
		resources.ingress = append(resources.ingress, proxy)
		listener, e := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(nativePort)))
		if e != nil {
			return session, fmt.Errorf("native public listener unavailable")
		}
		resources.serveHTTP(ctx, listener, proxy)
	}
	if ctx.Err() != nil {
		return session, fmt.Errorf("private network setup interrupted")
	}
	return session, nil
}
func networkDNSUpstream(data []byte) (string, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			ip := net.ParseIP(fields[1])
			if ip != nil {
				return net.JoinHostPort(ip.String(), "53"), nil
			}
		}
	}
	return "", fmt.Errorf("trusted resolver requires a literal upstream IP")
}
func writeNetworkPublicFiles(parentIP string) (dir, hosts, resolver string, err error) {
	if net.ParseIP(parentIP) == nil {
		return "", "", "", fmt.Errorf("invalid private parent address")
	}
	root := sandbox.NetworkPublicRoot
	if err = os.Mkdir(root, 0755); err != nil && !os.IsExist(err) {
		return "", "", "", err
	}
	if err = trustedRootPublicDirectory(root); err != nil {
		return "", "", "", err
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return "", "", "", err
	}
	dir = filepath.Join(root, hex.EncodeToString(random))
	if err = os.Mkdir(dir, 0755); err != nil {
		return "", "", "", err
	}
	hosts = filepath.Join(dir, "hosts")
	resolver = filepath.Join(dir, "resolv.conf")
	if err = os.WriteFile(hosts, []byte("127.0.0.1 localhost\n::1 localhost\n"+parentIP+" clawmanager-local-model.invalid clawmanager-local-egress.invalid clawmanager-local-services.invalid\n"), 0444); err != nil {
		return dir, hosts, resolver, err
	}
	err = os.WriteFile(resolver, []byte("nameserver "+parentIP+"\noptions timeout:2 attempts:2\n"), 0444)
	return
}
func trustedRootPublicDirectory(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return fmt.Errorf("public routing root must be canonical")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, e := os.Lstat(current)
		if e != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 || !rootOwned(info) {
			return fmt.Errorf("public routing ancestor must be controlled by root")
		}
		if current == path && info.Mode().Perm() != 0755 {
			return fmt.Errorf("public routing root requires 0755")
		}
		if current == "/" {
			return nil
		}
	}
}
func (r *networkResources) serveHTTP(ctx context.Context, listener net.Listener, handler http.Handler) {
	listener = newSessionHTTPListener(listener, r.httpConnections)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10, IdleTimeout: 30 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	r.servers = append(r.servers, server)
	r.listeners = append(r.listeners, listener)
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
			r.cancel()
		}
	}()
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		<-ctx.Done()
		server.Close()
		listener.Close()
	}()
}
func (r *networkResources) close(ctx context.Context) error {
	r.cancel()
	for _, server := range r.servers {
		server.Close()
	}
	for _, listener := range r.listeners {
		listener.Close()
	}
	for _, proxy := range r.ingress {
		proxy.Close()
	}
	if r.broker != nil {
		r.broker.Close()
	}
	if r.credentials != nil {
		r.credentials.Close()
	}
	if r.helperDone != nil {
		select {
		case <-r.helperDone:
		case <-ctx.Done():
			return fmt.Errorf("helper stop unconfirmed")
		}
	}
	joined := make(chan struct{})
	go func() { r.workers.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-ctx.Done():
		return fmt.Errorf("network workers stop unconfirmed")
	}
	if r.uncertain {
		return fmt.Errorf("namespace setup cleanup requires ownership reconciliation")
	}
	if r.guard != nil {
		if err := r.guard.Close(ctx); err != nil {
			return err
		}
	}
	var err error
	if r.privateDir != "" {
		err = errors.Join(err, os.RemoveAll(r.privateDir))
	}
	if r.publicDir != "" {
		err = errors.Join(err, os.RemoveAll(r.publicDir))
	}
	return err
}

func validateNetworkAssignment(cfg gateway.Config, req gateway.CreateGatewayRequest, port int) (string, error) {
	if cfg.NetworkSecurityProfile != "isolated-network-v1" || req.SecurityProfile != cfg.NetworkSecurityProfile || req.PodUID != cfg.PodUID || cfg.PodUID == "" || len(cfg.ControlToken) < 32 || req.UID < 200000 || req.GID <= 0 || cfg.NetworkSlotCount < 1 || cfg.NetworkSlotCount > 256 || cfg.NetworkSlotIndex < 0 || cfg.NetworkSlotIndex >= cfg.NetworkSlotCount {
		return "", fmt.Errorf("invalid private network assignment")
	}
	block := cfg.GatewayPortBlockSize
	if block < 1 || block > 64 || port != cfg.GatewayPortStart || port+block-1 > cfg.GatewayPortEnd || port+block-1 > 65535 {
		return "", fmt.Errorf("invalid private native port block")
	}
	if req.PortRange != (gateway.PortRange{}) && (req.PortRange.Start != port || req.PortRange.End != port+block-1) {
		return "", fmt.Errorf("invalid private native port range")
	}
	token, ok := gateway.RequestEnvValue(req, "CLAWMANAGER_INSTANCE_TOKEN")
	if !ok || !strings.HasPrefix(token, "ui1_") || len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, " \t\r\n\x00") {
		return "", fmt.Errorf("private native ingress credential unavailable")
	}
	return token, nil
}
