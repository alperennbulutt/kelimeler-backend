package handlers

import (
	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
)

// RegisterRoutes, tüm API route'larını mount eder.
func RegisterRoutes(api fiber.Router, h *Handlers, ts *auth.TokenService) {
	api.Get("/health", func(c *fiber.Ctx) error {
		var dict int64
		h.DB.Model(&models.DictionaryWord{}).Count(&dict)
		return c.JSON(fiber.Map{"status": "ok", "dictionaryWords": dict})
	})

	guard := middleware.RequireAuth(ts)

	// --- Auth (yetkilendirme gerektirmeyen) ---
	a := api.Group("/auth")
	a.Post("/register", h.Register)
	a.Post("/login", h.Login)
	a.Post("/google", h.GoogleAuth)
	a.Post("/apple", h.AppleAuth)
	a.Post("/refresh", h.Refresh)
	a.Post("/logout", h.Logout)

	// --- Auth (yetkilendirme gerektiren) ---
	a.Get("/me", guard, h.Me)
	a.Post("/link/google", guard, h.LinkGoogle)
	a.Post("/link/apple", guard, h.LinkApple)
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
	users.Post("/me/usage/reset", h.ResetDailyUsage)
	users.Post("/me/usage/group-started", h.IncrementUsage("groups_started_today_count"))
	users.Post("/me/usage/group-unlocked-by-ad", h.IncrementUsage("groups_unlocked_by_ad_today"))
	users.Post("/me/usage/review-ad-pass", h.IncrementUsage("review_ad_pass_count_today"))

	// --- Map ---
	mp := api.Group("/map", guard)
	mp.Get("/levels/:level/groups", h.LevelGroups)
	mp.Get("/levels/:level/max-group", h.MaxGroupForLevel)
	mp.Get("/active-boxes-count", h.ActiveBoxesCount)

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
