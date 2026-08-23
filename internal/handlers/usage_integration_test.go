package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Bu dosya gerçek bir PostgreSQL gerektirir; no-fill sınırı koşullu bir
// UPDATE ... WHERE ile uygulandığı için yalnızca gerçek veritabanında
// doğrulanabilir.
//
// Çalıştırmak için:
//
//	cd backend && docker compose up -d db
//	TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5434/kelimeler?sslmode=disable" \
//	  go test ./internal/handlers/ -v
//
// TEST_DATABASE_URL tanımlı değilse testler atlanır (CI'da sessizce geçmesi için).
func testDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil, entegrasyon testi atlanıyor")
	}

	gdb, err := db.Connect(dsn)
	if err != nil {
		t.Fatalf("veritabanına bağlanılamadı: %v", err)
	}
	if err := db.Migrate(gdb); err != nil {
		t.Fatalf("migrate başarısız: %v", err)
	}
	return gdb
}

// testUser, benzersiz e-postalı bir kullanıcı oluşturur ve test bitince siler.
func testUser(t *testing.T, gdb *gorm.DB) *models.User {
	t.Helper()

	u := &models.User{
		Email:       "test-" + time.Now().Format("20060102150405.000000000") + "@example.test",
		DisplayName: "Test",
	}
	if err := gdb.Create(u).Error; err != nil {
		t.Fatalf("kullanıcı oluşturulamadı: %v", err)
	}
	t.Cleanup(func() {
		gdb.Unscoped().Delete(&models.User{}, "id = ?", u.ID)
	})
	return u
}

// testApp, auth middleware'ini atlayıp userID'yi doğrudan enjekte eden bir
// Fiber uygulaması kurar. Amaç token akışını değil, sayaç mantığını sınamak.
func testApp(h *Handlers, userID string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	inject := func(c *fiber.Ctx) error {
		c.Locals("userID", userID)
		return c.Next()
	}
	app.Post("/no-fill-pass", inject, h.GrantNoFillPass)
	app.Get("/usage", inject, h.GetDailyUsage)
	return app
}

func post(t *testing.T, app *fiber.App, path string) (int, map[string]any) {
	t.Helper()

	res, err := app.Test(httptest.NewRequest(http.MethodPost, path, nil), -1)
	if err != nil {
		t.Fatalf("istek başarısız: %v", err)
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	return res.StatusCode, decoded
}

func TestGrantNoFillPass_GunlukSiniriAsmaz(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	// Sınır kadar istek başarılı olmalı.
	for i := 1; i <= services.MaxNoFillPassesPerDay; i++ {
		status, body := post(t, app, "/no-fill-pass")
		if status != fiber.StatusOK {
			t.Fatalf("%d. geçiş: status = %d, beklenen 200 (body: %v)", i, status, body)
		}
		if got := int(body["noFillPassesToday"].(float64)); got != i {
			t.Errorf("%d. geçiş: noFillPassesToday = %d, beklenen %d", i, got, i)
		}
		wantRemaining := services.MaxNoFillPassesPerDay - i
		if got := int(body["noFillPassesRemaining"].(float64)); got != wantRemaining {
			t.Errorf("%d. geçiş: kalan = %d, beklenen %d", i, got, wantRemaining)
		}
	}

	// Sınırın üstündeki istek reddedilmeli.
	status, body := post(t, app, "/no-fill-pass")
	if status != fiber.StatusTooManyRequests {
		t.Fatalf("sınır üstü: status = %d, beklenen 429 (body: %v)", status, body)
	}
	if body["code"] != "no_fill_limit_reached" {
		t.Errorf("sınır üstü: code = %v, beklenen no_fill_limit_reached", body["code"])
	}

	// Reddedilen istek sayacı artırmamalı.
	var after models.User
	gdb.First(&after, "id = ?", u.ID)
	if after.NoFillPassesToday != services.MaxNoFillPassesPerDay {
		t.Errorf("reddedilen istek sayacı artırdı: %d, beklenen %d",
			after.NoFillPassesToday, services.MaxNoFillPassesPerDay)
	}
}

func TestGrantNoFillPass_GunDonumundeSifirlanir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	// Sınırı doldur.
	for i := 0; i < services.MaxNoFillPassesPerDay; i++ {
		if status, body := post(t, app, "/no-fill-pass"); status != fiber.StatusOK {
			t.Fatalf("hazırlık isteği başarısız: %d (%v)", status, body)
		}
	}
	if status, _ := post(t, app, "/no-fill-pass"); status != fiber.StatusTooManyRequests {
		t.Fatalf("sınır dolmadı, status = %d", status)
	}

	// Sayacı düne ait göster: gün dönümü sıfırlamayı tetiklemeli.
	dun := time.Now().AddDate(0, 0, -1).UnixMilli()
	gdb.Model(&models.User{}).Where("id = ?", u.ID).
		Update("last_reset_date", dun)

	status, body := post(t, app, "/no-fill-pass")
	if status != fiber.StatusOK {
		t.Fatalf("gün dönümü sonrası status = %d, beklenen 200 (body: %v)", status, body)
	}
	if got := int(body["noFillPassesToday"].(float64)); got != 1 {
		t.Errorf("gün dönümü sonrası sayaç = %d, beklenen 1 (sıfırlanıp artmalı)", got)
	}
}

func TestGrantNoFillPass_EszamanliIstekleriSiniriAsmaz(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	// Sınırın çok üstünde eşzamanlı istek: koşullu UPDATE yarışı engellemeli.
	const n = 10
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go func() {
			res, err := app.Test(httptest.NewRequest(http.MethodPost, "/no-fill-pass", nil), -1)
			if err != nil {
				done <- 0
				return
			}
			res.Body.Close()
			done <- res.StatusCode
		}()
	}

	basarili := 0
	for i := 0; i < n; i++ {
		if <-done == fiber.StatusOK {
			basarili++
		}
	}

	if basarili > services.MaxNoFillPassesPerDay {
		t.Errorf("%d istek başarılı oldu, sınır %d — koşullu UPDATE yarışı engellemiyor",
			basarili, services.MaxNoFillPassesPerDay)
	}

	var after models.User
	gdb.First(&after, "id = ?", u.ID)
	if after.NoFillPassesToday > services.MaxNoFillPassesPerDay {
		t.Errorf("sayaç sınırı aştı: %d > %d", after.NoFillPassesToday, services.MaxNoFillPassesPerDay)
	}
}
