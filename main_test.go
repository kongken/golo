package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"
)

func newTestApp(t *testing.T) *app {
	t.Helper()
	db, err := sql.Open("sqlite", "file:test.db?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := initSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	return &app{db: db, config: appConfig{BaseURL: "http://short.test", APIKey: "secret-api-key"}}
}

func newTestRouter(t *testing.T) (*app, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	a := newTestApp(t)
	r := gin.New()
	a.registerRoutes(r)
	return a, r
}

func TestShortenAndRedirect(t *testing.T) {
	a, r := newTestRouter(t)

	form := url.Values{}
	form.Set("link-url", "https://example.com/hello")
	form.Set("custom-ending", "hello")
	req := httptest.NewRequest(http.MethodPost, "/shorten", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("shorten status = %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), "http://short.test/hello") {
		t.Fatalf("response missing short url: %s", res.Body.String())
	}

	redirectReq := httptest.NewRequest(http.MethodGet, "/hello", nil)
	redirectReq.Header.Set("Referer", "https://ref.example")
	redirectRes := httptest.NewRecorder()
	r.ServeHTTP(redirectRes, redirectReq)

	if redirectRes.Code != http.StatusMovedPermanently {
		t.Fatalf("redirect status = %d", redirectRes.Code)
	}
	if got := redirectRes.Header().Get("Location"); got != "https://example.com/hello" {
		t.Fatalf("redirect location = %q", got)
	}
	lnk, err := a.findLinkByCode(req.Context(), "hello")
	if err != nil {
		t.Fatalf("find link: %v", err)
	}
	if lnk.Clicks != 1 {
		t.Fatalf("clicks = %d, want 1", lnk.Clicks)
	}
}

func TestAPIShortenLookupAndStats(t *testing.T) {
	_, r := newTestRouter(t)

	shortenReq := httptest.NewRequest(http.MethodGet, "/api/v2/action/shorten?key=secret-api-key&url=https://example.com/docs&custom_ending=docs", nil)
	shortenRes := httptest.NewRecorder()
	r.ServeHTTP(shortenRes, shortenReq)
	if shortenRes.Code != http.StatusOK {
		t.Fatalf("shorten status = %d body=%s", shortenRes.Code, shortenRes.Body.String())
	}

	redirectReq := httptest.NewRequest(http.MethodGet, "/docs", nil)
	redirectReq.Header.Set("Referer", "https://news.ycombinator.com")
	redirectRes := httptest.NewRecorder()
	r.ServeHTTP(redirectRes, redirectReq)

	lookupReq := httptest.NewRequest(http.MethodGet, "/api/v2/action/lookup?key=secret-api-key&url_ending=docs", nil)
	lookupRes := httptest.NewRecorder()
	r.ServeHTTP(lookupRes, lookupReq)
	if lookupRes.Code != http.StatusOK {
		t.Fatalf("lookup status = %d body=%s", lookupRes.Code, lookupRes.Body.String())
	}
	var lookup map[string]any
	if err := json.Unmarshal(lookupRes.Body.Bytes(), &lookup); err != nil {
		t.Fatalf("decode lookup: %v", err)
	}
	if lookup["action"] != "lookup" {
		t.Fatalf("lookup action = %v", lookup["action"])
	}

	statsReq := httptest.NewRequest(http.MethodGet, "/api/v2/data/link?key=secret-api-key&url_ending=docs&stats_type=referer", nil)
	statsRes := httptest.NewRecorder()
	r.ServeHTTP(statsRes, statsReq)
	if statsRes.Code != http.StatusOK {
		t.Fatalf("stats status = %d body=%s", statsRes.Code, statsRes.Body.String())
	}
	if !strings.Contains(statsRes.Body.String(), "news.ycombinator.com") {
		t.Fatalf("stats missing referer data: %s", statsRes.Body.String())
	}
}
