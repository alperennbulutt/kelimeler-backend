package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/middleware"

	"github.com/getsentry/sentry-go"
	"github.com/gofiber/fiber/v2"
)

// Bu testler veritabanı ve ağ gerektirmez: yalnızca newApp'in kurduğu
// middleware zinciri, üzerine eklenen sahte rotalarla sınanır.

const yabanciOrigin = "https://kotu.example"

// testApp, newApp'in üzerine biri panic atan iki rota ekler ve istek satırlarının
// yazıldığı tamponu döner.
func testApp(cfg *config.Config) (*fiber.App, *bytes.Buffer) {
	accessLog := &bytes.Buffer{}
	app := newApp(cfg, accessLog)
	app.Get("/ok", func(c *fiber.Ctx) error { return c.SendString("tamam") })
	app.Post("/ok", func(c *fiber.Ctx) error { return c.SendString("tamam") })
	app.Get("/panic", func(*fiber.Ctx) error { panic("test panic'i") })
	return app, accessLog
}

func istek(t *testing.T, app *fiber.App, req *http.Request) (*http.Response, string) {
	t.Helper()

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("%s %s başarısız: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()

	govde, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s gövdesi okunamadı: %v", req.Method, req.URL.Path, err)
	}
	return resp, string(govde)
}

// --- Middleware sırası ---

func TestNewApp_PanicYanitaCevrilirVeSurecAyaktaKalir(t *testing.T) {
	app, accessLog := testApp(&config.Config{AppEnv: "test"})

	resp, govde := istek(t, app, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if resp.StatusCode != fiber.StatusInternalServerError {
		t.Fatalf("panic: status = %d, beklenen 500 (body: %s)", resp.StatusCode, govde)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(govde), &decoded); err != nil || decoded["code"] != "internal" {
		t.Errorf("panic: gövde = %s, beklenen ErrorHandler'ın {\"code\":\"internal\"} yanıtı", govde)
	}

	// Logger recover'ın dışında olmalı: panic'li isteğin satırı da yazılır.
	satir := accessLog.String()
	if !strings.Contains(satir, "500") || !strings.Contains(satir, "/panic") {
		t.Errorf("panic'li istek loglanmadı; istek logu: %q", satir)
	}
	if !strings.Contains(satir, "test panic'i") {
		t.Errorf("istek satırında panic mesajı yok; istek logu: %q", satir)
	}

	// Panic süreci düşürmemiş olmalı: aynı uygulama sonraki isteği yanıtlar.
	resp, govde = istek(t, app, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if resp.StatusCode != fiber.StatusOK || govde != "tamam" {
		t.Errorf("panic sonrası istek: status = %d, gövde = %q; beklenen 200 \"tamam\"", resp.StatusCode, govde)
	}
}

// kayitTransport, Sentry'ye gönderilecek olayları ağa çıkmadan biriktirir.
type kayitTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (k *kayitTransport) Configure(sentry.ClientOptions)        {}
func (k *kayitTransport) Flush(time.Duration) bool              { return true }
func (k *kayitTransport) FlushWithContext(context.Context) bool { return true }
func (k *kayitTransport) Close()                                {}
func (k *kayitTransport) SendEvent(event *sentry.Event) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.events = append(k.events, event)
}

func (k *kayitTransport) olaylar() []*sentry.Event {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]*sentry.Event(nil), k.events...)
}

