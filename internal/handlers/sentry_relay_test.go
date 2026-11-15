package handlers

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

// Sentry rölesi testleri veritabanına dokunmaz; Sentry yerine httptest
// sunucusu kullanılır.

const (
	roleAnahtar = "edb7aaaabbbbccccdddd"
	roleProje   = "4512437775106048"
	roleYol     = "/api/v1/sentry/api/" + roleProje + "/envelope/"
	roleAuth    = "Sentry sentry_version=7, sentry_client=sentry.dart.flutter/9.0.0, sentry_key=" + roleAnahtar
	ornekZarf   = `{"event_id":"9ec79c33ec9942ab8353589fcb2e04dc","sent_at":"2026-11-16T10:00:00Z"}` + "\n" +
		`{"type":"event","length":2}` + "\n{}\n"
)

// sahteSentry, gelen istekleri kaydeden bir Sentry taklididir.
type sahteSentry struct {
	srv    *httptest.Server
	mu     sync.Mutex
	istek  []kayitliIstek
	status int
	header http.Header
	govde  string
}

type kayitliIstek struct {
	method, path, query string
	header              http.Header
	govde               []byte
}

func yeniSahteSentry(t *testing.T) *sahteSentry {
	t.Helper()
	s := &sahteSentry{status: http.StatusOK, govde: `{"id":"9ec79c33ec9942ab8353589fcb2e04dc"}`}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.istek = append(s.istek, kayitliIstek{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), b})
		status, govde, header := s.status, s.govde, s.header
		s.mu.Unlock()
		for k, v := range header {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, govde)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *sahteSentry) istekler() []kayitliIstek {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]kayitliIstek(nil), s.istek...)
}

// roleApp, gerçek rota tablosunu rölesi açık bir yapılandırmayla kurar ve
// röleyi sahte Sentry'ye yönlendirir. upstream boşsa röle verilen adrese gider.
func roleApp(t *testing.T, upstream string) *fiber.App {
	t.Helper()
	dsn, err := config.ParseSentryDSN("https://" + roleAnahtar + "@o4512437762981888.ingest.us.sentry.io/" + roleProje)
	if err != nil {
		t.Fatalf("DSN ayrıştırılamadı: %v", err)
	}
	tokens := testTokens()
	h := New(nil, &config.Config{AppEnv: config.EnvProduction, SentryRelay: &dsn}, tokens, nil)
	if h.SentryRelay == nil {
		t.Fatal("SentryRelay kurulmadı")
	}
	h.SentryRelay.UpstreamURL = upstream + "/api/" + roleProje + "/envelope/"
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	RegisterRoutes(app.Group("/api/v1"), h, tokens, sinirsiz)
	return app
}

func roleIstegi(path, govde string, basliklar map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(govde))
	req.Header.Set("Content-Type", "application/x-sentry-envelope")
	req.Header.Set("X-Sentry-Auth", roleAuth)
	for k, v := range basliklar {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	return req
}

