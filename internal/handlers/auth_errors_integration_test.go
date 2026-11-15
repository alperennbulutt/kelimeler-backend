package handlers

import (
	"errors"
	"net/http"
	"testing"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/db/dbtest"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Veritabanı hatalarının "bulunamadı" ya da "yanlış kimlik bilgisi" diye
// yanıtlanmadığını sınar. Gerçek PostgreSQL gerektirir (bkz. usage_integration_test.go).

// kimlikTablosuBozukDB, yalnızca auth_identities okumalarının hata verdiği ayrı
// bir bağlantı döner: kullanıcı okunur ama bağlı giriş yöntemleri okunamaz.
func kimlikTablosuBozukDB(t *testing.T) *gorm.DB {
	t.Helper()

	testDB(t) // TEST_DATABASE_URL yoksa atla, şemayı hazırla
	bozuk, err := db.Connect(dbtest.DSN(t))
	if err != nil {
		t.Fatalf("ikinci bağlantı açılamadı: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := bozuk.DB(); err == nil {
			sqlDB.Close()
		}
	})
	if err := bozuk.Callback().Query().Before("gorm:query").Register("test:kimlik_bozuk", func(tx *gorm.DB) {
		if tx.Statement.Table == "auth_identities" {
			_ = tx.AddError(errors.New("test: auth_identities okunamıyor"))
		}
	}); err != nil {
		t.Fatalf("callback kaydedilemedi: %v", err)
	}
	return bozuk
}

func TestMe_KullaniciYoksa404Doner(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)
	if err := gdb.Delete(&models.User{}, "id = ?", u.ID).Error; err != nil {
		t.Fatalf("kullanıcı silinemedi: %v", err)
	}

	status, body := send(t, authApp(h), authRequest(t, h, u, http.MethodGet, "/api/v1/auth/me", nil))
	if status != fiber.StatusNotFound || body["code"] != "user_not_found" {
		t.Errorf("status = %d, body = %v; beklenen 404 user_not_found", status, body)
	}
}

func TestMe_VeritabaniHatasi404Degil500Doner(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(kopukDB(t), &config.Config{AppEnv: "test"}, testTokens(), nil)

	for _, path := range []string{"/api/v1/auth/me", "/api/v1/users/me"} {
		status, body := send(t, authApp(h), authRequest(t, h, u, http.MethodGet, path, nil))
		if status != fiber.StatusInternalServerError {
			t.Errorf("GET %s status = %d, beklenen 500 (body: %v); bağlantı hatası \"hesap yok\" sayılmamalı",
				path, status, body)
		}
	}
}

// Bağlı giriş yöntemleri okunamazsa yanıt "linkedProviders: []" ile 200
// olmamalı: istemci bunu "bağlı yöntem yok" diye gösterir.
func TestMe_BagliYontemlerOkunamazsa500Doner(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	testIdentity(t, gdb, u, "google")
	h := New(kimlikTablosuBozukDB(t), &config.Config{AppEnv: "test"}, testTokens(), nil)

	status, body := send(t, authApp(h), authRequest(t, h, u, http.MethodGet, "/api/v1/auth/me", nil))
	if status != fiber.StatusInternalServerError {
		t.Errorf("status = %d, beklenen 500 (body: %v)", status, body)
	}
}

func TestLogin_VeritabaniHatasi401Degil500Doner(t *testing.T) {
	app := routesApp(kopukDB(t), "development", sinirsiz)

	status, body := postJSON(t, app, "/api/v1/auth/login", fiber.Map{
		"email": benzersizEposta("kopuk"), "password": "parola1234",
	})
	if status != fiber.StatusInternalServerError {
		t.Errorf("status = %d, beklenen 500 (body: %v); bağlantı hatası \"parola yanlış\" sayılmamalı", status, body)
	}
}

func TestLogin_ParolaKimligiOkunamazsa500Doner(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := routesApp(kimlikTablosuBozukDB(t), "development", sinirsiz)

	status, body := postJSON(t, app, "/api/v1/auth/login", fiber.Map{"email": u.Email, "password": "parola1234"})
	if status != fiber.StatusInternalServerError {
		t.Errorf("status = %d, beklenen 500 (body: %v)", status, body)
	}
}

func TestLogin_BilinmeyenHesapVeParolasizHesap401Doner(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb) // parola kimliği yok
	app := routesApp(gdb, "development", sinirsiz)

	for ad, email := range map[string]string{"bilinmeyen e-posta": benzersizEposta("yok"), "parolasız hesap": u.Email} {
		status, body := postJSON(t, app, "/api/v1/auth/login", fiber.Map{"email": email, "password": "parola1234"})
		if status != fiber.StatusUnauthorized || body["code"] != "invalid_credentials" {
			t.Errorf("%s: status = %d, body = %v; beklenen 401 invalid_credentials", ad, status, body)
		}
	}
}
