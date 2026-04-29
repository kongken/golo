package main

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>golo</title>
  <style>
    body { font-family: Arial, sans-serif; margin: 0; background: #0f172a; color: #e2e8f0; }
    main { max-width: 720px; margin: 0 auto; padding: 40px 20px; }
    h1 { font-size: 2.5rem; margin-bottom: 0.25rem; }
    p { color: #94a3b8; }
    form, .panel { background: #111827; border: 1px solid #334155; border-radius: 16px; padding: 20px; margin-top: 24px; }
    label { display: block; margin-top: 12px; margin-bottom: 6px; }
    input[type="url"], input[type="text"] { width: 100%; box-sizing: border-box; padding: 12px; border-radius: 10px; border: 1px solid #475569; background: #020617; color: #e2e8f0; }
    button { margin-top: 16px; padding: 12px 16px; background: #22c55e; color: #052e16; border: 0; border-radius: 10px; font-weight: bold; cursor: pointer; }
    .error { color: #fca5a5; }
    .result { color: #86efac; word-break: break-all; }
    code { color: #bfdbfe; }
  </style>
</head>
<body>
  <main>
    <h1>golo</h1>
    <p>A small Go rewrite of the core Polr shortener workflow.</p>
    <form method="post" action="/shorten">
      <label for="link-url">Destination URL</label>
      <input id="link-url" name="link-url" type="url" placeholder="https://example.com" required>
      <label for="custom-ending">Custom ending</label>
      <input id="custom-ending" name="custom-ending" type="text" placeholder="optional-slug">
      <label><input name="options" type="checkbox" value="s"> Secret link</label>
      <button type="submit">Shorten</button>
    </form>
    {{if .Error}}<div class="panel error">{{.Error}}</div>{{end}}
    {{if .Result}}<div class="panel result">Short URL: <a href="{{.Result}}">{{.Result}}</a></div>{{end}}
    {{if .Secret}}<div class="panel">Secret key: <code>{{.Secret}}</code></div>{{end}}
  </main>
</body>
</html>`

var indexTemplate = template.Must(template.New("index").Parse(indexHTML))

type config struct {
	port         string
	baseURL      string
	databasePath string
	apiKey       string
}

type app struct {
	db     *sql.DB
	config config
}

type link struct {
	ID        int64
	Code      string
	LongURL   string
	SecretKey string
	Clicks    int64
	CreatedAt time.Time
	UpdatedAt time.Time
	Disabled  bool
}

type indexData struct {
	Error  string
	Result string
	Secret string
}

func main() {
	cfg := config{
		port:         envOrDefault("PORT", "8080"),
		baseURL:      strings.TrimRight(os.Getenv("BASE_URL"), "/"),
		databasePath: envOrDefault("DATABASE_PATH", "golo.db"),
		apiKey:       os.Getenv("API_KEY"),
	}

	db, err := sql.Open("sqlite", cfg.databasePath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		log.Fatalf("init schema: %v", err)
	}

	application := &app{db: db, config: cfg}
	addr := ":" + cfg.port
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, application); err != nil {
		log.Fatal(err)
	}
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		a.renderIndex(w, indexData{})
	case r.URL.Path == "/shorten" && r.Method == http.MethodPost:
		a.handleShortenForm(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v2/"):
		a.handleAPI(w, r)
	default:
		a.handleRedirect(w, r)
	}
}

func (a *app) renderIndex(w http.ResponseWriter, data indexData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *app) handleShortenForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.renderIndex(w, indexData{Error: "invalid form submission"})
		return
	}

	created, err := a.createLink(r.Context(), createLinkInput{
		LongURL:      r.FormValue("link-url"),
		CustomEnding: r.FormValue("custom-ending"),
		Secret:       r.FormValue("options") == "s",
	})
	if err != nil {
		a.renderIndex(w, indexData{Error: err.Error()})
		return
	}

	a.renderIndex(w, indexData{
		Result: a.fullShortURL(r, created.Code, created.SecretKey),
		Secret: created.SecretKey,
	})
}

func (a *app) handleAPI(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.writeAPIError(w, r, http.StatusBadRequest, "MISSING_PARAMETERS", "Invalid or missing parameters.")
		return
	}

	if a.config.apiKey != "" && r.FormValue("key") != a.config.apiKey {
		a.writeAPIError(w, r, http.StatusUnauthorized, "AUTH_ERROR", "Invalid API key.")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/v2/")
	switch path {
	case "action/shorten":
		a.handleAPIShorten(w, r)
	case "action/shorten_bulk":
		a.handleAPIShortenBulk(w, r)
	case "action/lookup":
		a.handleAPILookup(w, r)
	case "data/link":
		a.handleAPIDataLink(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *app) handleAPIShorten(w http.ResponseWriter, r *http.Request) {
	created, err := a.createLink(r.Context(), createLinkInput{
		LongURL:      r.FormValue("url"),
		CustomEnding: r.FormValue("custom_ending"),
		Secret:       r.FormValue("is_secret") == "true",
	})
	if err != nil {
		a.writeAPIError(w, r, http.StatusBadRequest, "CREATION_ERROR", err.Error())
		return
	}

	a.writeAPIResponse(w, r, "shorten", a.fullShortURL(r, created.Code, created.SecretKey))
}

func (a *app) handleAPIShortenBulk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Links []struct {
			URL          string `json:"url"`
			IsSecret     bool   `json:"is_secret"`
			CustomEnding string `json:"custom_ending"`
		} `json:"links"`
	}
	if err := json.Unmarshal([]byte(r.FormValue("data")), &payload); err != nil {
		a.writeAPIError(w, r, http.StatusBadRequest, "INVALID_PARAMETERS", "Invalid JSON.")
		return
	}

	shortened := make([]map[string]string, 0, len(payload.Links))
	for _, item := range payload.Links {
		created, err := a.createLink(r.Context(), createLinkInput{
			LongURL:      item.URL,
			CustomEnding: item.CustomEnding,
			Secret:       item.IsSecret,
		})
		if err != nil {
			a.writeAPIError(w, r, http.StatusBadRequest, "CREATION_ERROR", err.Error())
			return
		}
		shortened = append(shortened, map[string]string{
			"long_url":  item.URL,
			"short_url": a.fullShortURL(r, created.Code, created.SecretKey),
		})
	}

	a.writeJSON(w, http.StatusOK, map[string]any{
		"action": "shorten_bulk",
		"result": map[string]any{"shortened_links": shortened},
	})
}

func (a *app) handleAPILookup(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("url_ending")
	if !isAlphaDash(code) {
		a.writeAPIError(w, r, http.StatusBadRequest, "MISSING_PARAMETERS", "Invalid or missing parameters.")
		return
	}

	lnk, err := a.findLinkByCode(r.Context(), code)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "Link not found.")
			return
		}
		a.writeAPIError(w, r, http.StatusInternalServerError, "LOOKUP_ERROR", "Unable to load link.")
		return
	}

	if lnk.SecretKey != "" && r.FormValue("url_key") != lnk.SecretKey {
		a.writeAPIError(w, r, http.StatusUnauthorized, "ACCESS_DENIED", "Invalid URL code for secret URL.")
		return
	}

	result := map[string]any{
		"long_url":   lnk.LongURL,
		"created_at": map[string]any{"date": lnk.CreatedAt.UTC().Format("2006-01-02 15:04:05.000000"), "timezone_type": 3, "timezone": "UTC"},
		"updated_at": map[string]any{"date": lnk.UpdatedAt.UTC().Format("2006-01-02 15:04:05.000000"), "timezone_type": 3, "timezone": "UTC"},
		"clicks":     fmt.Sprintf("%d", lnk.Clicks),
	}

	a.writeAPIResponse(w, r, "lookup", result)
}

func (a *app) handleAPIDataLink(w http.ResponseWriter, r *http.Request) {
	if responseType(r) == "plain_text" {
		a.writeAPIError(w, r, http.StatusUnauthorized, "JSON_ONLY", "Only JSON-encoded data is available for this endpoint.")
		return
	}

	code := r.FormValue("url_ending")
	statsType := r.FormValue("stats_type")
	if !isAlphaDash(code) || statsType == "" {
		a.writeAPIError(w, r, http.StatusBadRequest, "MISSING_PARAMETERS", "Invalid or missing parameters.")
		return
	}

	lnk, err := a.findLinkByCode(r.Context(), code)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "Link not found.")
			return
		}
		a.writeAPIError(w, r, http.StatusInternalServerError, "ANALYTICS_ERROR", "Unable to load link.")
		return
	}

	left, right, err := parseBounds(r.FormValue("left_bound"), r.FormValue("right_bound"))
	if err != nil {
		a.writeAPIError(w, r, http.StatusBadRequest, "MISSING_PARAMETERS", "Invalid or missing parameters.")
		return
	}

	data, err := a.analytics(r.Context(), lnk.ID, statsType, left, right)
	if err != nil {
		status := http.StatusBadRequest
		code := "INVALID_ANALYTICS_TYPE"
		if !errors.Is(err, errInvalidAnalyticsType) {
			status = http.StatusInternalServerError
			code = "ANALYTICS_ERROR"
		}
		a.writeAPIError(w, r, status, code, err.Error())
		return
	}

	a.writeJSON(w, http.StatusOK, map[string]any{
		"action": "data_link_" + statsType,
		"result": map[string]any{
			"url_ending": lnk.Code,
			"data":       data,
		},
	})
}

func (a *app) handleRedirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" || len(parts) > 2 {
		http.NotFound(w, r)
		return
	}

	lnk, err := a.findLinkByCode(r.Context(), parts[0])
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}

	if lnk.Disabled {
		http.Error(w, "link has been disabled", http.StatusForbidden)
		return
	}

	if lnk.SecretKey != "" {
		if len(parts) != 2 || parts[1] != lnk.SecretKey {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	if err := a.recordClick(r.Context(), lnk.ID, r.Referer()); err != nil {
		http.Error(w, "analytics failed", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, lnk.LongURL, http.StatusMovedPermanently)
}

type createLinkInput struct {
	LongURL      string
	CustomEnding string
	Secret       bool
}

func (a *app) createLink(ctx context.Context, input createLinkInput) (*link, error) {
	if !validURL(input.LongURL) {
		return nil, errors.New("invalid or missing URL")
	}
	if input.CustomEnding != "" && !isAlphaDash(input.CustomEnding) {
		return nil, errors.New("custom ending must be alpha_dash")
	}

	code := input.CustomEnding
	if code == "" {
		generated, err := a.generateCode(ctx)
		if err != nil {
			return nil, err
		}
		code = generated
	}

	secretKey := ""
	if input.Secret {
		generated, err := randomHex(2)
		if err != nil {
			return nil, err
		}
		secretKey = generated
	}

	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	result, err := a.db.ExecContext(ctx, `
		INSERT INTO links (code, long_url, secret_key, clicks, is_disabled, created_at, updated_at)
		VALUES (?, ?, ?, 0, 0, ?, ?)
	`, code, input.LongURL, secretKey, nowText, nowText)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, errors.New("custom ending already in use")
		}
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &link{ID: id, Code: code, LongURL: input.LongURL, SecretKey: secretKey, Clicks: 0, CreatedAt: now, UpdatedAt: now}, nil
}

func (a *app) generateCode(ctx context.Context) (string, error) {
	for i := 0; i < 10; i++ {
		candidate, err := randomBase62(6)
		if err != nil {
			return "", err
		}
		_, err = a.findLinkByCode(ctx, candidate)
		if errors.Is(err, sql.ErrNoRows) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("unable to allocate short code")
}

func (a *app) findLinkByCode(ctx context.Context, code string) (*link, error) {
	var lnk link
	var secret sql.NullString
	var createdAt string
	var updatedAt string
	var disabled int
	err := a.db.QueryRowContext(ctx, `
		SELECT id, code, long_url, secret_key, clicks, is_disabled, created_at, updated_at
		FROM links WHERE code = ?
	`, code).Scan(&lnk.ID, &lnk.Code, &lnk.LongURL, &secret, &lnk.Clicks, &disabled, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	lnk.SecretKey = secret.String
	lnk.Disabled = disabled == 1
	lnk.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, err
	}
	lnk.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return nil, err
	}
	return &lnk, nil
}

func (a *app) recordClick(ctx context.Context, linkID int64, referer string) error {
	if referer == "" {
		referer = "Direct"
	}
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	_, err := a.db.ExecContext(ctx, `
		UPDATE links SET clicks = clicks + 1, updated_at = ? WHERE id = ?;
	`, nowText, linkID)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `
		INSERT INTO clicks (link_id, referer, country, created_at) VALUES (?, ?, 'Unknown', ?)
	`, linkID, referer, nowText)
	return err
}

var errInvalidAnalyticsType = errors.New("invalid analytics type requested")

func (a *app) analytics(ctx context.Context, linkID int64, statsType string, left time.Time, right time.Time) ([]map[string]any, error) {
	queryBase := "FROM clicks WHERE link_id = ? AND created_at >= ? AND created_at <= ?"
	switch statsType {
	case "day":
		rows, err := a.db.QueryContext(ctx, `
			SELECT substr(created_at, 1, 10) AS day, COUNT(*) `+queryBase+` GROUP BY day ORDER BY day
		`, linkID, left.UTC().Format(time.RFC3339Nano), right.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var result []map[string]any
		for rows.Next() {
			var day string
			var count int64
			if err := rows.Scan(&day, &count); err != nil {
				return nil, err
			}
			result = append(result, map[string]any{"x": day, "y": count})
		}
		return result, rows.Err()
	case "country", "referer":
		column := statsType
		rows, err := a.db.QueryContext(ctx, `
			SELECT `+column+`, COUNT(*) `+queryBase+` GROUP BY `+column+` ORDER BY COUNT(*) DESC, `+column+`
		`, linkID, left.UTC().Format(time.RFC3339Nano), right.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var result []map[string]any
		for rows.Next() {
			var label string
			var count int64
			if err := rows.Scan(&label, &count); err != nil {
				return nil, err
			}
			result = append(result, map[string]any{"label": label, "clicks": count})
		}
		return result, rows.Err()
	default:
		return nil, errInvalidAnalyticsType
	}
}

func (a *app) fullShortURL(r *http.Request, code string, secret string) string {
	base := a.config.baseURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	if secret == "" {
		return base + "/" + code
	}
	return base + "/" + code + "/" + secret
}

func (a *app) writeAPIResponse(w http.ResponseWriter, r *http.Request, action string, result any) {
	if responseType(r) == "plain_text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, result)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"action": action, "result": result})
}

func (a *app) writeAPIError(w http.ResponseWriter, r *http.Request, status int, code string, message string) {
	if responseType(r) == "plain_text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		fmt.Fprintf(w, "%d %s", status, message)
		return
	}
	a.writeJSON(w, status, map[string]any{"status_code": status, "error_code": code, "error": message})
}

func (a *app) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func initSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS links (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			code TEXT NOT NULL UNIQUE,
			long_url TEXT NOT NULL,
			secret_key TEXT,
			clicks INTEGER NOT NULL DEFAULT 0,
			is_disabled INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS clicks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			link_id INTEGER NOT NULL,
			referer TEXT NOT NULL,
			country TEXT NOT NULL,
			created_at TEXT NOT NULL,
			FOREIGN KEY(link_id) REFERENCES links(id)
		);
	`)
	return err
}

func parseBounds(leftRaw string, rightRaw string) (time.Time, time.Time, error) {
	right := time.Now().UTC()
	left := right.AddDate(-1, 0, 0)
	var err error
	if leftRaw != "" {
		left, err = parseFlexibleTime(leftRaw)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if rightRaw != "" {
		right, err = parseFlexibleTime(rightRaw)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	return left, right, nil
}

func parseFlexibleTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errors.New("invalid time")
}

func responseType(r *http.Request) string {
	if r.FormValue("response_type") == "plain_text" {
		return "plain_text"
	}
	return "json"
}

func validURL(raw string) bool {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func isAlphaDash(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func randomBase62(length int) (string, error) {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	var sb strings.Builder
	for i := 0; i < length; i++ {
		n, err := crand.Int(crand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		sb.WriteByte(alphabet[n.Int64()])
	}
	return sb.String(), nil
}

func randomHex(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := crand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func envOrDefault(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
