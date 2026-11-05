package handlers

import (
	"context"
	"log"
	"time"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// healthTimeout, sağlık kontrolünün veritabanını bekleyeceği en uzun süredir;
// probe'un kendi zaman aşımından kısa olmalı ki asılı DB "yanıt yok" değil 503
// olarak görünsün.
const healthTimeout = 2 * time.Second

// health, veritabanına gerçekten ulaşılabildiğini sınar. Ulaşılamıyorsa 503
// döner: Coolify yeni container'ı sağlıklı saymaz ve trafiği ona vermez.
// Başarılı yanıtın biçimi değişmedi: {"status":"ok","dictionaryWords":N}.
func health(gdb *gorm.DB) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), healthTimeout)
		defer cancel()

		var dict int64
		if err := gdb.WithContext(ctx).Model(&models.DictionaryWord{}).Count(&dict).Error; err != nil {
			// Probe birkaç saniyede bir gelir; Sentry'yi doldurmamak için hata
			// işleyiciye değil doğrudan yanıta çevrilir.
			log.Printf("health: veritabanına ulaşılamıyor: %v", err)
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"status": "unavailable",
				"code":   "db_unavailable",
				"error":  "veritabanına ulaşılamıyor",
			})
		}
		return c.JSON(fiber.Map{"status": "ok", "dictionaryWords": dict})
	}
}

// Zorunlu güncelleme middleware'inin (middleware.RequireAppVersion) dışında
// kalan uçlar, API grubuna göre yollarıyla.
const (
	HealthPath       = "/health"
	VersionCheckPath = "/app/version-check"
)

// AppVersionExempt, prefix altındaki (ör. "/api/v1") zorunlu güncelleme
// denetiminden muaf tam yolları döner. Sağlık kontrolü sürüm başlığı taşımaz;
// sürüm kontrolü ise 426 alsaydı istemci storeUrl'i ve sınırları göremezdi.
func AppVersionExempt(prefix string) []string {
	return []string{prefix + HealthPath, prefix + VersionCheckPath}
}

