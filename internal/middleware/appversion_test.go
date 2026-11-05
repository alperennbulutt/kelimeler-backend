package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"kelimeler-backend/internal/appversion"
	"kelimeler-backend/internal/observability"

	"github.com/gofiber/fiber/v2"
)

const testStoreURL = "https://apps.apple.com/app/id123"

func surumPolitikasi(t *testing.T) appversion.Policy {
	t.Helper()
	sinir, ok := appversion.Parse("1.2.0")
	if !ok {
		t.Fatal("sınır ayrıştırılamadı")
	}
	minAndroid, _ := appversion.Parse("1.2.0+15")
	return appversion.Policy{
		IOS:     appversion.Rule{Min: sinir, StoreURL: testStoreURL},
		Android: appversion.Rule{Min: minAndroid, StoreURL: "https://play.google.com/x"},
	}
}

// surumApp, /api/v1 altında birkaç uç kurar; muaf yollar sağlık ve sürüm kontrolüdür.
func surumApp(t *testing.T) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	api := app.Group("/api/v1")
	api.Use(RequireAppVersion(surumPolitikasi(t), "/api/v1/health", "/api/v1/app/version-check"))
	ok := func(c *fiber.Ctx) error { return c.SendString("ok") }
	api.Get("/health", ok)
	api.Get("/app/version-check", ok)
	api.Get("/map/open-groups", ok)
	api.Post("/auth/login", ok)
	return app
}

type surumIstegi struct {
	method, path, platform, version string
}

func surumDurum(t *testing.T, app *fiber.App, r surumIstegi) (int, map[string]any, *http.Response) {
	t.Helper()
	if r.method == "" {
		r.method = http.MethodGet
	}
	req := httptest.NewRequest(r.method, r.path, nil)
	if r.platform != "" {
		req.Header.Set(observability.AppPlatformHeader, r.platform)
	}
	if r.version != "" {
		req.Header.Set(observability.AppVersionHeader, r.version)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("istek başarısız: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body, resp
}

func TestRequireAppVersion_EskiSurum426(t *testing.T) {
	app := surumApp(t)
	for _, r := range []surumIstegi{
		{path: "/api/v1/map/open-groups", platform: "ios", version: "1.1.9+99"},
		{path: "/api/v1/map/open-groups", platform: "IOS", version: "1.1"},
		{method: http.MethodPost, path: "/api/v1/auth/login", platform: "ios", version: "1.0.0+1"},
		{path: "/api/v1/map/open-groups", platform: "android", version: "1.2.0+14"},
	} {
		status, body, resp := surumDurum(t, app, r)
		if status != fiber.StatusUpgradeRequired {
			t.Errorf("%+v: status = %d, beklenen 426", r, status)
			continue
		}
		if body["code"] != UpgradeRequiredCode || body["error"] != UpgradeRequiredMessage {
			t.Errorf("%+v: gövde = %v", r, body)
		}
		wantURL := testStoreURL
		if r.platform == "android" {
			wantURL = "https://play.google.com/x"
		}
		if body["storeUrl"] != wantURL {
			t.Errorf("%+v: storeUrl = %v, beklenen %q", r, body["storeUrl"], wantURL)
		}
		if len(body) != 3 {
			t.Errorf("%+v: gövdede beklenmeyen alan: %v", r, body)
		}
		if cc := resp.Header.Get(fiber.HeaderCacheControl); cc != "no-store" {
			t.Errorf("%+v: Cache-Control = %q", r, cc)
		}
	}
}

func TestRequireAppVersion_EngellenmeyenIstekler(t *testing.T) {
	app := surumApp(t)
	for ad, r := range map[string]surumIstegi{
		"baslik yok":                {path: "/api/v1/map/open-groups"},
		"yalnizca surum":            {path: "/api/v1/map/open-groups", version: "0.1.0"},
		"yalnizca platform":         {path: "/api/v1/map/open-groups", platform: "ios"},
		"sinirdaki surum":           {path: "/api/v1/map/open-groups", platform: "ios", version: "1.2.0"},
		"yeni surum":                {path: "/api/v1/map/open-groups", platform: "ios", version: "2.0.0+1"},
		"android sinirdaki build":   {path: "/api/v1/map/open-groups", platform: "android", version: "1.2.0+15"},
		"android buildsiz":          {path: "/api/v1/map/open-groups", platform: "android", version: "1.2.0"},
		"ayristirilamayan surum":    {path: "/api/v1/map/open-groups", platform: "ios", version: "eski-surum"},
		"bilinmeyen platform":       {path: "/api/v1/map/open-groups", platform: "web", version: "0.1.0"},
		"saglik kontrolu muaf":      {path: "/api/v1/health", platform: "ios", version: "0.1.0"},
		"saglik sondaki egik cizgi": {path: "/api/v1/health/", platform: "ios", version: "0.1.0"},
		"surum kontrolu muaf":       {path: "/api/v1/app/version-check", platform: "ios", version: "0.1.0"},
		"giris basliksiz":           {method: http.MethodPost, path: "/api/v1/auth/login"},
	} {
		if status, body, _ := surumDurum(t, app, r); status != fiber.StatusOK {
			t.Errorf("%s: status = %d (%v), beklenen 200", ad, status, body)
		}
	}
}

// Sıfır politika (hiç ayar yok) hiçbir isteği engellemez.
func TestRequireAppVersion_AyarYoksaEngellemez(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Use(RequireAppVersion(appversion.Policy{}))
	app.Get("/x", func(c *fiber.Ctx) error { return c.SendString("ok") })
	if status, _, _ := surumDurum(t, app, surumIstegi{path: "/x", platform: "ios", version: "0.0.1"}); status != fiber.StatusOK {
		t.Errorf("status = %d, beklenen 200", status)
	}
}
