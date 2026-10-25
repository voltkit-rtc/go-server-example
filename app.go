package main

// The whole token server. Your app asks for a room token, this server checks the
// app is yours and asks VoltKit for one. The VoltKit API secret never leaves the
// server; the app only ever holds a short-lived room token.

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
)

var (
	roomRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,63}$`)
	identityRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	roles      = []string{"host", "cohost", "moderator", "viewer"}
)

const maxBodyBytes = 4096

// tokenRequest is the JSON body of POST /token.
type tokenRequest struct {
	Room     string `json:"room"`
	Identity string `json:"identity"`
	Role     string `json:"role"`
}

// newHandler builds the HTTP handler. appKey is LAB_KEY; rtc is the VoltKit client (or a fake).
func newHandler(appKey string, rtc Rtc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		// 1. Check the caller is your app. In a real product this is your own user login.
		if !sameKey(r.Header.Get("X-App-Key"), appKey) {
			fail(w, 401, "unauthorized", "Wrong app key.")
			return
		}

		// 2. Check the input.
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, 413, "too-large", "Body too large.")
			return
		}
		var in tokenRequest
		if err != nil || json.Unmarshal(raw, &in) != nil {
			fail(w, 400, "bad-json", "Send a JSON body.")
			return
		}
		if in.Role == "" {
			in.Role = "host"
		}
		if !roomRe.MatchString(in.Room) {
			fail(w, 400, "bad-room", "room: 3–64 of a-z 0-9 -")
			return
		}
		if !identityRe.MatchString(in.Identity) {
			fail(w, 400, "bad-identity", "identity: 1–64 of A-Z a-z 0-9 . _ -")
			return
		}
		if !contains(roles, in.Role) {
			fail(w, 400, "bad-role", "role: "+strings.Join(roles, ", "))
			return
		}

		// 3. Make sure the room exists, then mint a token for this person and role.
		if _, err := rtc.EnsureRoom(in.Room, in.Identity); err != nil {
			failVoltKit(w, err)
			return
		}
		t, err := rtc.CreateToken(in.Room, in.Identity, in.Role)
		if err != nil {
			failVoltKit(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, map[string]any{
			"wsUrl":     t["wsUrl"],
			"token":     t["token"],
			"room":      in.Room,
			"identity":  orDefault(t["identity"], in.Identity),
			"role":      orDefault(t["role"], in.Role),
			"expiresAt": t["expiresAt"],
		})
	})
	// Everything else, including wrong methods on known paths.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fail(w, 404, "not-found", "Use POST /token.")
	})
	return mux
}

// failVoltKit maps a VoltKit error: its own 4xx/5xx keep status and code (e.g. no
// minutes left); a network failure becomes 502; anything else is a 500.
func failVoltKit(w http.ResponseWriter, err error) {
	var ve *VoltKitError
	if errors.As(err, &ve) {
		if ve.Status >= 400 {
			fail(w, ve.Status, ve.Code, ve.Message)
			return
		}
		fail(w, 502, "voltkit-unavailable", "VoltKit did not answer.")
		return
	}
	log.Printf("Request failed: %T", err)
	fail(w, 500, "internal", "Unexpected server error.")
}

// fail writes the error shape every failure uses: { "error": { "code", "message" } }.
func fail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// sameKey is a constant-time comparison, so the key cannot be guessed from response times.
func sameKey(given, expected string) bool {
	return given != "" && subtle.ConstantTimeCompare([]byte(given), []byte(expected)) == 1
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func orDefault(v any, fallback string) any {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}
