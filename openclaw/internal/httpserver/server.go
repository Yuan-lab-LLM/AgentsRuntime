package httpserver

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/openclawinspector"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/process"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/profiler"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/store"
)

type Server struct {
	srv *http.Server
}

const openClawWaitWarmupTimeout = 30 * time.Second

func New(cfg appconfig.Config, proc *process.Manager, prof *profiler.Profiler, inspector *openclawinspector.Inspector, st *store.Store) *Server {
	router := gin.New()
	router.Use(gin.Recovery())

	router.GET("/healthz", func(c *gin.Context) {
		snapshot := proc.Snapshot()
		code := http.StatusOK
		if snapshot.Status == process.StatusCrashed || snapshot.Status == process.StatusUnknown {
			code = http.StatusServiceUnavailable
		}
		c.JSON(code, gin.H{
			"status":          snapshot.Status,
			"openclaw_pid":    snapshot.PID,
			"current_state":   st.Snapshot(),
			"server_time_utc": time.Now().UTC(),
		})
	})

	router.GET("/readyz", func(c *gin.Context) {
		snapshot := proc.Snapshot()
		code := http.StatusServiceUnavailable
		if snapshot.Status == process.StatusRunning || snapshot.Status == process.StatusStarting {
			code = http.StatusOK
		}
		c.JSON(code, gin.H{
			"status":          snapshot.Status,
			"openclaw_pid":    snapshot.PID,
			"server_time_utc": time.Now().UTC(),
		})
	})

	waitPageHandler := func(c *gin.Context) {
		target := c.Query("target")
		if target == "" {
			target = cfg.BrowserURL
		}
		if target == "" {
			target = "http://localhost:18789"
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(runtimeWaitPage(target, cfg.RuntimeType, cfg.RuntimeName)))
	}
	router.GET("/runtime-wait", waitPageHandler)
	// Keep the original route for already-running images and saved browser tabs.
	router.GET("/openclaw-wait", waitPageHandler)

	waitReadyHandler := func(c *gin.Context) {
		snapshot := proc.Snapshot()
		ready := runtimeWaitReady(snapshot)
		c.JSON(http.StatusOK, gin.H{
			"ready":                  ready,
			"status":                 snapshot.Status,
			"gateway_warmup_started": snapshot.GatewayWarmupStarted,
			"gateway_warmup_ready":   snapshot.GatewayWarmupReady,
			"openclaw_pid":           snapshot.PID,
			"operation":              snapshot.LastOperation,
			"operation_log":          snapshot.LastOperationResult,
		})
	}
	router.GET("/runtime-wait/ready", waitReadyHandler)
	router.GET("/openclaw-wait/ready", waitReadyHandler)

	router.GET("/debug/state", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"process": proc.Snapshot(),
			"store":   st.Snapshot(),
		})
	})

	router.GET("/debug/runtime", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"process":   proc.Snapshot(),
			"system":    prof.Collect(),
			"openclaw":  inspector.Collect(),
			"store":     st.Snapshot(),
			"timestamp": time.Now().UTC(),
		})
	})

	return &Server{
		srv: &http.Server{
			Addr:              cfg.LocalHTTPBind,
			Handler:           router,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
}

func runtimeWaitReady(snapshot process.Snapshot) bool {
	return snapshot.Status == process.StatusRunning || snapshot.GatewayWarmupStarted || snapshot.GatewayWarmupReady
}

func runtimeWaitPage(target, runtimeType, runtimeName string) string {
	isOpenClaw := strings.EqualFold(strings.TrimSpace(runtimeType), "openclaw")
	if gatewayToken := os.Getenv("OPENCLAW_GATEWAY_TOKEN"); isOpenClaw && gatewayToken != "" {
		// Keep the token server-side until the wait page is rendered. Do not
		// attach it to the wait-page query string or browser launch arguments.
		target = "http://localhost:18789/#token=" + url.QueryEscape(gatewayToken)
	}
	displayName := strings.TrimSpace(runtimeName)
	if displayName == "" {
		displayName = strings.TrimSpace(runtimeType)
	}
	if displayName == "" {
		displayName = "Runtime"
	}
	displayName = html.EscapeString(displayName)

	return `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>` + displayName + `</title>
  <style>
    html, body { height: 100%; margin: 0; }
    body {
      display: grid;
      place-items: center;
      background: #f4f7fb;
      color: #16202a;
      font: 15px/1.5 system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    main { width: min(420px, calc(100vw - 48px)); }
    h1 { margin: 0 0 8px; font-size: 22px; font-weight: 700; }
    p { margin: 0; color: #53616f; }
    .bar { height: 3px; margin-top: 24px; overflow: hidden; background: #d9e2ec; }
    .bar::before {
      content: "";
      display: block;
      width: 42%;
      height: 100%;
      background: #1677ff;
      animation: move 1.1s ease-in-out infinite;
    }
    @keyframes move {
      0% { transform: translateX(-100%); }
      100% { transform: translateX(240%); }
    }
  </style>
</head>
<body>
  <main>
    <h1>&#27491;&#22312;&#21551;&#21160; ` + displayName + `</h1>
    <p id="status">&#21518;&#21488;&#26381;&#21153;&#20934;&#22791;&#20013;&#65292;&#35831;&#31245;&#20505;&#12290;</p>
    <div class="bar"></div>
  </main>
  <script>
    const target = ` + strconv.Quote(target) + `;
    const runtimeRequiresWarmup = ` + strconv.FormatBool(isOpenClaw) + `;
    const warmupWaitMs = ` + strconv.FormatInt(openClawWaitWarmupTimeout.Milliseconds(), 10) + `;
    let gatewayReadyAt = 0;
    async function check() {
      try {
        const resp = await fetch("/runtime-wait/ready", { cache: "no-store" });
        const data = await resp.json();
        if (data.ready) {
          if (!runtimeRequiresWarmup) {
            location.replace(target);
            return;
          }
          if (gatewayReadyAt === 0) {
            gatewayReadyAt = Date.now();
          }
          const warmupStarted = data.gateway_warmup_started === true;
          const warmupReady = data.gateway_warmup_ready === true;
          const warmupTimedOut = (warmupStarted || warmupReady) && Date.now() - gatewayReadyAt >= warmupWaitMs;
          if (warmupReady || warmupTimedOut) {
            location.replace(target);
            return;
          }
          document.getElementById("status").textContent = "\u6b63\u5728\u9884\u70ed\u6a21\u578b\u76ee\u5f55\uff0c\u9a6c\u4e0a\u8fdb\u5165\u3002";
          setTimeout(check, 1000);
          return;
        }
        const status = data.status || "starting";
        document.getElementById("status").textContent = "\u5f53\u524d\u72b6\u6001\uff1a" + status;
      } catch (_) {
        document.getElementById("status").textContent = "\u6b63\u5728\u8fde\u63a5\u672c\u5730\u542f\u52a8\u670d\u52a1\u3002";
      }
      setTimeout(check, 1200);
    }
    check();
  </script>
</body>
</html>`
}

func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}
