package handlers

import (
	"testing"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
)

// Yalnızca geliştirme/test ortamında kayıtlı olması gereken uçlar.
var gelistirmeUclari = []string{
	"POST /api/v1/users/me/usage/reset",
}

// Her ortamda kayıtlı olması gereken uçlar. E-posta/parola ve misafir uçları
// production'da da açıktır.
var ortakUclar = []string{
	"POST /api/v1/auth/google",
	"POST /api/v1/auth/register",
	"POST /api/v1/auth/login",
	"POST /api/v1/auth/email/lookup",
	"POST /api/v1/auth/guest",
	"POST /api/v1/auth/link/password",
	"POST /api/v1/dictionary/words/batch",
	"POST /api/v1/progress/sync",
	"GET /api/v1/sync/snapshot",
	"GET /api/v1/health",
	"GET /api/v1/app/version-check",
	"POST /api/v1/sentry/api/:projectId/envelope",
}

// kayitliRotalar, rota tablosunu verilen ortamla kurar ve "METOT yol"
// kümesini döner. Veritabanına dokunmaz.
func kayitliRotalar(appEnv string) map[string]bool {
	tokens := testTokens()
	h := New(nil, &config.Config{AppEnv: appEnv}, tokens, nil)
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	RegisterRoutes(app.Group("/api/v1"), h, tokens, sinirsiz)

	out := map[string]bool{}
	for _, r := range app.GetRoutes(true) {
		out[r.Method+" "+r.Path] = true
	}
	return out
}

func TestRegisterRoutes_GelistirmeUclariYalnizcaProductionDisinda(t *testing.T) {
	for _, c := range []struct {
		env     string
		kayitli bool
	}{
		{config.EnvProduction, false},
		{config.EnvDevelopment, true},
	} {
		rotalar := kayitliRotalar(c.env)
		for _, uc := range gelistirmeUclari {
			if rotalar[uc] != c.kayitli {
				t.Errorf("APP_ENV=%s: %s kayıtlı = %v, beklenen %v", c.env, uc, rotalar[uc], c.kayitli)
			}
		}
		// Ortamdan bağımsız uçlar her iki ortamda da vardır.
		for _, uc := range ortakUclar {
			if !rotalar[uc] {
				t.Errorf("APP_ENV=%s: %s kayıtlı değil", c.env, uc)
			}
		}
	}
}
