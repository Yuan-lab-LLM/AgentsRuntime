package workbench

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestPreviewRouteValidation(t *testing.T) {
	valid := PreviewRoute{InstanceID: 1, UserID: 2, Generation: 3, GatewayID: "gw", Scheme: "http", Host: "localhost", Port: 3000, Path: "/a/b", RawPath: "/a%2Fb", RawQuery: "q=a%2Fb"}
	for _, kind := range []string{"valid", "host", "port", "helper", "credentials", "zone", "path", "raw", "scope", "scheme"} {
		t.Run(kind, func(t *testing.T) {
			route := valid
			switch kind {
			case "host":
				route.Host = "example.com"
			case "port":
				route.Port = 65536
			case "helper":
				route.Port = 15000
			case "credentials":
				route.Host = "user@localhost"
			case "zone":
				route.Host = "::1%lo0"
			case "path":
				route.Path = "http://evil/"
			case "raw":
				route.RawPath = "/different"
			case "scope":
				route.Generation = 0
			case "scheme":
				route.Scheme = "file"
			}
			raw, _ := json.Marshal(route)
			r := httptest.NewRequest("GET", "/v1/workbench/preview", nil)
			r.Header.Set(PreviewHeader, base64.RawURLEncoding.EncodeToString(raw))
			_, err := DecodePreview(r)
			if (err == nil) != (kind == "valid") {
				t.Fatalf("validation %v", err)
			}
		})
	}
}
