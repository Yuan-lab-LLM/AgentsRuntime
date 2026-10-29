package dshproxy

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	instanceTokenEnv    = "CLAWMANAGER_INSTANCE_TOKEN"
	instanceTokenHeader = "X-ClawManager-Instance-Token"
	dshCookiePrefix     = "dsh-auth-"
)

type runConfig struct {
	listenHost     string
	port           int
	instanceToken  string
	startupTimeout time.Duration
	childCommand   []string
	stdout         io.Writer
	stderr         io.Writer
}

// RunCommand starts a DSH process on a private loopback port and exposes an
// authenticated reverse proxy on the managed gateway port. The proxy keeps
// DSH's process-specific browser session inside the runtime boundary.
func RunCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("dsh-web-proxy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listenHost := flags.String("listen-host", "0.0.0.0", "managed gateway bind host")
	port := flags.Int("port", 0, "managed gateway port")
	startupTimeout := flags.Duration("startup-timeout", 90*time.Second, "DSH startup timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *port < 1 || *port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if *startupTimeout <= 0 {
		return fmt.Errorf("startup-timeout must be positive")
	}
	instanceToken := strings.TrimSpace(os.Getenv(instanceTokenEnv))
	if instanceToken == "" {
		return fmt.Errorf("%s is required", instanceTokenEnv)
	}
	childCommand := flags.Args()
	if len(childCommand) == 0 {
		return fmt.Errorf("DSH child command is required after --")
	}
	return run(ctx, runConfig{
		listenHost:     strings.TrimSpace(*listenHost),
		port:           *port,
		instanceToken:  instanceToken,
		startupTimeout: *startupTimeout,
		childCommand:   childCommand,
		stdout:         stdout,
		stderr:         stderr,
	})
}

func run(ctx context.Context, cfg runConfig) error {
	if cfg.stdout == nil {
		cfg.stdout = io.Discard
	}
	if cfg.stderr == nil {
		cfg.stderr = io.Discard
	}
	if cfg.listenHost == "" {
		cfg.listenHost = "0.0.0.0"
	}
	if len(cfg.childCommand) == 0 {
		return fmt.Errorf("DSH child command is empty")
	}

	childCtx, cancelChild := context.WithCancel(ctx)
	defer cancelChild()
	cmd := exec.CommandContext(childCtx, cfg.childCommand[0], cfg.childCommand[1:]...)
	cmd.Env = os.Environ()
	cmd.Stderr = cfg.stderr
	childStdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("capture DSH stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start DSH: %w", err)
	}

	childDone := make(chan error, 1)
	go func() {
		childDone <- cmd.Wait()
	}()
	launchURLs := make(chan *url.URL, 1)
	stdoutErrors := make(chan error, 1)
	go scanDSHStdout(childStdout, cfg.stdout, launchURLs, stdoutErrors)

	startupTimer := time.NewTimer(cfg.startupTimeout)
	defer startupTimer.Stop()
	var launchURL *url.URL
	select {
	case <-ctx.Done():
		return ctx.Err()
	case childErr := <-childDone:
		return unexpectedChildExit(childErr)
	case scanErr := <-stdoutErrors:
		return fmt.Errorf("read DSH startup output: %w", scanErr)
	case <-startupTimer.C:
		return fmt.Errorf("DSH did not publish its authenticated URL within %s", cfg.startupTimeout)
	case launchURL = <-launchURLs:
	}

	target, sessionCookie, err := bootstrapBrowserSession(ctx, launchURL)
	if err != nil {
		return fmt.Errorf("bootstrap DSH browser session: %w", err)
	}

	listener, err := net.Listen("tcp", net.JoinHostPort(cfg.listenHost, strconv.Itoa(cfg.port)))
	if err != nil {
		return fmt.Errorf("listen on managed gateway port %d: %w", cfg.port, err)
	}
	defer listener.Close()

	server := &http.Server{
		Handler:           newAuthenticatedProxy(target, cfg.instanceToken, sessionCookie),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	fmt.Fprintf(cfg.stdout, "dsh web proxy: ready on %s\n", listener.Addr())

	select {
	case <-ctx.Done():
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		_ = server.Shutdown(shutdownCtx)
		return ctx.Err()
	case childErr := <-childDone:
		_ = server.Close()
		return unexpectedChildExit(childErr)
	case serveErr := <-serveDone:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve managed DSH gateway: %w", serveErr)
	}
}

