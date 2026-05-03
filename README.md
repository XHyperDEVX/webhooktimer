# Webhook Timer

A tiny 24/7 webhook scheduler with a minimal web UI.

## What this rebuild includes

- Extremely simple UI (single page, no frontend dependencies)
- Fixed or random intervals
- Sleep window (no executions during configured quiet time)
- Per-entry enable/disable
- Live countdown to next execution
- Last execution status and timestamp
- Per-entry execution log (last 10)
- **Run now** button with immediate success/error feedback
- Atomic JSON state persistence (new format, old DB files are intentionally not reused)

## Why it is lightweight

- Pure Go + standard library only
- No JS framework, no CSS framework, no websocket dependency
- Static binary in a `scratch` container image

## Run locally

```bash
go run .
```

Open: `http://localhost:8080`

## Docker

```bash
docker compose up --build
```

### Environment variables

- `PORT` (default `8080`)
- `STATE_PATH` (default `/data/state.json`)
- `TZ` (default `UTC`, used for sleep window calculations)

## API (used by the UI)

- `GET /api/entries`
- `POST /api/entries`
- `PUT /api/entries/{id}`
- `DELETE /api/entries/{id}`
- `POST /api/entries/{id}/toggle`
- `POST /api/entries/{id}/execute`
- `GET /api/entries/{id}/logs`

## Notes

- The new persistence format is JSON, not SQLite.
- If an old database exists from previous versions, it is ignored by design.
