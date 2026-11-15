package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"kelimeler-backend/internal/appversion"
	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/observability"

	"github.com/gofiber/fiber/v2"
)

// Sürüm kontrolü veritabanına dokunmaz; bu testler her yerde çalışır.

const (
	iosMagaza     = "https://apps.apple.com/app/id123"
	androidMagaza = config.DefaultAppStoreURLAndroid
)

func surum(t *testing.T, s string) appversion.Version {
	t.Helper()
	v, ok := appversion.Parse(s)
	if !ok {
		t.Fatalf("Parse(%q) başarısız", s)
	}
	return v
}

// surumKontrolApp, gerçek rota tablosunu verilen sürüm politikasıyla kurar.
func surumKontrolApp(t *testing.T, p appversion.Policy) *fiber.App {
	t.Helper()
	tokens := testTokens()
	h := New(nil, &config.Config{AppEnv: config.EnvProduction, AppVersions: p}, tokens, nil)
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	RegisterRoutes(app.Group("/api/v1"), h, tokens, sinirsiz)
	return app
}

func ornekPolitika(t *testing.T) appversion.Policy {
	return appversion.Policy{
		IOS: appversion.Rule{
			Min:      surum(t, "1.0.0"),
			Latest:   surum(t, "1.1.0"),
			StoreURL: iosMagaza,
		},
		Android: appversion.Rule{
			Min:      surum(t, "1.0.0+3"),
			Latest:   surum(t, "1.1.0"),
			StoreURL: androidMagaza,
		},
	}
}

type surumYaniti struct {
	UpdateRequired  bool   `json:"updateRequired"`
	UpdateAvailable bool   `json:"updateAvailable"`
	MinVersion      string `json:"minVersion"`
	LatestVersion   string `json:"latestVersion"`
	StoreURL        string `json:"storeUrl"`
}

func surumKontrol(t *testing.T, app *fiber.App, query string, basliklar map[string]string) surumYaniti {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/app/version-check"+query, nil)
	for k, v := range basliklar {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("istek başarısız: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, beklenen 200 (gövde %s)", resp.StatusCode, raw)
	}
	if cc := resp.Header.Get(fiber.HeaderCacheControl); cc != "no-store" {
		t.Errorf("Cache-Control = %q, beklenen no-store", cc)
	}
	// Alanların tamamı her yanıtta bulunur (boş olsalar da).
	var alanlar map[string]any
	if err := json.Unmarshal(raw, &alanlar); err != nil {
		t.Fatalf("gövde JSON değil: %s", raw)
	}
	for _, k := range []string{"updateRequired", "updateAvailable", "minVersion", "latestVersion", "storeUrl"} {
		if _, ok := alanlar[k]; !ok {
			t.Errorf("gövdede %q yok: %s", k, raw)
		}
	}
	if len(alanlar) != 5 {
		t.Errorf("gövdede beklenmeyen alan: %s", raw)
	}
	var y surumYaniti
	_ = json.Unmarshal(raw, &y)
	return y
}

func TestVersionCheck(t *testing.T) {
	app := surumKontrolApp(t, ornekPolitika(t))
	ios := surumYaniti{MinVersion: "1.0.0", LatestVersion: "1.1.0", StoreURL: iosMagaza}
	android := surumYaniti{MinVersion: "1.0.0+3", LatestVersion: "1.1.0", StoreURL: androidMagaza}
	bayrak := func(y surumYaniti, required, available bool) surumYaniti {
		y.UpdateRequired, y.UpdateAvailable = required, available
		return y
	}

	cases := []struct {
		ad        string
		query     string
		basliklar map[string]string
		want      surumYaniti
	}{
		{"zorunlu", "?platform=ios&version=0.9.0%2B5", nil, bayrak(ios, true, true)},
		{"android build ile zorunlu", "?platform=android&version=1.0.0%2B2", nil, bayrak(android, true, true)},
		{"kodlanmamis arti bosluga cozulse de okunur", "?platform=android&version=1.0.0+2", nil, bayrak(android, true, true)},
		{"yeni surum var", "?platform=ios&version=1.0.1%2B9", nil, bayrak(ios, false, true)},
		{"guncel", "?platform=android&version=1.1.0%2B20", nil, bayrak(android, false, false)},
		{"1.2 = 1.2.0", "?platform=ios&version=1.1", nil, bayrak(ios, false, false)},

		// Fail-open: 400 değil, bayrakları false olan 200.
		{"bilinmeyen platform", "?platform=web&version=0.1.0", nil, surumYaniti{}},
		{"platform yok", "?version=0.1.0", nil, surumYaniti{}},
		{"surum yok", "?platform=ios", nil, bayrak(ios, false, false)},
		{"surum bozuk", "?platform=ios&version=abc", nil, bayrak(ios, false, false)},
		{"hicbir sey yok", "", nil, surumYaniti{}},

		// Sorguda yoksa başlıklar okunur.
		{"basliklardan", "", map[string]string{
			observability.AppPlatformHeader: "ios",
			observability.AppVersionHeader:  "0.9.0+1",
		}, bayrak(ios, true, true)},
		{"platform sorgudan surum basliktan", "?platform=android", map[string]string{
			observability.AppVersionHeader: "1.0.5+7",
		}, bayrak(android, false, true)},
		// Sorgu önceliklidir.
		{"sorgu basliklardan once gelir", "?platform=ios&version=1.1.0", map[string]string{
			observability.AppPlatformHeader: "android",
			observability.AppVersionHeader:  "0.1.0",
		}, bayrak(ios, false, false)},
	}
	for _, c := range cases {
		t.Run(c.ad, func(t *testing.T) {
			if got := surumKontrol(t, app, c.query, c.basliklar); got != c.want {
				t.Errorf("yanıt = %+v, beklenen %+v", got, c.want)
			}
		})
	}
}

// Ayar yokken (sıfır politika) her sürüm güncel sayılır ve alanlar boştur.
func TestVersionCheck_AyarYok(t *testing.T) {
	app := surumKontrolApp(t, appversion.Policy{})
	got := surumKontrol(t, app, "?platform=ios&version=0.0.1", nil)
	if got != (surumYaniti{}) {
		t.Errorf("yanıt = %+v, beklenen boş", got)
	}
}

// Uç kimlik doğrulama istemez: token'sız istek 401 değil 200 alır.
func TestVersionCheck_KimlikDogrulamaIstemez(t *testing.T) {
	app := surumKontrolApp(t, ornekPolitika(t))
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/app/version-check?platform=ios&version=1.0.0", nil), -1)
	if err != nil {
		t.Fatalf("istek başarısız: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("status = %d, beklenen 200", resp.StatusCode)
	}
}

func TestAppVersionExempt(t *testing.T) {
	got := AppVersionExempt("/api/v1")
	want := []string{"/api/v1/health", "/api/v1/app/version-check"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("AppVersionExempt = %q, beklenen %q", got, want)
	}
	// Muaf yolların gerçekten kayıtlı olduğundan emin ol (yol değişirse kaçmasın).
	rotalar := kayitliRotalar(config.EnvProduction)
	for _, p := range want {
		if !rotalar["GET "+p] {
			t.Errorf("GET %s kayıtlı değil", p)
		}
	}
}