func TestNewApp_PanicSentryyeUlasirVeYineYanitaCevrilir(t *testing.T) {
	const dsn = "https://anahtar@sentry.invalid/1"

	transport := &kayitTransport{}
	if err := sentry.Init(sentry.ClientOptions{Dsn: dsn, Transport: transport}); err != nil {
		t.Fatalf("sentry başlatılamadı: %v", err)
	}
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })

	app, accessLog := testApp(&config.Config{AppEnv: "test", SentryDSN: dsn})

	resp, govde := istek(t, app, httptest.NewRequest(http.MethodGet, "/panic", nil))

	// Sentry recover'ın içinde olmalı: panic'i ilk o görür.
	olaylar := transport.olaylar()
	if len(olaylar) != 1 {
		t.Fatalf("Sentry'ye %d olay gitti, beklenen 1 (panic recover'a Sentry'den önce ulaşmamalı)", len(olaylar))
	}
	if !strings.Contains(olaylar[0].Message, "test panic'i") {
		t.Errorf("Sentry olayı mesajı = %q, beklenen panic mesajı", olaylar[0].Message)
	}

	// Sentry panic'i yeniden fırlatır; yanıtı yine recover + ErrorHandler üretir.
	if resp.StatusCode != fiber.StatusInternalServerError {
		t.Errorf("status = %d, beklenen 500 (body: %s)", resp.StatusCode, govde)
	}
	if satir := accessLog.String(); !strings.Contains(satir, "500") || !strings.Contains(satir, "/panic") {
		t.Errorf("panic'li istek loglanmadı; istek logu: %q", satir)
	}

	resp, _ = istek(t, app, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("panic sonrası istek: status = %d, beklenen 200", resp.StatusCode)
	}
	if got := len(transport.olaylar()); got != 1 {
		t.Errorf("başarılı istek sonrası Sentry olay sayısı = %d, beklenen 1", got)
	}
}

// Hata yanıta en dıştaki logger'da çevrilir; Sentry ondan önce kapanır. Durum
// kodu erkenden yazılmazsa hatalı istekler Sentry'de 200/ok görünür ve 404
// filtresi çalışmaz.
func TestNewApp_SentryTransactionGercekDurumKodunuTasir(t *testing.T) {
	const dsn = "https://anahtar@sentry.invalid/1"

	transport := &kayitTransport{}
	err := sentry.Init(sentry.ClientOptions{
		Dsn: dsn, Transport: transport, EnableTracing: true, TracesSampleRate: 1,
	})
	if err != nil {
		t.Fatalf("sentry başlatılamadı: %v", err)
	}
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })

	app, _ := testApp(&config.Config{AppEnv: "test", SentryDSN: dsn})
	app.Get("/e401", func(*fiber.Ctx) error {
		return middleware.NewAPIError(fiber.StatusUnauthorized, "unauthenticated", "yetkisiz")
	})
	app.Get("/e500", func(*fiber.Ctx) error { return errors.New("veritabanına ulaşılamadı") })

	// transactionKodu, yol için gönderilen transaction'ın durum kodunu döner;
	// transaction gönderilmediyse ok=false.
	transactionKodu := func(yol string, beklenenHTTP int) (kod any, ok bool) {
		t.Helper()
		once := len(transport.olaylar())
		resp, govde := istek(t, app, httptest.NewRequest(http.MethodGet, yol, nil))
		if resp.StatusCode != beklenenHTTP {
			t.Fatalf("%s: status = %d, beklenen %d (body: %s)", yol, resp.StatusCode, beklenenHTTP, govde)
		}
		for _, olay := range transport.olaylar()[once:] {
			if olay.Type != "transaction" {
				continue
			}
			veri, _ := olay.Contexts["trace"]["data"].(map[string]any)
			return veri["http.response.status_code"], true
		}
		return nil, false
	}

	durumlar := []struct {
		yol  string
		http int
	}{
		{"/ok", fiber.StatusOK},
		{"/e401", fiber.StatusUnauthorized},
		{"/e500", fiber.StatusInternalServerError},
		{"/panic", fiber.StatusInternalServerError},
	}
	for _, d := range durumlar {
		kod, ok := transactionKodu(d.yol, d.http)
		if !ok {
			t.Errorf("%s: transaction gönderilmedi", d.yol)
			continue
		}
		if fmt.Sprint(kod) != fmt.Sprint(d.http) {
			t.Errorf("%s: transaction durum kodu = %v, beklenen %d", d.yol, kod, d.http)
		}
	}

	// sentry-go 404 transaction'larını varsayılan olarak atar; kod 200 görünseydi
	// olmayan yollara gelen tarama istekleri de gönderilirdi.
	if kod, ok := transactionKodu("/yok", fiber.StatusNotFound); ok {
		t.Errorf("/yok: 404 transaction'ı gönderildi (durum kodu %v)", kod)
	}
}