// RegisterRoutes, tüm API route'larını mount eder.
//
// authLimit, parola/sosyal giriş, e-posta sorgusu, misafir hesabı ve parola
// bağlama uçlarına uygulanan sıkı hız sınırıdır; deneme saldırılarını
// yavaşlatmak için genel sınırdan ayrıdır.
func RegisterRoutes(api fiber.Router, h *Handlers, ts *auth.TokenService, authLimit fiber.Handler) {
	api.Get(HealthPath, health(h.DB))

	// Açılıştaki zorunlu güncelleme kontrolü: kimliksizdir, girişten önce
	// çağrılır. 426 middleware'inin dışında tutulur (bkz. AppVersionExempt).
	api.Get(VersionCheckPath, h.VersionCheck)

	guard := middleware.RequireAuth(ts)

	// --- Auth (yetkilendirme gerektirmeyen) ---
	// Kimlik üreten uçlar sıkı hız sınırına tabidir.
	a := api.Group("/auth")
	a.Post("/google", authLimit, h.GoogleAuth)
	a.Post("/apple", authLimit, h.AppleAuth)

	// E-posta/parola her ortamda açıktır. Kayıtta e-posta sahipliği
	// doğrulanmaz; bu yüzden adresin doğrulanmış sahibi Google/Apple ile
	// geldiğinde yalnızca parolalı hesap ona devredilir (bkz.
	// takeOverPasswordAccount): adresi önceden kaydeden biri gerçek sahibin
	// önüne geçemez.
	a.Post("/register", authLimit, h.Register)
	a.Post("/login", authLimit, h.Login)

	// E-posta önce giriş ekranı: adresin kayıtlı olup olmadığını (ve parolası
	// mı, yalnızca Google/Apple'ı mı olduğunu) bilerek açığa vurur. Toplu
	// adres taramasını sıkı sınır yavaşlatır (bkz. EmailLookup).
	a.Post("/email/lookup", authLimit, h.EmailLookup)

	// Misafir hesabı açmak da hesap üretir: sınırsız olsaydı tek bir istemci
	// tabloyu doldurabilirdi.
	a.Post("/guest", authLimit, h.Guest)

	// Yenileme sıkı sınıra değil yalnızca genel API sınırına tabidir: uygulama
	// açılışlarının çoğu buradan geçer ve aynı IP'yi paylaşan (CGNAT) kullanıcılar
	// sıkı sınırı birlikte doldurur. Refresh token uzun ve rastgele bir değerdir;
	// deneme yanılmayla bulunamayacağı için sıkı sınırın koruyacağı bir şey yoktur.
	a.Post("/refresh", h.Refresh)
	a.Post("/logout", h.Logout)

	// --- Auth (yetkilendirme gerektiren) ---
	a.Get("/me", guard, h.Me)
	a.Post("/link/google", guard, h.LinkGoogle)
	a.Post("/link/apple", guard, h.LinkApple)
	// Parola bcrypt ile hash'lenir (pahalı): sıkı sınıra da tabidir.
	a.Post("/link/password", authLimit, guard, h.LinkPassword)
	a.Delete("/link/google", guard, h.UnlinkProvider("google"))
	a.Delete("/link/apple", guard, h.UnlinkProvider("apple"))
	a.Delete("/account", guard, h.DeleteAccount)

	// --- Dictionary ---
	dict := api.Group("/dictionary", guard)
	dict.Get("/words", h.GetWordsForGroup)
	dict.Post("/words/batch", h.GetWordsByIds)

	// --- Progress (Leitner) ---
	prog := api.Group("/progress", guard)
	prog.Get("/word/:wordId", h.GetUserWord)
	prog.Get("/group", h.GetProgressForGroup)
	prog.Post("/learn", h.LearnWord)
	// Kaydı olmayan kelimeye "Bilmiyorum": kutu 1 kaydı (grup görülmüş sayılır).
	prog.Post("/unknown", h.MarkUnknown)
	prog.Post("/swipe", h.Swipe)
	prog.Get("/review/box/:box", h.ReviewWordsForBox)
	prog.Get("/review/box/:box/counts", h.UpcomingCountsForBox)

	// --- Users & günlük kullanım ---
	users := api.Group("/users", guard)
	users.Get("/me", h.Me)
	users.Patch("/me", h.UpdateProfile)
	users.Put("/me/current-group", h.SetCurrentGroup)
	users.Post("/me/advance-group", h.AdvanceGroup)
	users.Get("/me/usage", h.GetDailyUsage)
	// Grup açma uçları gövdedeki {level, group} ile idempotenttir: aynı grup gün
	// içinde yalnızca bir kez sayılır.
	users.Post("/me/usage/group-started", h.UnlockGroup(freeGroupColumn))
	users.Post("/me/usage/group-unlocked-by-ad", h.UnlockGroup("groups_unlocked_by_ad_today"))
	// Gövdedeki {checkpoint} ile idempotenttir: aynı reklam kapısı bir kez sayılır.
	users.Post("/me/usage/review-ad-pass", h.RecordReviewAdPass)
	// Reklam no-fill toleransı: sınırı sunucu zorlar, dolduysa 429 döner.
	users.Post("/me/usage/no-fill-pass", h.GrantNoFillPass)

	// Sayaç sıfırlama yalnızca geliştirme/test ortamında açıktır: production'da
	// kullanıcının kendi günlük limitlerini sıfırlayabilmesi gating'i anlamsız kılar.
	if !h.Cfg.IsProduction() {
		users.Post("/me/usage/reset", h.ResetDailyUsage)
	}

	// --- Map ---
	mp := api.Group("/map", guard)
	mp.Get("/levels/:level/groups", h.LevelGroups)
	mp.Get("/levels/:level/max-group", h.MaxGroupForLevel)
	mp.Get("/active-boxes-count", h.ActiveBoxesCount)
	// Açık (başlanmış, tamamlanmamış) gruplar ve aynı anda açık grup sınırı.
	mp.Get("/open-groups", h.OpenGroups)

	// --- Home ---
	home := api.Group("/home", guard)
	home.Get("/learned-count", h.LearnedCount)
	home.Get("/current-group", h.CurrentGroup)

	// --- Stats ---
	stats := api.Group("/stats", guard)
	stats.Post("/daily-login", h.LogDailyLogin)
	stats.Get("/summary", h.StatsSummary)
	stats.Get("/learned-words", h.LearnedWords)
	stats.Get("/login-dates", h.LoginDates)
}