func roleGonder(t *testing.T, app *fiber.App, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("istek başarısız: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestSentryRelay_ZarfDegistirilmedenAktarilir(t *testing.T) {
	s := yeniSahteSentry(t)
	app := roleApp(t, s.srv.URL)

	resp, govde := roleGonder(t, app, roleIstegi(roleYol, ornekZarf, map[string]string{
		"User-Agent":      "sentry.dart.flutter/9.0.0",
		"X-Forwarded-For": "203.0.113.7",
		"X-Real-Ip":       "203.0.113.7",
		"Cookie":          "oturum=gizli",
		"Authorization":   "Bearer gizli",
	}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, beklenen 200 (%s)", resp.StatusCode, govde)
	}
	if govde != `{"id":"9ec79c33ec9942ab8353589fcb2e04dc"}` {
		t.Errorf("yanıt gövdesi = %q, Sentry'ninki aktarılmalı", govde)
	}

	ist := s.istekler()
	if len(ist) != 1 {
		t.Fatalf("Sentry'ye %d istek gitti, beklenen 1", len(ist))
	}
	g := ist[0]
	if g.method != http.MethodPost || g.path != "/api/"+roleProje+"/envelope/" {
		t.Errorf("Sentry isteği = %s %s", g.method, g.path)
	}
	if string(g.govde) != ornekZarf {
		t.Errorf("gövde değişti: %q", g.govde)
	}
	if ct := g.header.Get("Content-Type"); ct != "application/x-sentry-envelope" {
		t.Errorf("Content-Type = %q", ct)
	}
	if a := g.header.Get("X-Sentry-Auth"); a != roleAuth {
		t.Errorf("X-Sentry-Auth = %q", a)
	}
	if ua := g.header.Get("User-Agent"); ua != "sentry.dart.flutter/9.0.0" {
		t.Errorf("User-Agent = %q", ua)
	}
	for _, k := range []string{"X-Forwarded-For", "X-Real-Ip", "Forwarded", "Cookie", "Authorization"} {
		if v := g.header.Get(k); v != "" {
			t.Errorf("%s Sentry'ye aktarılmamalı, gelen %q", k, v)
		}
	}
}

// SDK gövdeyi gzip'leyebilir: gövde açılmadan, Content-Encoding ile aktarılır.
func TestSentryRelay_GzipGovdeAcilmadanAktarilir(t *testing.T) {
	s := yeniSahteSentry(t)
	app := roleApp(t, s.srv.URL)

	var buf bytes.Buffer
	gzipYaz(t, &buf, ornekZarf)
	resp, govde := roleGonder(t, app, roleIstegi(roleYol, buf.String(), map[string]string{"Content-Encoding": "gzip"}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s)", resp.StatusCode, govde)
	}
	ist := s.istekler()
	if len(ist) != 1 {
		t.Fatalf("Sentry'ye %d istek gitti, beklenen 1", len(ist))
	}
	if ce := ist[0].header.Get("Content-Encoding"); ce != "gzip" {
		t.Errorf("Content-Encoding = %q, beklenen gzip", ce)
	}
	if !bytes.Equal(ist[0].govde, buf.Bytes()) {
		t.Errorf("gzip'li gövde bayt bayt aynı aktarılmadı")
	}
}

func TestSentryRelay_SondakiEgikCizgiSartDegil(t *testing.T) {
	s := yeniSahteSentry(t)
	app := roleApp(t, s.srv.URL)
	for _, yol := range []string{roleYol, strings.TrimSuffix(roleYol, "/")} {
		if resp, govde := roleGonder(t, app, roleIstegi(yol, ornekZarf, nil)); resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d (%s)", yol, resp.StatusCode, govde)
		}
	}
	if n := len(s.istekler()); n != 2 {
		t.Errorf("Sentry'ye %d istek gitti, beklenen 2", n)
	}
}

// Anahtarı sorguda gönderen SDK'lar: sorgudaki anahtar kabul edilir ve
// Sentry'ye de sorguyla gider.
func TestSentryRelay_SorgudakiAnahtarKabulEdilir(t *testing.T) {
	s := yeniSahteSentry(t)
	app := roleApp(t, s.srv.URL)
	resp, govde := roleGonder(t, app, roleIstegi(roleYol+"?sentry_key="+roleAnahtar+"&sentry_version=7&baska=x", ornekZarf,
		map[string]string{"X-Sentry-Auth": ""}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s)", resp.StatusCode, govde)
	}
	ist := s.istekler()
	if len(ist) != 1 {
		t.Fatalf("Sentry'ye %d istek gitti, beklenen 1", len(ist))
	}
	if ist[0].query != "sentry_key="+roleAnahtar+"&sentry_version=7" {
		t.Errorf("Sentry sorgusu = %q", ist[0].query)
	}
}

func TestSentryRelay_YabanciProjeVeAnahtar403(t *testing.T) {
	s := yeniSahteSentry(t)
	app := roleApp(t, s.srv.URL)
	for ad, req := range map[string]*http.Request{
		"baska proje":           roleIstegi("/api/v1/sentry/api/999/envelope/", ornekZarf, nil),
		"proje sayi degil":      roleIstegi("/api/v1/sentry/api/abc/envelope/", ornekZarf, nil),
		"baska anahtar":         roleIstegi(roleYol, ornekZarf, map[string]string{"X-Sentry-Auth": "Sentry sentry_version=7, sentry_key=baskasi"}),
		"auth yok":              roleIstegi(roleYol, ornekZarf, map[string]string{"X-Sentry-Auth": ""}),
		"anahtarsiz auth":       roleIstegi(roleYol, ornekZarf, map[string]string{"X-Sentry-Auth": "Sentry sentry_version=7"}),
		"sorguda baska anahtar": roleIstegi(roleYol+"?sentry_key=baskasi", ornekZarf, map[string]string{"X-Sentry-Auth": ""}),
		// Başlıktaki yanlış anahtar sorgudaki doğru anahtarla aşılamaz.
		"baslik yanlis sorgu dogru": roleIstegi(roleYol+"?sentry_key="+roleAnahtar, ornekZarf,
			map[string]string{"X-Sentry-Auth": "Sentry sentry_key=baskasi"}),
	} {
		if resp, govde := roleGonder(t, app, req); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status = %d, beklenen 403 (%s)", ad, resp.StatusCode, govde)
		}
	}
	if n := len(s.istekler()); n != 0 {
		t.Errorf("reddedilen istekler Sentry'ye gitmemeli; %d istek gitti", n)
	}
}