func unexpectedChildExit(err error) error {
	if err == nil {
		return fmt.Errorf("DSH exited unexpectedly")
	}
	return fmt.Errorf("DSH exited unexpectedly: %w", err)
}

func scanDSHStdout(reader io.Reader, output io.Writer, launchURLs chan<- *url.URL, scanErrors chan<- error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	published := false
	for scanner.Scan() {
		line := scanner.Text()
		launchURL, matched, err := parseLaunchURL(line)
		if matched {
			// Never copy the process launch token into the pooled runtime logs.
			if err != nil {
				fmt.Fprintln(output, "dsh web: authenticated startup URL withheld")
				continue
			}
			fmt.Fprintf(output, "dsh web: private backend ready on %s\n", launchURL.Host)
			if !published {
				published = true
				launchURLs <- launchURL
			}
			continue
		}
		fmt.Fprintln(output, line)
	}
	if err := scanner.Err(); err != nil {
		scanErrors <- err
	}
}

func parseLaunchURL(line string) (*url.URL, bool, error) {
	const marker = "dsh web:"
	markerAt := strings.Index(line, marker)
	if markerAt < 0 {
		return nil, false, nil
	}
	remainder := strings.TrimSpace(line[markerAt+len(marker):])
	fields := strings.Fields(remainder)
	if len(fields) == 0 || !strings.Contains(fields[0], "token=") {
		return nil, false, nil
	}
	parsed, err := url.Parse(fields[0])
	if err != nil {
		return nil, true, fmt.Errorf("parse DSH launch URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.Host == "" {
		return nil, true, fmt.Errorf("DSH launch URL must use an HTTP loopback origin")
	}
	hostname := parsed.Hostname()
	loopback := strings.EqualFold(hostname, "localhost")
	if ip := net.ParseIP(hostname); ip != nil {
		loopback = ip.IsLoopback()
	}
	if !loopback {
		return nil, true, fmt.Errorf("DSH launch URL host is not loopback")
	}
	tokens := parsed.Query()["token"]
	if len(tokens) != 1 || strings.TrimSpace(tokens[0]) == "" {
		return nil, true, fmt.Errorf("DSH launch URL has no single process token")
	}
	return parsed, true, nil
}

func bootstrapBrowserSession(ctx context.Context, launchURL *url.URL) (*url.URL, string, error) {
	if launchURL == nil {
		return nil, "", fmt.Errorf("DSH launch URL is nil")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, launchURL.String(), nil)
	if err != nil {
		return nil, "", err
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode != http.StatusSeeOther {
		return nil, "", fmt.Errorf("DSH token exchange returned HTTP %d", response.StatusCode)
	}
	var sessionCookie string
	for _, cookie := range response.Cookies() {
		if strings.HasPrefix(cookie.Name, dshCookiePrefix) && cookie.Value != "" {
			sessionCookie = cookie.Name + "=" + cookie.Value
			break
		}
	}
	if sessionCookie == "" {
		return nil, "", fmt.Errorf("DSH token exchange did not return a browser-session cookie")
	}
	target := *launchURL
	target.Path = ""
	target.RawPath = ""
	target.RawQuery = ""
	target.Fragment = ""
	return &target, sessionCookie, nil
}

func newAuthenticatedProxy(target *url.URL, expectedToken, sessionCookie string) http.Handler {
	reverseProxy := httputil.NewSingleHostReverseProxy(target)
	director := reverseProxy.Director
	reverseProxy.Director = func(request *http.Request) {
		director(request)
		restorePluginBatchRawQuery(request.URL)
		request.Host = target.Host
		rewriteBrowserOrigin(request.Header, target)
		stripManagedCredentials(request.Header)
		attachDSHSessionCookie(request.Header, sessionCookie)
	}
	reverseProxy.ModifyResponse = func(response *http.Response) error {
		cookies := response.Header.Values("Set-Cookie")
		response.Header.Del("Set-Cookie")
		for _, cookie := range cookies {
			if strings.HasPrefix(strings.TrimSpace(cookie), dshCookiePrefix) {
				continue
			}
			response.Header.Add("Set-Cookie", cookie)
		}
		return nil
	}
	reverseProxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, err error) {
		http.Error(writer, "DSH gateway unavailable", http.StatusBadGateway)
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !requestHasInstanceToken(request, expectedToken) {
			writer.Header().Set("Cache-Control", "no-store")
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		reverseProxy.ServeHTTP(writer, request)
	})
}

// restorePluginBatchRawQuery accepts queries normalized by older ClawManager
// proxies. DSH intentionally uses a leading '?' as the first plugin batch key,
// but url.Values.Encode turns that key into "%3F...=" and breaks routing.
func restorePluginBatchRawQuery(requestURL *url.URL) {
	if requestURL == nil || requestURL.Path != "/plugins/" || requestURL.RawQuery == "" {
		return
	}
	segments := strings.Split(requestURL.RawQuery, "&")
	encodedKey, value, hasValue := strings.Cut(segments[0], "=")
	if !hasValue || value != "" {
		return
	}
	decodedKey, err := url.QueryUnescape(encodedKey)
	if err != nil || !strings.HasPrefix(decodedKey, "?@") {
		return
	}
	if strings.ContainsAny(decodedKey, "&=#\r\n") {
		return
	}
	segments[0] = decodedKey
	requestURL.RawQuery = strings.Join(segments, "&")
}

func requestHasInstanceToken(request *http.Request, expected string) bool {
	candidates := []string{
		strings.TrimSpace(request.Header.Get(instanceTokenHeader)),
		bearerToken(request.Header.Get("Authorization")),
		strings.TrimSpace(request.Header.Get("X-Api-Key")),
	}
	for _, candidate := range candidates {
		if len(candidate) != len(expected) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(expected)) == 1 {
			return true
		}
	}
	return false
}

