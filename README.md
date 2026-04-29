# golo

`golo` is a Go rewrite of the core Polr URL shortener flow.

It includes:

- Web UI for shortening links
- Redirects for public and secret links
- SQLite-backed storage with automatic schema creation
- Polr-style `api/v2` endpoints for shorten, bulk shorten, lookup, and basic analytics

## Run

```bash
go run .
```

Environment variables:

- `PORT`: listen port, default `8080`
- `BASE_URL`: public base URL, defaults to the incoming request host
- `DATABASE_PATH`: sqlite database path, default `golo.db`
- `API_KEY`: optional shared API key for `/api/v2` endpoints

## API Compatibility

Implemented endpoints:

- `GET|POST /api/v2/action/shorten`
- `POST /api/v2/action/shorten_bulk`
- `GET|POST /api/v2/action/lookup`
- `GET|POST /api/v2/data/link`

Analytics support is intentionally lightweight in this first Go port:

- `day`: daily click counts
- `country`: grouped as `Unknown` without GeoIP integration
- `referer`: grouped by request `Referer`, with empty values normalized to `Direct`