func TestSentryRelay_BuyukGovde413(t *testing.T) {
	s := yeniSahteSentry(t)
	app := roleApp(t, s.srv.URL)

	resp, govde := roleGonder(t, app, roleIstegi(roleYol, strings.Repeat("a", SentryRelayMaxBody+1), nil))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, beklenen 413 (%s)", resp.StatusCode, govde)
	}
	if n := len(s.istekler()); n != 0 {
		t.Errorf("büyük gövde Sentry'ye gitmemeli; %d istek gitti", n)
	}

	// Tam sınırdaki gövde aktarılır.
	if resp, govde := roleGonder(t, app, roleIstegi(roleYol, strings.Repeat("a", SentryRelayMaxBody), nil)); resp.StatusCode != http.StatusOK {
		t.Errorf("sınırdaki gövde: status = %d (%s)", resp.StatusCode, govde)
	}
}

func TestSentryRelay_KapaliysaBulunamadi(t *testing.T) {
	tokens := testTokens()
	h := New(nil, &config.Config{AppEnv: config.EnvProduction}, tokens, nil)
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	RegisterRoutes(app.Group("/api/v1"), h, tokens, sinirsiz)

	if resp, govde := roleGonder(t, app, roleIstegi(roleYol, ornekZarf, nil)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, beklenen 404 (%s)", resp.StatusCode, govde)
	}
}

// Sentry'nin hata ve hız sınırı yanıtları, SDK'nın beklemesi için gereken
// başlıklarla birlikte olduğu gibi döner.
func TestSentryRelay_SentryDurumuAktarilir(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable, http.StatusBadRequest} {
		s := yeniSahteSentry(t)
		s.status = status
		s.govde = `{"detail":"x"}`
		s.header = http.Header{
			"Retry-After":          {"60"},
			"X-Sentry-Rate-Limits": {"60:error:organization"},
		}
		app := roleApp(t, s.srv.URL)

		resp, govde := roleGonder(t, app, roleIstegi(roleYol, ornekZarf, nil))
		if resp.StatusCode != status {
			t.Errorf("Sentry %d: status = %d (%s)", status, resp.StatusCode, govde)
		}
		if govde != `{"detail":"x"}` {
			t.Errorf("Sentry %d: gövde = %q", status, govde)
		}
		if v := resp.Header.Get("Retry-After"); v != "60" {
			t.Errorf("Sentry %d: Retry-After = %q", status, v)
		}
		if v := resp.Header.Get("X-Sentry-Rate-Limits"); v != "60:error:organization" {
			t.Errorf("Sentry %d: X-Sentry-Rate-Limits = %q", status, v)
		}
	}
}

func TestSentryRelay_SentryyeUlasilamazsa502(t *testing.T) {
	s := yeniSahteSentry(t)
	adres := s.srv.URL
	s.srv.Close()
	app := roleApp(t, adres)

	if resp, govde := roleGonder(t, app, roleIstegi(roleYol, ornekZarf, nil)); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, beklenen 502 (%s)", resp.StatusCode, govde)
	}
}

func TestSentryKeyFromAuth(t *testing.T) {
	for in, want := range map[string]string{
		"Sentry sentry_version=7, sentry_client=a/1, sentry_key=abc": "abc",
		"sentry sentry_key=abc,sentry_version=7":                     "abc",
		"Sentry sentry_key = abc ":                                   "abc",
		"sentry_key=abc":                                             "abc",
		"Sentry sentry_version=7":                                    "",
		"":                                                           "",
		"Sentry sentry_keyx=abc":                                     "",
	} {
		if got := sentryKeyFromAuth(in); got != want {
			t.Errorf("sentryKeyFromAuth(%q) = %q, beklenen %q", in, got, want)
		}
	}
}

func gzipYaz(t *testing.T, w io.Writer, s string) {
	t.Helper()
	zw := gzip.NewWriter(w)
	if _, err := io.WriteString(zw, s); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}
