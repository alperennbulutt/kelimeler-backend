package middleware

import (
	"strings"

	"kelimeler-backend/internal/auth"

	"github.com/gofiber/fiber/v2"
)

const (
	userIDKey    = "userID"
	userEmailKey = "userEmail"
)

// RequireAuth, Authorization: Bearer <token> başlığını doğrular ve
// kullanıcı id'sini c.Locals'a koyar.
func RequireAuth(ts *auth.TokenService) fiber.Handler {
	return func(c *fiber.Ctx) error {
		header := c.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			return NewAPIError(fiber.StatusUnauthorized, "unauthenticated", "yetkilendirme gerekli")
		}
		claims, err := ts.ParseAccessToken(strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			return NewAPIError(fiber.StatusUnauthorized, "unauthenticated", "geçersiz token")
		}
		c.Locals(userIDKey, claims.Subject)
		c.Locals(userEmailKey, claims.Email)
		return c.Next()
	}
}

// UserID, RequireAuth sonrası context'teki kullanıcı id'sini döndürür.
func UserID(c *fiber.Ctx) string {
	id, _ := c.Locals(userIDKey).(string)
	return id
}
