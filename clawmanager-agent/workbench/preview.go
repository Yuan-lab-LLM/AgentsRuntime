package workbench

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/netrelay"
)

const PreviewHeader = "X-ClawManager-Workbench-Preview"
const PreviewPath = "/v1/workbench/preview"

type PreviewRoute struct {
	InstanceID int    `json:"instance_id"`
	UserID     int    `json:"user_id"`
	Generation int    `json:"generation"`
	GatewayID  string `json:"gateway_id"`
	Scheme     string `json:"scheme"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Path       string `json:"path"`
	RawPath    string `json:"raw_path,omitempty"`
	RawQuery   string `json:"raw_query,omitempty"`
}

func (p PreviewRoute) Scope() Request {
	return Request{InstanceID: p.InstanceID, UserID: p.UserID, Generation: p.Generation, GatewayID: p.GatewayID}
}
func (p PreviewRoute) URL() *url.URL {
	return &url.URL{Scheme: p.Scheme, Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), Path: p.Path, RawPath: p.RawPath, RawQuery: p.RawQuery}
}
func DecodePreview(r *http.Request) (PreviewRoute, error) {
	var p PreviewRoute
	values := r.Header.Values(PreviewHeader)
	if len(values) != 1 || len(values[0]) > base64.RawURLEncoding.EncodedLen(16<<10) {
		return p, Invalid()
	}
	raw, err := base64.RawURLEncoding.DecodeString(values[0])
	if err != nil || len(raw) > 16<<10 {
		return p, Invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil || decoder.Decode(new(any)) != io.EOF {
		return p, Invalid()
	}
	host := p.Host
	if host == "localhost" {
		host = "127.0.0.1"
	}
	if p.InstanceID <= 0 || p.UserID <= 0 || p.Generation <= 0 || p.GatewayID == "" || (p.Scheme != "http" && p.Scheme != "https") || !netrelay.PreviewTarget(net.JoinHostPort(host, strconv.Itoa(p.Port)), nil) || !strings.HasPrefix(p.Path, "/") || strings.ContainsAny(p.Path+p.RawPath+p.RawQuery, "\r\n\x00") {
		return p, Invalid()
	}
	if p.RawPath != "" {
		decoded, err := url.PathUnescape(p.RawPath)
		if err != nil || decoded != p.Path || p.URL().EscapedPath() != p.RawPath {
			return p, Invalid()
		}
	}
	return p, nil
}
