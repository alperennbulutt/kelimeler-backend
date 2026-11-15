package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kelimeler-backend/internal/appversion"
	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/handlers"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/observability"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// apiApp, sunucunun gerçek API zincirini (hız sınırı → sürüm denetimi →
// rotalar) iOS için en düşük sürüm 1.2.0 ile kurar. Veritabanı, bağlanmayı ilk
// sorguya bırakan ve dinleyeni olmayan bir adrestir: sağlık kontrolü 503 verir.
func apiApp(t *testing.T) *fiber.App {
	t.Helper()
	sinir, ok := appversion.Parse("1.2.0")
	if !ok {
		t.Fatal("sınır ayrıştırılamadı")
	}
	cfg := &config.Config{
		AppEnv:              config.EnvDevelopment,
		APIRateLimit:        1000,
		APIRateLimitWindow:  time.Minute,
		AuthRateLimit:       1000,
		AuthRateLimitWindow: time.Minute,
		AppVersions: appversion.Policy{
			IOS: appversion.Rule{Min: sinir, StoreURL: "https://apps.apple.com/app/id123"},
		},
	}
	gdb, err := gorm.Open(postgres.New(postgres.Config{DSN: "postgres://u:p@127.0.0.1:1/yok?sslmode=disable"}),
		&gorm.Config{DisableAutomaticPing: true, Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("gorm açılamadı: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			sqlDB.Close()
		}
	})
	tokens := auth.NewTokenService("test-secret-test-secret-test-secret", time.Hour, time.Hour)
	app := newApp(cfg, io.Discard)
	registerAPI(app, cfg, handlers.New(gdb, cfg, tokens, nil), tokens)
	return app
}

func surumluIstek(method, path, version string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	if version != "" {
		req.Header.Set(observability.AppPlatformHeader, "ios")
		req.Header.Set(observability.AppVersionHeader, version)
	}
	return req
}

// Sürüm denetimi kimlik doğrulamadan ve handler'lardan önce çalışır: eski
// istemci giriş ekranında da 426 alır, korumalı uçta 401 değil 426 görür.
func TestRegisterAPI_EskiSurumGiristeDe426Alir(t *testing.T) {
	app := apiApp(t)
	for _, r := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/auth/login"},
		{http.MethodPost, "/api/v1/auth/guest"},
		{http.MethodPost, "/api/v1/auth/refresh"},
		{http.MethodGet, "/api/v1/users/me"},
	} {
		resp, govde := istek(t, app, surumluIstek(r.method, r.path, "1.1.0+40"))
		if resp.StatusCode != fiber.StatusUpgradeRequired {
			t.Errorf("%s %s: status = %d, beklenen 426 (%s)", r.method, r.path, resp.StatusCode, govde)
			continue
		}
		var body map[string]string
		if err := json.Unmarshal([]byte(govde), &body); err != nil {
			t.Fatalf("gövde JSON değil: %s", govde)
		}
		if body["code"] != middleware.UpgradeRequiredCode || body["storeUrl"] != "https://apps.apple.com/app/id123" {
			t.Errorf("%s %s: gövde = %v", r.method, r.path, body)
		}
	}
}

func TestRegisterAPI_GuncelYaDaBasliksizIstemciEngellenmez(t *testing.T) {
	app := apiApp(t)
	for _, version := range []string{"", "1.2.0+41"} {
		// Gövdesiz giriş isteği veritabanına gitmeden 400 alır.
		resp, govde := istek(t, app, surumluIstek(http.MethodPost, "/api/v1/auth/login", version))
		if resp.StatusCode != fiber.StatusBadRequest {
			t.Errorf("sürüm %q: login status = %d, beklenen 400 (%s)", version, resp.StatusCode, govde)
		}
		resp, govde = istek(t, app, surumluIstek(http.MethodGet, "/api/v1/users/me", version))
		if resp.StatusCode != fiber.StatusUnauthorized {
			t.Errorf("sürüm %q: /users/me status = %d, beklenen 401 (%s)", version, resp.StatusCode, govde)
		}
	}
}

// Sağlık kontrolü ve sürüm kontrolü eski sürümde de 426 almaz.
func TestRegisterAPI_MuafUclar(t *testing.T) {
	app := apiApp(t)

	resp, govde := istek(t, app, surumluIstek(http.MethodGet, "/api/v1/health", "0.1.0"))
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("health: status = %d, beklenen 503 (veritabanı yok) (%s)", resp.StatusCode, govde)
	}

	resp, govde = istek(t, app, surumluIstek(http.MethodGet, "/api/v1/app/version-check", "0.1.0"))
	if resp.StatusCode != fiber.StatusOK || !strings.Contains(govde, `"updateRequired":true`) {
		t.Errorf("version-check: status = %d, gövde %s; beklenen 200 ve updateRequired:true", resp.StatusCode, govde)
	}
}

// Sentry rölesi gerçek API zincirinde de 426 almaz: eski sürümün çökme
// raporları da Sentry'ye ulaşmalı. Genel hız sınırı ise uygulanır.
func TestRegisterAPI_SentryRolesiSurumDenetimindenMuaf(t *testing.T) {
	var gelen int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gelen++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	sinir, _ := appversion.Parse("1.2.0")
	dsn, err := config.ParseSentryDSN("https://anahtar@o1.ingest.us.sentry.io/42")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		AppEnv:              config.EnvDevelopment,
		APIRateLimit:        2,
		APIRateLimitWindow:  time.Minute,
		AuthRateLimit:       1000,
		AuthRateLimitWindow: time.Minute,
		AppVersions:         appversion.Policy{IOS: appversion.Rule{Min: sinir}},
		SentryRelay:         &dsn,
	}
	tokens := auth.NewTokenService("test-secret-test-secret-test-secret", time.Hour, time.Hour)
	h := handlers.New(nil, cfg, tokens, nil)
	h.SentryRelay.UpstreamURL = upstream.URL + "/api/42/envelope/"
	app := newApp(cfg, io.Discard)
	registerAPI(app, cfg, h, tokens)

	zarf := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sentry/api/42/envelope/", strings.NewReader("{}\n"))
		req.Header.Set(observability.AppPlatformHeader, "ios")
		req.Header.Set(observability.AppVersionHeader, "1.0.0+1")
		req.Header.Set("Content-Type", "application/x-sentry-envelope")
		req.Header.Set("X-Sentry-Auth", "Sentry sentry_version=7, sentry_key=anahtar")
		return req
	}
	for i := 0; i < 2; i++ {
		if resp, govde := istek(t, app, zarf()); resp.StatusCode != http.StatusOK {
			t.Fatalf("eski sürümle röle: status = %d, beklenen 200 (%s)", resp.StatusCode, govde)
		}
	}
	if gelen != 2 {
		t.Errorf("Sentry'ye %d istek gitti, beklenen 2", gelen)
	}
	if resp, govde := istek(t, app, zarf()); resp.StatusCode != fiber.StatusTooManyRequests {
		t.Errorf("hız sınırı aşılınca status = %d, beklenen 429 (%s)", resp.StatusCode, govde)
	}
}
