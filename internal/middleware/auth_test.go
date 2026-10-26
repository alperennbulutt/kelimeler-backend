package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kelimeler-backend/internal/auth"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
)

func TestRequireAuth(t *testing.T) {
	ts := auth.NewTokenService("test-secret", time.Minute, time.Hour)
	gecerli, err := ts.GenerateAccessToken("kullanici-1", "a@example.com", 0)
	if err != nil {
		t.Fatalf("token üretilemedi: %v", err)
	}
	// exp'siz token secret bilinirse üretilebilir; süresiz geçerli sayılmamalı.
	expsiz, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "kullanici-1"}).
		SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("token imzalanamadı: %v", err)
	}

	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Get("/me", RequireAuth(ts), func(c *fiber.Ctx) error { return c.SendString(UserID(c)) })

	cases := []struct {
		name, header string
		wantStatus   int
		wantBody     string
	}{
		{"geçerli token", "Bearer " + gecerli, fiber.StatusOK, "kullanici-1"},
		{"başlık yok", "", fiber.StatusUnauthorized, ""},
		{"Bearer öneki yok", gecerli, fiber.StatusUnauthorized, ""},
		{"bozuk token", "Bearer abc.def.ghi", fiber.StatusUnauthorized, ""},
		{"exp'siz token", "Bearer " + expsiz, fiber.StatusUnauthorized, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/me", nil)
			if c.header != "" {
				req.Header.Set(fiber.HeaderAuthorization, c.header)
			}
			res, err := app.Test(req, -1)
			if err != nil {
				t.Fatalf("istek başarısız: %v", err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != c.wantStatus {
				t.Fatalf("status = %d, beklenen %d (body: %s)", res.StatusCode, c.wantStatus, body)
			}
			if c.wantStatus == fiber.StatusOK {
				if string(body) != c.wantBody {
					t.Errorf("userID = %q, beklenen %q", body, c.wantBody)
				}
				return
			}
			var decoded map[string]string
			_ = json.Unmarshal(body, &decoded)
			if decoded["code"] != "unauthenticated" {
				t.Errorf("code = %q, beklenen unauthenticated", decoded["code"])
			}
		})
	}
}
