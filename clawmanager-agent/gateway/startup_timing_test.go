package gateway

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestStartupTimingRedactsAndBoundsBackendOutput(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	w := &isolatedProcessLog{instanceID: 12, generation: 3}
	w.startupTimings([]byte("secret-token\nhermes_lite_startup phase=db_pro"))
	w.startupTimings([]byte("be action=skip elapsed_ms=14\n"))
	w.startupTimings([]byte("hermes_lite_startup phase=db_probe action=skip elapsed_ms=99\n"))
	w.startupTimings([]byte("hermes_lite_startup phase=secret-token action=done elapsed_ms=1\n"))
	w.startupTimings([]byte(strings.Repeat("x", 10000) + "hermes_lite_startup phase=db_total action=done elapsed_ms=1\n"))
	w.startupTimings([]byte("hermes_lite_startup phase=db_total action=skip elapsed_ms=25\n"))
	got := output.String()
	if strings.Contains(got, "secret-token") || strings.Contains(got, "elapsed_ms=99") || strings.Contains(got, "elapsed_ms=1\n") {
		t.Fatalf("untrusted data escaped timing filter")
	}
	if strings.Count(got, "startup timing:") != 2 || !strings.Contains(got, "phase=db_probe action=skip elapsed_ms=14") || !strings.Contains(got, "phase=db_total action=skip elapsed_ms=25") {
		t.Fatalf("missing split timing records: %s", got)
	}
}

func TestPreHTTPStartupTimingRecords(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	w := &isolatedProcessLog{instanceID: 12, generation: 3}
	for _, phase := range []string{"cli_import", "skills_sync", "plugins_load", "web_import", "gateway_warm", "http_bind"} {
		w.startupTimings([]byte("hermes_lite_startup phase=" + phase + " action=done elapsed_ms=123\n"))
	}
	w.startupTimings([]byte("hermes_lite_startup phase=web_import action=done elapsed_ms=123 secret=value\n"))
	if strings.Count(output.String(), "startup timing:") != 6 || strings.Contains(output.String(), "secret") {
		t.Fatal("pre-HTTP stages were lost or untrusted fields escaped")
	}
}