func bearerToken(value string) string {
	parts := strings.Fields(value)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return ""
}

func stripManagedCredentials(header http.Header) {
	for _, name := range []string{
		"Authorization",
		"X-Api-Key",
		"X-OpenAI-Api-Key",
		"OpenAI-Api-Key",
		instanceTokenHeader,
		"X-ClawManager-LLM-API-Key",
	} {
		header.Del(name)
	}
}

func rewriteBrowserOrigin(header http.Header, target *url.URL) {
	targetOrigin := target.Scheme + "://" + target.Host
	if strings.TrimSpace(header.Get("Origin")) != "" {
		header.Set("Origin", targetOrigin)
	}
	if rawReferer := strings.TrimSpace(header.Get("Referer")); rawReferer != "" {
		if referer, err := url.Parse(rawReferer); err == nil {
			referer.Scheme = target.Scheme
			referer.Host = target.Host
			header.Set("Referer", referer.String())
		}
	}
}

func attachDSHSessionCookie(header http.Header, sessionCookie string) {
	parts := make([]string, 0)
	for _, raw := range header.Values("Cookie") {
		for _, part := range strings.Split(raw, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, _, _ := strings.Cut(part, "=")
			if strings.HasPrefix(strings.TrimSpace(name), dshCookiePrefix) {
				continue
			}
			parts = append(parts, part)
		}
	}
	parts = append(parts, sessionCookie)
	header.Del("Cookie")
	header.Set("Cookie", strings.Join(parts, "; "))
}
