package handlers

import (
	"kelimeler-backend/internal/observability"

	"github.com/gofiber/fiber/v2"
)

// VersionCheck, GET /app/version-check: istemcinin açılışta sorduğu zorunlu
// güncelleme kararı. Kimlik doğrulama istemez; giriş ekranından önce çağrılır.
//
// Sorgu: platform=ios|android, version=1.2.0+15. Sorguda yoksa X-App-Platform /
// X-App-Version başlıkları okunur (sorgu önceliklidir). Eksik ya da bilinmeyen
// değerde 400 değil, iki bayrağı false olan 200 döner (fail-open):
//
//	{"updateRequired":false,"updateAvailable":false,"minVersion":"",
//	 "latestVersion":"","storeUrl":""}
//
// Ayar değişince eski kararın sunulmaması için yanıt önbelleğe alınmaz.
func (h *Handlers) VersionCheck(c *fiber.Ctx) error {
	platform := c.Query("platform")
	if platform == "" {
		platform = c.Get(observability.AppPlatformHeader)
	}
	version := c.Query("version")
	if version == "" {
		version = c.Get(observability.AppVersionHeader)
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.JSON(h.Cfg.AppVersions.Check(platform, version))
}
