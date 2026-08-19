// Package browser is responsible for opening the configured runtime UI in the
// user-facing Chromium browser as soon as the Wayland session is ready.
//
// The linuxserver/webtop:ubuntu-kde image launches `plasmashell` directly
// without `ksmserver` (or a systemd user session that would host the
// `systemd-xdg-autostart-generator`), so XDG autostart entries placed in
// ~/.config/autostart never run. Rather than try to repair that machinery
// from outside, the agent owns the launch sequence end-to-end.
package browser

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
)

const (
	pollInterval        = 500 * time.Millisecond
	agentReadyTimeout   = 10 * time.Second
	healthClientTimeout = 2 * time.Second
)

// Launch waits for the Wayland socket and then spawns the configured browser
// pointed at cfg.BrowserURL. It intentionally does not wait for OpenClaw
// gateway health, because doctor and gateway startup can take long enough that
// a blank desktop feels broken to users.
func Launch(ctx context.Context, cfg appconfig.Config) {
	if !cfg.BrowserAutoLaunchEnabled {
		log.Printf("browser: auto launch disabled by config")
		return
	}
	if cfg.BrowserExecutable == "" || cfg.BrowserURL == "" {
		log.Printf("browser: missing executable or url, skipping launch")
		return
	}

	if err := waitForWaylandSocket(ctx, cfg); err != nil {
		log.Printf("browser: %v", err)
		return
	}
	if cfg.BrowserLaunchExtraDelay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.BrowserLaunchExtraDelay):
		}
	}
	launchURL := browserLaunchURL(ctx, cfg)
	if err := spawnBrowser(ctx, cfg, launchURL); err != nil {
		log.Printf("browser: spawn failed: %v", err)
		return
	}
	log.Printf("browser: launched %s -> %s", cfg.BrowserExecutable, launchURL)
}

func waitForWaylandSocket(ctx context.Context, cfg appconfig.Config) error {
	if cfg.WaylandSocketPath == "" {
		return nil
	}
	deadline := time.Now().Add(cfg.BrowserLaunchWaylandTimeout)
	for {
		if _, err := os.Stat(cfg.WaylandSocketPath); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wayland socket %s not ready within %s", cfg.WaylandSocketPath, cfg.BrowserLaunchWaylandTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func browserLaunchURL(ctx context.Context, cfg appconfig.Config) string {
	waitURL := runtimeWaitURL(cfg)
	if waitURL == "" {
		return cfg.BrowserURL
	}
	if err := waitForAgentHTTP(ctx, waitURL); err != nil {
		log.Printf("browser: wait page not ready, opening target directly: %v", err)
		return cfg.BrowserURL
	}
	return waitURL
}

func waitForAgentHTTP(ctx context.Context, waitURL string) error {
	parsed, err := url.Parse(waitURL)
	if err != nil {
		return err
	}
	healthURL := *parsed
	healthURL.Path = "/healthz"
	healthURL.RawQuery = ""

	client := &http.Client{Timeout: healthClientTimeout}
	deadline := time.Now().Add(agentReadyTimeout)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL.String(), nil)
		if err == nil {
			if resp, err := client.Do(req); err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 500 {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("agent http %s not ready within %s", healthURL.String(), agentReadyTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func runtimeWaitURL(cfg appconfig.Config) string {
	if cfg.LocalHTTPBind == "" || cfg.BrowserURL == "" {
		return ""
	}
	host := "127.0.0.1"
	port := "18080"
	if parsedHost, parsedPort, err := net.SplitHostPort(cfg.LocalHTTPBind); err == nil {
		port = parsedPort
		if parsedHost != "" && parsedHost != "0.0.0.0" && parsedHost != "::" && parsedHost != "[::]" {
			host = parsedHost
		}
	}
	return "http://" + net.JoinHostPort(host, port) + "/runtime-wait?target=" + url.QueryEscape(cfg.BrowserURL)
}

func spawnBrowser(ctx context.Context, cfg appconfig.Config, launchURL string) error {
	cmd := exec.CommandContext(ctx, cfg.BrowserExecutable, launchURL)
	cmd.Env = browserEnv()
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}

func browserEnv() []string {
	base := []string{
		"WAYLAND_DISPLAY=wayland-0",
		"XDG_RUNTIME_DIR=/config/.XDG",
		"XDG_SESSION_TYPE=wayland",
		"XDG_CURRENT_DESKTOP=KDE",
		"DISPLAY=:1",
		"HOME=/config",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	if lang := os.Getenv("LANG"); lang != "" {
		base = append(base, "LANG="+lang)
	}
	return base
}