// --- CORS ---

func corsIstegi(method, origin string) *http.Request {
	req := httptest.NewRequest(method, "/ok", nil)
	req.Header.Set(fiber.HeaderOrigin, origin)
	if method == http.MethodOptions {
		req.Header.Set(fiber.HeaderAccessControlRequestMethod, http.MethodPost)
		req.Header.Set(fiber.HeaderAccessControlRequestHeaders, "content-type,authorization")
	}
	return req
}

func TestNewApp_ProductiondaOriginListesiBoskenCORSBasligiYazilmaz(t *testing.T) {
	app, _ := testApp(&config.Config{AppEnv: "production"})

	corsBasliklari := []string{
		fiber.HeaderAccessControlAllowOrigin,
		fiber.HeaderAccessControlAllowHeaders,
		fiber.HeaderAccessControlAllowMethods,
		fiber.HeaderAccessControlAllowCredentials,
	}
	for _, method := range []string{http.MethodOptions, http.MethodPost, http.MethodGet} {
		t.Run(method, func(t *testing.T) {
			resp, _ := istek(t, app, corsIstegi(method, yabanciOrigin))

			for _, baslik := range corsBasliklari {
				if got := resp.Header.Get(baslik); got != "" {
					t.Errorf("%s = %q, beklenen başlık yok (hiçbir origin'e izin verilmemeli)", baslik, got)
				}
			}
			// Preflight onaylanmamalı: tarayıcı asıl isteği hiç göndermez.
			if method == http.MethodOptions && resp.StatusCode == fiber.StatusNoContent {
				t.Errorf("preflight status = 204, beklenen onaylanmaması")
			}
		})
	}

	// Origin başlığı taşımayan (mobil) istemci etkilenmez.
	resp, govde := istek(t, app, httptest.NewRequest(http.MethodPost, "/ok", nil))
	if resp.StatusCode != fiber.StatusOK || govde != "tamam" {
		t.Errorf("Origin'siz istek: status = %d, gövde = %q; beklenen 200 \"tamam\"", resp.StatusCode, govde)
	}
}

func TestNewApp_ListedekiOriginYansitilirDigeriYansitilmaz(t *testing.T) {
	const izinli = "https://panel.example"

	// Listeli yapılandırma ortamdan bağımsızdır.
	for _, env := range []string{"production", "development"} {
		t.Run(env, func(t *testing.T) {
			app, _ := testApp(&config.Config{AppEnv: env, CORSAllowedOrigins: []string{izinli}})

			resp, _ := istek(t, app, corsIstegi(http.MethodOptions, izinli))
			if got := resp.Header.Get(fiber.HeaderAccessControlAllowOrigin); got != izinli {
				t.Errorf("izinli origin preflight: Access-Control-Allow-Origin = %q, beklenen %q", got, izinli)
			}
			resp, _ = istek(t, app, corsIstegi(http.MethodPost, izinli))
			if got := resp.Header.Get(fiber.HeaderAccessControlAllowOrigin); got != izinli {
				t.Errorf("izinli origin POST: Access-Control-Allow-Origin = %q, beklenen %q", got, izinli)
			}

			for _, method := range []string{http.MethodOptions, http.MethodPost} {
				resp, _ = istek(t, app, corsIstegi(method, yabanciOrigin))
				if got := resp.Header.Get(fiber.HeaderAccessControlAllowOrigin); got != "" {
					t.Errorf("listede olmayan origin %s: Access-Control-Allow-Origin = %q, beklenen boş", method, got)
				}
			}
		})
	}
}

func TestNewApp_GelistirmedeTumOriginlereIzinVerilir(t *testing.T) {
	app, _ := testApp(&config.Config{AppEnv: "development"})

	for _, method := range []string{http.MethodOptions, http.MethodPost} {
		resp, _ := istek(t, app, corsIstegi(method, yabanciOrigin))
		if got := resp.Header.Get(fiber.HeaderAccessControlAllowOrigin); got != "*" {
			t.Errorf("%s: Access-Control-Allow-Origin = %q, beklenen \"*\"", method, got)
		}
	}
}
