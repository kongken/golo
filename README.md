# golo

`golo` is a Go rewrite of the core Polr URL shortener flow built on Butterfly Core.

It includes:

- Web UI for shortening links
- Butterfly Core app lifecycle and Gin route integration
- Redirects for public and secret links
- SQLite-backed storage with automatic schema creation
- Polr-style `api/v2` endpoints for shorten, bulk shorten, lookup, and basic analytics

## Run

```bash
go run .
```

By default the app boots through Butterfly Core using the checked-in file config at `config/golo.yaml`.

Relevant settings:

- `BUTTERFLY_CONFIG_FILE_PATH`: override the Butterfly config file path
- `BUTTERFLY_TRACING_DISABLE=true`: enabled by default in `main.go` for local runs
- `base_url`: public base URL in `config/golo.yaml`, defaults to the incoming request host when empty
- `database_path`: sqlite database path in `config/golo.yaml`
- `api_key`: optional shared API key for `/api/v2` endpoints in `config/golo.yaml`

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
