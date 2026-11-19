package gateway

import (
	"log"
	"regexp"
	"time"
)

// Backend output is untrusted. Only complete bounded timing records with a
// fixed vocabulary may cross the existing output-redaction boundary.
var startupTimingRecord = regexp.MustCompile(`^hermes_lite_startup phase=(db_import|db_probe|db_initialize|db_total|auth_warm|boot_fingerprint|cli_dispatch|cli_import|copilot_warm|gateway_warm|hosted_import|http_bind|inventory_warm|lifespan_prepare|model_switch_warm|plugins_load|prepare_runtime|provider_warm|server_setup|skills_sync|skills_optional|skills_external|skin_warm|terminal_config|web_deps|web_import) action=(done|skip|initialize|migrate|failed) elapsed_ms=([0-9]{1,9})$`)

func (w *isolatedProcessLog) startupTimings(data []byte) {
	w.timingMu.Lock()
	defer w.timingMu.Unlock()
	for _, b := range data {
		if b == '\n' {
			if !w.timingOverflow {
				m := startupTimingRecord.FindStringSubmatch(string(w.timingLine))
				if len(m) == 4 && !w.timingSeen[m[1]] {
					if w.timingSeen == nil {
						w.timingSeen = make(map[string]bool)
					}
					w.timingSeen[m[1]] = true
					log.Printf("runtime-agent startup timing: instance_id=%d generation=%d phase=%s action=%s elapsed_ms=%s", w.instanceID, w.generation, m[1], m[2], m[3])
				}
			}
			w.timingLine, w.timingOverflow = nil, false
		} else if len(w.timingLine) < 160 && !w.timingOverflow {
			w.timingLine = append(w.timingLine, b)
		} else {
			w.timingLine, w.timingOverflow = nil, true
		}
	}
}

func logStartupStage(runtime string, req CreateGatewayRequest, phase string, started time.Time) {
	if runtime == "hermes" {
		log.Printf("runtime-agent startup timing: instance_id=%d generation=%d phase=%s elapsed_ms=%d", req.InstanceID, req.Generation, phase, time.Since(started).Milliseconds())
	}
}
