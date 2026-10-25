package main

// go test ./...   (no network to VoltKit; a fake is used)

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const key = "example-app-key-0123456789"

// fakeRtc records calls and can fail EnsureRoom on demand.
type fakeRtc struct {
	calls       [][]string
	ensureError error
}

func (f *fakeRtc) EnsureRoom(roomID, hostIdentity string) (map[string]any, error) {
	f.calls = append(f.calls, []string{"EnsureRoom", roomID, hostIdentity})
	if f.ensureError != nil {
		return nil, f.ensureError
	}
	return map[string]any{"roomId": roomID, "hostToken": "never-sent"}, nil
}

func (f *fakeRtc) CreateToken(roomID, identity, role string) (map[string]any, error) {
	f.calls = append(f.calls, []string{"CreateToken", roomID, identity, role})
	return map[string]any{"token": "room-token", "wsUrl": "wss://rtc.example/ws", "identity": identity, "role": role, "expiresAt": float64(123)}, nil
}

func post(t *testing.T, h http.Handler, body string, appKey string) (int, map[string]any, http.Header) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if appKey != "" {
		req.Header.Set("X-App-Key", appKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header()
}

func TestTokenFlow(t *testing.T) {
	rtc := &fakeRtc{}
	h := newHandler(key, rtc)
	status, body, headers := post(t, h, `{"room":"demo-room","identity":"asha","role":"viewer"}`, key)
	if status != 200 || body["token"] != "room-token" || body["role"] != "viewer" || body["room"] != "demo-room" || body["expiresAt"] != float64(123) {
		t.Fatalf("got %d %v", status, body)
	}
	if headers.Get("Cache-Control") != "no-store" {
		t.Fatal("expected Cache-Control: no-store")
	}
	if len(rtc.calls) != 2 || rtc.calls[0][0] != "EnsureRoom" || rtc.calls[1][3] != "viewer" {
		t.Fatalf("unexpected calls %v", rtc.calls)
	}
	if _, body, _ := post(t, h, `{"room":"demo-room","identity":"bo"}`, key); body["role"] != "host" {
		t.Fatal("role should default to host")
	}
}

func TestAppKeyRequired(t *testing.T) {
	rtc := &fakeRtc{}
	h := newHandler(key, rtc)
	for _, k := range []string{"", "wrong-key"} {
		if status, _, _ := post(t, h, `{"room":"demo-room","identity":"a"}`, k); status != 401 {
			t.Fatalf("key %q: got %d", k, status)
		}
	}
	if len(rtc.calls) != 0 {
		t.Fatal("VoltKit must not be called")
	}
}

func TestBadInput(t *testing.T) {
	rtc := &fakeRtc{}
	h := newHandler(key, rtc)
	for _, body := range []string{`{"identity":"a"}`, `{"room":"X","identity":"a"}`, `{"room":"demo-room","identity":"has space"}`, `{"room":"demo-room","identity":"a","role":"owner"}`, `{bad json`} {
		if status, _, _ := post(t, h, body, key); status != 400 {
			t.Fatalf("%s: got %d", body, status)
		}
	}
	if status, _, _ := post(t, h, `{"room":"`+strings.Repeat("x", 5000)+`"}`, key); status != 413 {
		t.Fatalf("too large: got %d", status)
	}
	if len(rtc.calls) != 0 {
		t.Fatal("VoltKit must not be called")
	}
}

func TestVoltKitErrorsAndRoutes(t *testing.T) {
	h := newHandler(key, &fakeRtc{ensureError: &VoltKitError{Code: "quota-exhausted", Status: 402, Message: "No minutes left"}})
	status, body, _ := post(t, h, `{"room":"demo-room","identity":"a"}`, key)
	if status != 402 || body["error"].(map[string]any)["code"] != "quota-exhausted" {
		t.Fatalf("got %d %v", status, body)
	}
	h = newHandler(key, &fakeRtc{ensureError: &VoltKitError{Code: "network", Status: 0, Message: "timed out"}})
	if status, _, _ := post(t, h, `{"room":"demo-room","identity":"a"}`, key); status != 502 {
		t.Fatalf("network: got %d", status)
	}
	for path, want := range map[string]int{"/health": 200, "/voltkit/v1/lab/rooms": 404} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Fatalf("GET %s: got %d", path, rec.Code)
		}
	}
}

func TestConfig(t *testing.T) {
	base := map[string]string{"LAB_KEY": key, "VOLTKIT_RTC_URL": "https://rtc.example", "VOLTKIT_RTC_SECRET": "secret"}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cfg, err := readConfig(env(base))
	if err != nil || cfg.Port != 7000 {
		t.Fatalf("valid config rejected: %v", err)
	}
	for k, v := range map[string]string{"LAB_KEY": "short", "VOLTKIT_RTC_URL": "http://rtc.example", "VOLTKIT_RTC_SECRET": ""} {
		m := map[string]string{}
		for kk, vv := range base {
			m[kk] = vv
		}
		m[k] = v
		if _, err := readConfig(env(m)); err == nil || !strings.Contains(err.Error(), k) {
			t.Fatalf("%s should be rejected, got %v", k, err)
		}
	}
}

// TestClient runs the real client against a fake VoltKit served by httptest.
func TestClient(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/rooms/quota-room":
			w.WriteHeader(402)
			_, _ = w.Write([]byte(`{"error":"quota-exhausted","message":"No minutes left"}`))
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/rooms/"):
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"roomId": strings.TrimPrefix(r.URL.Path, "/v1/rooms/"), "hostIdentity": body["hostIdentity"], "seenKey": r.Header.Get("X-API-Key")})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tokens"):
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "tok", "wsUrl": "wss://rtc.example/ws", "identity": body["identity"], "role": body["role"], "expiresAt": 123})
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"not-found","message":"no such route"}`))
		}
	}))
	defer fake.Close()

	c := NewVoltKitClient(fake.URL, "secret-key", 0)
	room, err := c.EnsureRoom("demo-room", "asha")
	if err != nil || room["roomId"] != "demo-room" || room["seenKey"] != "secret-key" {
		t.Fatalf("EnsureRoom: %v %v", room, err)
	}
	tok, err := c.CreateToken("demo-room", "asha", "viewer")
	if err != nil || tok["token"] != "tok" || tok["role"] != "viewer" {
		t.Fatalf("CreateToken: %v %v", tok, err)
	}
	_, err = c.EnsureRoom("quota-room", "asha")
	var ve *VoltKitError
	if !errors.As(err, &ve) || ve.Status != 402 || ve.Code != "quota-exhausted" {
		t.Fatalf("expected quota-exhausted 402, got %v", err)
	}
}
