# Token server (Go)

The one piece of backend a VoltKit app needs, in Go 1.22+ with only the standard library: it keeps your VoltKit
API secret and gives each user a short-lived room token. It exposes the same `POST /token` API as the other VoltKit token servers, so the
[Flutter](https://github.com/voltkit-rtc/flutter-example) and [Android](https://github.com/voltkit-rtc/android-example)
example apps work against it unchanged.

## Run

```bash
cp .env.example .env     # fill in LAB_KEY (any long random string) and VOLTKIT_RTC_SECRET
go run .                 # http://localhost:7000
go run . --check-config  # validate .env and exit
go test ./...            # tests; no VoltKit account needed
```

Build a static binary with `CGO_ENABLED=0 go build -o token-server .`, or use the Dockerfile (distroless, ~10 MB):
`docker build -t voltkit-token-go . && docker run --env-file .env -p 7000:7000 voltkit-token-go`.

## The API

`GET /health` → `{ "status": "ok" }`

`POST /token` with header `X-App-Key: <LAB_KEY>` and body `{ "room": "demo-room", "identity": "asha", "role": "host" }`

```json
{ "wsUrl": "wss://…", "token": "…", "room": "demo-room", "identity": "asha", "role": "host", "expiresAt": 1761040000 }
```

| Field | Rule |
| --- | --- |
| `room` | 3–64 chars of `a-z 0-9 -`, starting with a letter or digit |
| `identity` | 1–64 chars of `A-Z a-z 0-9 . _ -` |
| `role` | `host`, `cohost`, `moderator` or `viewer`; defaults to `host` |

Errors are `{ "error": { "code", "message" } }`:

| Status | Code | When |
| --- | --- | --- |
| 400 | `bad-json`, `bad-room`, `bad-identity`, `bad-role` | Input failed a rule above |
| 401 | `unauthorized` | Missing or wrong `X-App-Key` |
| 404 | `not-found` | Any other route or method |
| 413 | `too-large` | Body over 4 KB |
| 4xx/5xx | VoltKit's own code | VoltKit refused (for example `quota-exhausted` when no minutes are left) |
| 502 | `voltkit-unavailable` | VoltKit did not answer (network error or timeout after retries) |

## The code

| File | Role |
| --- | --- |
| [`app.go`](app.go) | `newHandler()`: check the app key (constant time), check the input, ask VoltKit. Takes the `Rtc` interface so tests pass a fake. |
| [`voltkit.go`](voltkit.go) | `VoltKitClient`: `EnsureRoom()` is `PUT /v1/rooms/{id}`, `CreateToken()` is `POST /v1/rooms/{id}/tokens`, both with `X-API-Key`. Retries network errors, 429 and 5xx twice with backoff; never follows redirects (that would leak the key). |
| [`config.go`](config.go) | Reads `.env` and validates `LAB_KEY`, `VOLTKIT_RTC_URL`, `VOLTKIT_RTC_SECRET`, `PORT`, `HOST`. |
| [`main.go`](main.go) | Starts `http.Server` with sane timeouts and shuts down gracefully on SIGTERM/SIGINT. |
| [`app_test.go`](app_test.go) | Handler tests with a fake VoltKit, plus the real client against an `httptest` fake VoltKit. |

In your own product, replace the `X-App-Key` check with your user login, and decide the `role` from who that user is.
