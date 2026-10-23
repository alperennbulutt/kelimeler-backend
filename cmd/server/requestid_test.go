package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/observability"

	"github.com/getsentry/sentry-go"
	"github.com/gofiber/fiber/v2"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewApp_IstekKimligi(t *testing.T) {
	cases := []struct {
		name    string
		gelen   string
		korunur bool
	}{
		{"yoksa uretilir", "", false},
		{"gecerli olan korunur", "traefik-abc_123.4:5", true},
		{"satir sonu tasiyan reddedilir", "abc\r\nX-Sahte: 1", false},
		{"bosluklu reddedilir", "a b", false},
		{"cok uzun reddedilir", strings.Repeat("a", 129), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app, accessLog := testApp(&config.Config{AppEnv: "development"})
			app.Get("/e500", func(*fiber.Ctx) error { return errors.New("veritabanına ulaşılamadı") })

			for _, yol := range []string{"/ok", "/e500", "/panic"} {
				accessLog.Reset()
				req := httptest.NewRequest(http.MethodGet, yol, nil)
				if c.gelen != "" {
					req.Header[observability.RequestIDHeader] = []string{c.gelen}
				}
				resp, _ := istek(t, app, req)

				id := resp.Header.Get(observability.RequestIDHeader)
				if c.korunur && id != c.gelen {
					t.Errorf("%s: X-Request-ID = %q, beklenen gelen %q", yol, id, c.gelen)
				}
				if !c.korunur && !uuidRe.MatchString(id) {
					t.Errorf("%s: X-Request-ID = %q, beklenen üretilmiş UUID", yol, id)
				}
				if !strings.Contains(accessLog.String(), "| "+id+" |") {
					t.Errorf("%s: erişim logunda istek kimliği yok: %q", yol, accessLog.String())
				}
			}
		})
	}
}

// 5xx hataları Sentry'ye istek kimliğiyle gider; panic iki kez gönderilmez,
// 4xx hiç gönderilmez.
func TestNewApp_SunucuHatalariSentryyeIstekKimligiyleGider(t *testing.T) {
	const dsn = "https://anahtar@sentry.invalid/1"

	transport := &kayitTransport{}
	if err := sentry.Init(sentry.ClientOptions{Dsn: dsn, Transport: transport}); err != nil {
		t.Fatalf("sentry başlatılamadı: %v", err)
	}
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })

	app, _ := testApp(&config.Config{AppEnv: "development", SentryDSN: dsn})
	app.Get("/e500", func(*fiber.Ctx) error { return errors.New("veritabanına ulaşılamadı") })
	app.Get("/e503", func(*fiber.Ctx) error {
		return middleware.NewAPIError(fiber.StatusServiceUnavailable, "apple_not_configured", "yapılandırılmamış")
	})
	app.Get("/e400", func(*fiber.Ctx) error {
		return middleware.NewAPIError(fiber.StatusBadRequest, "invalid", "geçersiz")
	})

	durumlar := []struct {
		yol    string
		status int
		olay   int
		mesaj  string
	}{
		{"/e500", 500, 1, "veritabanına ulaşılamadı"},
		{"/e503", 503, 1, "yapılandırılmamış"},
		{"/panic", 500, 1, "test panic'i"}, // Sentry middleware'i gönderir, ErrorHandler tekrar göndermez
		{"/e400", 400, 0, ""},
		{"/ok", 200, 0, ""},
	}
	for _, d := range durumlar {
		once := len(transport.olaylar())
		resp, govde := istek(t, app, httptest.NewRequest(http.MethodGet, d.yol, nil))
		if resp.StatusCode != d.status {
			t.Fatalf("%s: status = %d, beklenen %d (body: %s)", d.yol, resp.StatusCode, d.status, govde)
		}
		yeni := transport.olaylar()[once:]
		if len(yeni) != d.olay {
			t.Errorf("%s: Sentry'ye %d olay gitti, beklenen %d", d.yol, len(yeni), d.olay)
			continue
		}
		if d.olay == 0 {
			continue
		}
		olay := yeni[0]
		id := resp.Header.Get(observability.RequestIDHeader)
		if id == "" || olay.Tags["request_id"] != id {
			t.Errorf("%s: olay request_id etiketi = %q, yanıt başlığı %q", d.yol, olay.Tags["request_id"], id)
		}
		metin := olay.Message
		for _, ex := range olay.Exception {
			metin += " " + ex.Value
		}
		if !strings.Contains(metin, d.mesaj) {
			t.Errorf("%s: olay metni = %q, beklenen %q içermesi", d.yol, metin, d.mesaj)
		}
	}
}
