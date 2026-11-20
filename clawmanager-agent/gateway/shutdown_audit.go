package gateway

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

var errShutdownUnconfirmed = errors.New("OpenClaw native cleanup or writer release was not confirmed; retain writer evidence")

type shutdownProof struct {
	// The proof remains local; only a bounded diagnostic observation leaves the manager.
	directory, key, namespace, claim string
	generation                       int
}

func shutdownJSON(value any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(value)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
func shutdownHash(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }
func shutdownDirectory(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) == filepath.VolumeName(dir)+string(filepath.Separator) {
		return errShutdownUnconfirmed
	}
	stat, err := os.Lstat(dir)
	if err != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
		return errShutdownUnconfirmed
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return errShutdownUnconfirmed
	}
	if runtime.GOOS == "windows" {
		// Go may expand DOS short names while Node preserves them. Reject
		// linked ancestors, then compare directory identities, not spellings.
		current := filepath.VolumeName(dir) + string(filepath.Separator)
		for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(dir), current), string(filepath.Separator)) {
			if part == "" {
				continue
			}
			current = filepath.Join(current, part)
			entry, e := os.Lstat(current)
			if e != nil || entry.Mode()&os.ModeSymlink != 0 {
				return errShutdownUnconfirmed
			}
		}
		realStat, e := os.Stat(real)
		if e != nil || !os.SameFile(stat, realStat) {
			return errShutdownUnconfirmed
		}
	} else if filepath.Clean(real) != filepath.Clean(dir) {
		return errShutdownUnconfirmed
	}
	return nil
}
func readShutdownFile(file string) ([]byte, error) {
	before, err := os.Lstat(file)
	if err != nil || !before.Mode().IsRegular() || before.Size() > 2048 {
		return nil, errShutdownUnconfirmed
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, errShutdownUnconfirmed
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		return nil, errShutdownUnconfirmed
	}
	data, err := io.ReadAll(io.LimitReader(f, 2049))
	after, statErr := os.Lstat(file)
	if err != nil || len(data) > 2048 || statErr != nil || !os.SameFile(before, after) || after.Mode()&os.ModeSymlink != 0 {
		return nil, errShutdownUnconfirmed
	}
	return data, nil
}
func decodeShutdown(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		return errShutdownUnconfirmed
	}
	return nil
}

type ShutdownObservation struct {
	Generation int    `json:"generation"`
	ClaimID    string `json:"claim_id"`
	Outcome    string `json:"outcome"`
}

func captureShutdownProof(env []string) (*shutdownProof, error) {
	if !capabilityHealthRequired(env) {
		return nil, nil
	}
	values := map[string]string{}
	for _, entry := range env {
		k, v, _ := strings.Cut(entry, "=")
		switch k {
		case "CLAWMANAGER_TASK_RECEIPT_DIR", "CLAWMANAGER_LLM_BASE_URL", "CLAWMANAGER_LLM_API_KEY", "CLAWMANAGER_GATEWAY_GENERATION":
			if _, ok := values[k]; ok {
				return nil, errShutdownUnconfirmed
			}
			values[k] = v
		}
	}
	directory := values["CLAWMANAGER_TASK_RECEIPT_DIR"]
	if shutdownDirectory(directory) != nil {
		return nil, errShutdownUnconfirmed
	}
	claimRoot := filepath.Join(directory, ".task-writer.lock")
	if shutdownDirectory(claimRoot) != nil {
		return nil, errShutdownUnconfirmed
	}
	data, err := readShutdownFile(filepath.Join(claimRoot, "owner.json"))
	if err != nil {
		return nil, err
	}
	var owner struct {
		Version    int    `json:"version"`
		Generation int    `json:"generation"`
		Owner      string `json:"owner"`
	}
	generation, err := strconv.ParseInt(values["CLAWMANAGER_GATEWAY_GENERATION"], 10, 32)
	if err != nil || generation < 0 || strconv.FormatInt(generation, 10) != values["CLAWMANAGER_GATEWAY_GENERATION"] || decodeShutdown(data, &owner) != nil || owner.Version != 1 || owner.Generation != int(generation) || !regexp.MustCompile("^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$").MatchString(owner.Owner) {
		return nil, errShutdownUnconfirmed
	}
	raw := values["CLAWMANAGER_LLM_BASE_URL"]
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.TrimSpace(raw) != raw {
		return nil, errShutdownUnconfirmed
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if u.Scheme == "http" && port == "80" || u.Scheme == "https" && port == "443" {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	u.Host = host
	if u.Path == "" {
		u.Path = "/"
	}
	base := strings.TrimRight(u.String(), "/")
	key := values["CLAWMANAGER_LLM_API_KEY"]
	if key == "" || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\r\n\x00") {
		return nil, errShutdownUnconfirmed
	}
	return &shutdownProof{directory: directory, key: key, generation: int(generation), claim: shutdownHash(data), namespace: shutdownHash(shutdownJSON([]any{base, key, generation}))}, nil
}
func (p *shutdownProof) verify() string {
	if p == nil || shutdownDirectory(p.directory) != nil {
		return "cleanup_unconfirmed"
	}
	root := filepath.Join(p.directory, "z-runtime-shutdowns")
	if shutdownDirectory(root) != nil {
		return "cleanup_unconfirmed"
	}
	data, err := readShutdownFile(filepath.Join(root, p.namespace+"-"+p.claim+".json"))
	if err != nil {
		return "cleanup_unconfirmed"
	}
	var value struct {
		Record struct {
			Version    int    `json:"version"`
			Namespace  string `json:"namespace"`
			Generation int    `json:"generation"`
			Claim      string `json:"claim_id"`
			Confirmed  bool   `json:"native_cleanup_confirmed"`
		} `json:"record"`
		MAC string `json:"mac"`
	}
	if decodeShutdown(data, &value) != nil || value.Record.Version != 1 || value.Record.Namespace != p.namespace || value.Record.Generation != p.generation || value.Record.Claim != p.claim || !value.Record.Confirmed {
		return "cleanup_unconfirmed"
	}
	mac, err := hex.DecodeString(value.MAC)
	if err != nil || len(mac) != sha256.Size {
		return "cleanup_unconfirmed"
	}
	expected := hmac.New(sha256.New, []byte(p.key))
	expected.Write(shutdownJSON([]any{"native-cleanup-v1", p.namespace, p.generation, p.claim, true}))
	if !hmac.Equal(mac, expected.Sum(nil)) {
		return "cleanup_unconfirmed"
	}
	if _, err := os.Lstat(filepath.Join(p.directory, ".task-writer.lock")); !os.IsNotExist(err) {
		return "cleanup_confirmed_release_unconfirmed"
	}
	return "cleanup_confirmed_release_observed"
}
