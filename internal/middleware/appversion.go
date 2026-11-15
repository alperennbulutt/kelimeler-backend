package middleware

import (
	"strings"

	"kelimeler-backend/internal/appversion"
	"kelimeler-backend/internal/observability"

	"github.com/gofiber/fiber/v2"
)

// UpgradeRequiredCode ve UpgradeRequiredMessage, desteklenmeyen sürümle gelen
// isteğe dönen 426 yanıtının kodu ve mesajıdır. İstemci bu koda bakarak tam
// ekran "Güncelleme gerekli" sayfasını açar.
const (
	UpgradeRequiredCode    = "upgrade_required"
	UpgradeRequiredMessage = "Bu sürüm artık desteklenmiyor. Lütfen uygulamayı güncelleyin."
)

// RequireAppVersion, en düşük sürümün altındaki istemcinin isteğini
// 426 Upgrade Required ile reddeder:
//
//	{"code":"upgrade_required","error":"...","storeUrl":"..."}
//
// Açılıştaki sürüm kontrolünü (GET /app/version-check) atlayan ya da uygulama
// açıkken sınırı yükseltilen istemci için güvenlik ağıdır. Kimlik doğrulamadan
// önce çalışmalıdır ki giriş uçlarında da yakalasın.
//
// Yalnızca X-App-Platform ve X-App-Version ikisi birden geldiğinde denetler:
// başlıkları göndermeyen (yayındaki eski) istemci engellenmez. Bilinmeyen
// platform ve ayrıştırılamayan sürüm de engellenmez (bkz. appversion.Policy).
// skip, denetlenmeyen tam yollardır (sağlık kontrolü ve sürüm kontrolünün
// kendisi); Fiber'ın varsayılan yönlendirmesi gibi sondaki "/" ve büyük/küçük
// harf fark etmez.
func RequireAppVersion(policy appversion.Policy, skip ...string) fiber.Handler {
	skipped := make(map[string]bool, len(skip))
	for _, p := range skip {
		skipped[normalizePath(p)] = true
	}
	return func(c *fiber.Ctx) error {
		if skipped[normalizePath(c.Path())] {
			return c.Next()
		}
		platform := c.Get(observability.AppPlatformHeader)
		version := c.Get(observability.AppVersionHeader)
		if platform == "" || version == "" {
			return c.Next()
		}
		blocked, storeURL := policy.Blocked(platform, version)
		if !blocked {
			return c.Next()
		}
		// Gövdede storeUrl da olduğu için ErrorHandler'ın {code, error}
		// biçimine bırakılmaz, yanıt burada yazılır.
		c.Set(fiber.HeaderCacheControl, "no-store")
		return c.Status(fiber.StatusUpgradeRequired).JSON(fiber.Map{
			"code":     UpgradeRequiredCode,
			"error":    UpgradeRequiredMessage,
			"storeUrl": storeURL,
		})
	}
}

func normalizePath(p string) string {
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return strings.ToLower(p)
}
