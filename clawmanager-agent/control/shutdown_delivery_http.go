package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

var errShutdownDeliveryHTTP = errors.New("shutdown diagnostic delivery not confirmed")

// Separate from legacy doJSON: only an explicit successful application envelope
// is acceptable, not an empty 2xx, a bare payload, an error envelope or a redirect.
func shutdownDeliveryJSON(ctx context.Context, client *http.Client, endpoint, authHeader, authValue, method string, payload, target any) error {
	var reader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return errShutdownDeliveryHTTP
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return errShutdownDeliveryHTTP
	}
	req.Header.Set(authHeader, authValue)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "no-cache, no-store")
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := copyClient.Do(req)
	if err != nil {
		return errShutdownDeliveryHTTP
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errShutdownDeliveryHTTP
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return errShutdownDeliveryHTTP
	}
	var envelope struct {
		Success bool            `json:"success"`
		Error   string          `json:"error"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || !envelope.Success || envelope.Error != "" || len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, target) != nil {
		return errShutdownDeliveryHTTP
	}
	return nil
}
