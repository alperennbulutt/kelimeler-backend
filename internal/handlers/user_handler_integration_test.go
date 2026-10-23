package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Bu dosya /users/me uçlarının yanıt kurulumunu gerçek PostgreSQL'e karşı
// sınar; kurulum için usage_integration_test.go başındaki nota bakın.

// kimlikSorgusuHataVerir, bu bağlantıdaki auth_identities okumalarını hataya
// çevirir; kullanıcı satırı okunabilirken bağlı giriş yöntemleri okunamaz.
func kimlikSorgusuHataVerir(t *testing.T, gdb *gorm.DB) {
	t.Helper()

	err := gdb.Callback().Query().Before("gorm:query").Register("itest:kimlik_hatasi", func(tx *gorm.DB) {
		if tx.Statement.Table == "auth_identities" {
			_ = tx.AddError(errors.New("itest: auth_identities okunamadı"))
		}
	})
	if err != nil {
		t.Fatalf("callback eklenemedi: %v", err)
	}
}

func TestUsersMe_BagliYontemlerOkunamazsa500(t *testing.T) {
	// Eskiden AdvanceGroup ve diğer /users/me yanıtları hatayı yutup
	// linkedProviders:[] dönüyordu; istemci bunu "hiçbir yöntem bağlı değil"
	// diye okur. Profil güncellemesinin kendi yazma hatası (500) ayrıca
	// TestUclar_VeritabaniHatasindaSahte200Donmez'de sınanır.
	gdb := testDB(t)
	u := testUser(t, gdb)
	seedGroupLevels(t, gdb)
	setPointer(t, gdb, u.ID, "A1", 1)
	kimlikSorgusuHataVerir(t, gdb)

	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("userID", u.ID)
		return c.Next()
	})
	app.Patch("/users/me", h.UpdateProfile)
	app.Put("/users/me/current-group", h.SetCurrentGroup)
	app.Post("/users/me/advance-group", h.AdvanceGroup)

	cases := []struct {
		name string
		req  func() *http.Request
	}{
		{"sonraki grup govdesiz", func() *http.Request {
			return httptest.NewRequest(http.MethodPost, "/users/me/advance-group", nil)
		}},
		{"sonraki grup govdeli", func() *http.Request {
			return advanceRequest(fiber.Map{"level": "A1", "group": 2})
		}},
		{"kaldigin grup", func() *http.Request {
			return currentGroupRequest(fiber.Map{"level": "A1", "group": 3})
		}},
		{"profil", func() *http.Request {
			req := jsonRequest("/users/me", fiber.Map{"displayName": "Ad"})
			req.Method = http.MethodPatch
			return req
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := send(t, app, c.req())
			if status != fiber.StatusInternalServerError {
				t.Fatalf("status = %d, beklenen 500 (body: %v)", status, body)
			}
			if _, ok := body["user"]; ok {
				t.Errorf("hata yanıtında kullanıcı var: %v", body)
			}
			// Bağlı yöntemler yazımdan önce okunur: okunamayınca işaretçi
			// hiç yazılmaz, yeniden deneme grubu iki kez atlatmaz.
			assertPointer(t, gdb, u.ID, "A1", 1)
		})
	}
}
