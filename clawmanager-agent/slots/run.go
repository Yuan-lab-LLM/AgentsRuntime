package slots

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/iamlovingit/clawmanager-agent/control"
	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func Run(ctx context.Context, cfg gateway.Config, role string) error {
	if cfg.PodUID == "" {
		return fmt.Errorf("container slots require POD_UID")
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("container slots require Linux")
	}
	switch role {
	case "pool":
		return runPool(ctx, cfg)
	case "slot":
		return runWorker(ctx, cfg)
	default:
		return fmt.Errorf("unknown runtime agent role %q", role)
	}
}

func runWorker(ctx context.Context, cfg gateway.Config) error {
	index, err := slotIndex(os.Getenv("RUNTIME_SLOT_INDEX"))
	if err != nil {
		return err
	}
	socket := os.Getenv("RUNTIME_SLOT_SOCKET")
	expected := filepath.Join("/run/clawmanager-slots", fmt.Sprintf("slot-%d.sock", index))
	if socket != expected {
		return fmt.Errorf("slot socket must be %s", expected)
	}
	cfg.Capacity = 1
	if err = os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		return err
	}
	guard, err := os.OpenFile(socket+".guard", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer guard.Close()
	if err = lockWorkspace(guard); err != nil {
		return fmt.Errorf("another supervisor owns this slot")
	}
	worker, err := NewWorker(cfg)
	if err != nil {
		return err
	}
	if err = worker.manager.Initialize(); err != nil {
		return err
	}
	if info, e := os.Lstat(socket); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("slot socket path is not a socket")
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer os.Remove(socket)
	if err = os.Chmod(socket, 0600); err != nil {
		listener.Close()
		return err
	}
	return serve(ctx, listener, worker, func(stop context.Context) error { return worker.Shutdown(stop) }, cfg.ProcessStopTimeout)
}

func runPool(ctx context.Context, cfg gateway.Config) error {
	tlsConfig, err := poolTLSConfig(cfg)
	if err != nil {
		return err
	}
	count, err := strconv.Atoi(os.Getenv("RUNTIME_SLOT_COUNT"))
	if err != nil || count < 1 || count > 256 {
		return fmt.Errorf("RUNTIME_SLOT_COUNT must be 1..256")
	}
	dir := os.Getenv("RUNTIME_SLOT_SOCKET_DIR")
	if dir == "" {
		dir = "/run/clawmanager-slots"
	}
	if dir != "/run/clawmanager-slots" {
		return fmt.Errorf("invalid slot socket directory")
	}
	cfg.Capacity = count
	clients := make([]*http.Client, count)
	for i := range clients {
		socket := filepath.Join(dir, fmt.Sprintf("slot-%d.sock", i))
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
		}}
		defer transport.CloseIdleConnections()
		clients[i] = &http.Client{Transport: transport, Timeout: 20 * time.Minute}
	}
	call := func(ctx context.Context, index int, method, path string, body []byte) (int, []byte, error) {
		if index < 0 || index >= len(clients) {
			return 0, nil, fmt.Errorf("invalid slot")
		}
		req, err := http.NewRequestWithContext(ctx, method, "http://slot"+path, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set(gateway.ControlTokenHeader, cfg.ControlToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := clients[index].Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
		if len(data) > 4<<20 {
			return 0, nil, fmt.Errorf("slot response too large")
		}
		return resp.StatusCode, data, err
	}
	pool, err := NewPool(cfg, count, call)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	listener = tls.NewListener(listener, tlsConfig)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go pool.report(runCtx)
	return serve(ctx, listener, pool, nil, cfg.ProcessStopTimeout)
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler, cleanup func(context.Context) error, stopTimeout time.Duration) error {
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	select {
	case err := <-stopped:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}
	if stopTimeout <= 0 {
		stopTimeout = 20 * time.Second
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout+10*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(stopCtx)
	if cleanup != nil {
		if err := cleanup(stopCtx); err != nil {
			return err
		}
	}
	return shutdownErr
}

func (p *Pool) report(ctx context.Context) {
	reporter := control.NewReportClient(p.cfg)
	metadata := p.metadata
	podID := 0
	for ctx.Err() == nil {
		payload := metadata.RegisterPayload()
		payload.Capacity = p.count
		payload.MaxGateways = p.count
		payload.AvailableSlots = 0
		payload.Capabilities = append(payload.Capabilities, "container-slots-v1")
		var err error
		podID, err = reporter.Register(ctx, payload)
		if err == nil && podID > 0 {
			break
		}
		log.Print("slot pool registration unavailable")
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	var wg sync.WaitGroup
	loop := func(interval time.Duration, work func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				work(ctx)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	loop(2*time.Second, func(ctx context.Context) {
		snapshotCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		snapshots, _ := p.snapshots(snapshotCtx)
		used := 0
		for _, s := range snapshots {
			if s.Quarantined || (s.Assignment != nil && s.Assignment.State != "stopped") {
				used++
			}
		}
		p.mu.RLock()
		draining := p.draining
		p.mu.RUnlock()
		available := p.count - used
		state := "ready"
		if draining {
			available = 0
			state = "draining"
		}
		if metadata.UpgradeStandby() {
			available = 0
			state = "standby"
		}
		_ = reporter.ReportHeartbeat(ctx, gateway.HeartbeatPayload{PodID: podID, Namespace: p.cfg.Namespace, PodName: p.cfg.PodName, State: state, MaxGateways: p.count, UsedSlots: used, AvailableSlots: available, Draining: draining, ReportedAt: time.Now().UTC()})
	})
	loop(5*time.Second, func(ctx context.Context) {
		snapshotCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		snapshots, _ := p.snapshots(snapshotCtx)
		states := []gateway.GatewayState{}
		for _, s := range snapshots {
			states = append(states, s.Gateways...)
		}
		_ = reporter.ReportGateways(ctx, gateway.GatewayReportPayload{PodID: podID, Namespace: p.cfg.Namespace, PodName: p.cfg.PodName, Gateways: states})
	})
	loop(30*time.Second, func(ctx context.Context) {
		payload := gateway.SkillReportPayload{PodID: podID, RuntimeType: p.cfg.RuntimeType, Namespace: p.cfg.Namespace, PodName: p.cfg.PodName, Mode: "full", ReportedAt: time.Now().UTC(), Instances: []gateway.SkillInstanceReport{}}
		for i := 0; i < p.count; i++ {
			requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			status, data, err := p.call(requestCtx, i, http.MethodGet, "/v1/slot-skills", nil)
			cancel()
			var skills gateway.SkillReportPayload
			if err != nil || status != 200 || json.Unmarshal(data, &skills) != nil {
				continue
			}
			payload.Instances = append(payload.Instances, skills.Instances...)
		}
		_ = reporter.ReportSkills(ctx, payload)
	})
	metrics := newMetricsSampler()
	metricsInterval := p.cfg.MetricsInterval
	if metricsInterval <= 0 {
		metricsInterval = 10 * time.Second
	}
	loop(metricsInterval, func(ctx context.Context) {
		collectionCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		slots := p.collectMetrics(collectionCtx)
		cancel()
		payload := aggregateMetrics(p.cfg, podID, metrics.Sample(), slots, readPodNetwork())
		if err := reporter.ReportMetrics(ctx, payload); err != nil {
			log.Print("slot pool metrics report unavailable")
		}
	})
	wg.Wait()
}
