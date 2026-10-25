package main

// A minimal VoltKit REST client (net/http, no dependencies).
//
// Only the two calls a token server needs:
//   EnsureRoom   PUT  /v1/rooms/{roomId}         create the room or return it (idempotent)
//   CreateToken  POST /v1/rooms/{roomId}/tokens  mint a short-lived token for one identity + role
//
// Every request carries the tenant secret in X-API-Key; apps never reach this code.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"time"
)

const maxAttempts = 3 // original try + 2 retries on transient failures

// VoltKitError is a failed VoltKit call. Status is the HTTP status (0 = network).
type VoltKitError struct {
	Code    string
	Status  int
	Message string
}

func (e *VoltKitError) Error() string { return e.Message }

// retryable: network failures, 429 and 5xx, except the 503 that lasts until an operator acts.
func (e *VoltKitError) retryable() bool {
	return (e.Status == 0 || e.Status == 429 || e.Status >= 500) && e.Code != "billing-unavailable"
}

// Rtc is what the handler needs from VoltKit; VoltKitClient implements it, tests fake it.
type Rtc interface {
	EnsureRoom(roomID, hostIdentity string) (map[string]any, error)
	CreateToken(roomID, identity, role string) (map[string]any, error)
}

type VoltKitClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewVoltKitClient(baseURL, apiKey string, timeout time.Duration) *VoltKitClient {
	return &VoltKitClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: timeout,
			// Never follow a redirect: doing so would hand X-API-Key to whatever host it names.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// EnsureRoom creates the room, or gets it back if it already exists. One round trip either way.
func (c *VoltKitClient) EnsureRoom(roomID, hostIdentity string) (map[string]any, error) {
	return c.request(http.MethodPut, "/v1/rooms/"+url.PathEscape(roomID), map[string]any{"hostIdentity": hostIdentity})
}

// CreateToken mints a room token. The role is decided here, on the server, and signed into the token.
func (c *VoltKitClient) CreateToken(roomID, identity, role string) (map[string]any, error) {
	return c.request(http.MethodPost, "/v1/rooms/"+url.PathEscape(roomID)+"/tokens", map[string]any{"identity": identity, "role": role})
}

// request sends one JSON request, retrying transient failures with jittered backoff.
func (c *VoltKitClient) request(method, path string, body any) (map[string]any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(200*time.Millisecond<<(attempt-1) + time.Duration(rand.Intn(100))*time.Millisecond)
		}
		out, err := c.once(method, path, payload)
		if err == nil {
			return out, nil
		}
		var ve *VoltKitError
		if !errors.As(err, &ve) || !ve.retryable() {
			return nil, err
		}
		last = err
	}
	return nil, last
}

func (c *VoltKitClient) once(method, path string, payload []byte) (map[string]any, error) {
	req, err := http.NewRequest(method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, &VoltKitError{Code: "network", Status: 0, Message: fmt.Sprintf("%s %s: %v", method, path, err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.apiKey)

	res, err := c.http.Do(req)
	if err != nil {
		// err may quote the URL; it never contains the key (that is a header).
		return nil, &VoltKitError{Code: "network", Status: 0, Message: fmt.Sprintf("%s %s: %v", method, path, err)}
	}
	defer res.Body.Close()
	text, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, &VoltKitError{Code: "network", Status: 0, Message: fmt.Sprintf("%s %s: %v", method, path, err)}
	}

	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return nil, &VoltKitError{Code: "redirected", Status: res.StatusCode,
			Message: fmt.Sprintf("%s %s: redirected; point VOLTKIT_RTC_URL at the controller itself.", method, path)}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		code, message := errorReply(text)
		if code == "" {
			code = fmt.Sprintf("http-%d", res.StatusCode)
		}
		if message == "" {
			message = truncate(string(text), 300)
		}
		return nil, &VoltKitError{Code: code, Status: res.StatusCode, Message: fmt.Sprintf("%s %s: %s", method, path, message)}
	}
	var out map[string]any
	if err := json.Unmarshal(text, &out); err != nil || out == nil {
		return nil, &VoltKitError{Code: fmt.Sprintf("http-%d", res.StatusCode), Status: res.StatusCode,
			Message: fmt.Sprintf("%s %s: the reply is not JSON.", method, path)}
	}
	return out, nil
}

// errorReply extracts code and message from an error body. VoltKit answers
// { error, message } or nested { error: { code, message } }; anything else yields "".
func errorReply(text []byte) (code, message string) {
	var flat struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(text, &flat) != nil {
		return "", ""
	}
	var nested struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(flat.Error, &nested) == nil {
		return nested.Code, nested.Message
	}
	var s string
	if json.Unmarshal(flat.Error, &s) == nil {
		return s, flat.Message
	}
	return "", flat.Message
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
