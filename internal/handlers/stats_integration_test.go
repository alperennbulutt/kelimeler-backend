package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

// Bu dosya istatistik uçlarını (/stats/*) gerçek PostgreSQL'e karşı sınar.
// Çalıştırma talimatı ve TEST_DATABASE_URL için usage_integration_test.go'ya bakın.

// statsApp, istatistik uçlarını auth'suz (userID enjekte edilerek) kurar.
func statsApp(h *Handlers, userID string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	inject := func(c *fiber.Ctx) error {
		c.Locals("userID", userID)
		return c.Next()
	}
	app.Post("/stats/daily-login", inject, h.LogDailyLogin)
	app.Get("/stats/summary", inject, h.StatsSummary)
	app.Get("/stats/learned-words", inject, h.LearnedWords)
	app.Get("/stats/login-dates", inject, h.LoginDates)
	return app
}

func get(t *testing.T, app *fiber.App, path string) (int, map[string]any) {
	t.Helper()
	return send(t, app, httptest.NewRequest(http.MethodGet, path, nil))
}

func TestStats_VeritabaniHatasi500Doner(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)

	// Ayrı bir havuz açıp kapatıyoruz: kapanınca her sorgu hata döner. Uçlar
	// bunu boş/sıfır veriyle 200 olarak gizlememeli (istemci sıfır seriyi
	// gerçek sanar). testUser'ın temizliği asıl havuzla çalışmaya devam eder.
	bozuk, err := db.Connect(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("veritabanına bağlanılamadı: %v", err)
	}
	sqlDB, err := bozuk.DB()
	if err != nil {
		t.Fatalf("sql.DB alınamadı: %v", err)
	}
	sqlDB.Close()

	h := New(bozuk, &config.Config{AppEnv: "test"}, nil, nil)
	app := statsApp(h, u.ID)

	cases := []struct {
		method, path string
	}{
		{http.MethodPost, "/stats/daily-login"},
		{http.MethodGet, "/stats/summary"},
		{http.MethodGet, "/stats/learned-words"},
		{http.MethodGet, "/stats/login-dates"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			status, body := send(t, app, httptest.NewRequest(c.method, c.path, nil))
			if status != fiber.StatusInternalServerError {
				t.Fatalf("status = %d, beklenen 500 (body: %v)", status, body)
			}
			if body["code"] != "internal" {
				t.Errorf("code = %v, beklenen \"internal\"", body["code"])
			}
		})
	}
}
