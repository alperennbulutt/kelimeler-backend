package handlers

import (
	"time"

	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// UpdateProfile: PATCH /users/me {displayName?}
func (h *Handlers) UpdateProfile(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var req dto.UpdateProfileRequest
	if err := c.BodyParser(&req); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "geçersiz istek")
	}
	if req.DisplayName != nil {
		name, err := dto.NormalizeDisplayName(*req.DisplayName)
		if err != nil {
			return apiErr(fiber.StatusBadRequest, "invalid_display_name", err.Error())
		}
		h.DB.Model(&models.User{}).Where("id = ?", userID).Update("display_name", name)
	}
	return h.respondUser(c, userID)
}

// SetCurrentGroup: PUT /users/me/current-group — user_profile_repository.setCurrentGroup.
func (h *Handlers) SetCurrentGroup(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var req dto.SetCurrentGroupRequest
	if err := c.BodyParser(&req); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "geçersiz istek")
	}
	updates := map[string]any{
		"current_level": req.Level,
		"current_group": req.Group,
	}
	if req.Title != nil {
		updates["current_group_title"] = *req.Title
	}
	if req.Description != nil {
		updates["current_group_description"] = *req.Description
	}
	if req.TotalWords != nil {
		updates["current_group_total_words"] = *req.TotalWords
	}
	h.DB.Model(&models.User{}).Where("id = ?", userID).Updates(updates)
	return h.respondUser(c, userID)
}

// AdvanceGroup: POST /users/me/advance-group — sonraki level/grup'u sunucu hesaplar.
func (h *Handlers) AdvanceGroup(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var user models.User
	if err := h.DB.First(&user, "id = ?", userID).Error; err != nil {
		return apiErr(fiber.StatusNotFound, "user_not_found", "kullanıcı bulunamadı")
	}

	var maxGroup int
	h.DB.Model(&models.DictionaryWord{}).Where("level = ?", user.CurrentLevel).
		Select("COALESCE(MAX(grp),0)").Scan(&maxGroup)

	nextLevel, nextGroup, levelChanged := services.AdvanceGroup(user.CurrentLevel, user.CurrentGroup, maxGroup)
	user.CurrentLevel = nextLevel
	user.CurrentGroup = nextGroup
	h.DB.Model(&models.User{}).Where("id = ?", userID).
		Updates(map[string]any{"current_level": nextLevel, "current_group": nextGroup})

	return c.JSON(fiber.Map{
		"user":         dto.NewUserDTO(&user, h.linkedProviders(userID)),
		"levelChanged": levelChanged,
		"newLevel":     nextLevel,
		"newGroup":     nextGroup,
	})
}

// --- Günlük kullanım ---

func usageDTO(u *models.User) dto.DailyUsageDTO {
	remaining := services.MaxNoFillPassesPerDay - u.NoFillPassesToday
	if remaining < 0 {
		remaining = 0
	}
	return dto.DailyUsageDTO{
		GroupsStartedTodayCount: u.GroupsStartedTodayCount,
		GroupsUnlockedByAdToday: u.GroupsUnlockedByAdToday,
		ReviewAdPassCountToday:  u.ReviewAdPassCountToday,
		NoFillPassesToday:       u.NoFillPassesToday,
		NoFillPassesRemaining:   remaining,
		LastResetDate:           u.LastResetDate,
	}
}

func (h *Handlers) GetDailyUsage(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var user models.User
	if err := h.DB.First(&user, "id = ?", userID).Error; err != nil {
		return apiErr(fiber.StatusNotFound, "user_not_found", "kullanıcı bulunamadı")
	}
	if services.NeedsReset(user.LastResetDate) {
		h.resetUsage(userID)
		h.DB.First(&user, "id = ?", userID)
	}
	return c.JSON(usageDTO(&user))
}

// ResetDailyUsage: POST /users/me/usage/reset
func (h *Handlers) ResetDailyUsage(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	h.resetUsage(userID)
	var user models.User
	h.DB.First(&user, "id = ?", userID)
	return c.JSON(usageDTO(&user))
}

func (h *Handlers) resetUsage(userID string) {
	h.DB.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
		"groups_started_today_count":  0,
		"groups_unlocked_by_ad_today": 0,
		"review_ad_pass_count_today":  0,
		"no_fill_passes_today":        0,
		"last_reset_date":             time.Now().UnixMilli(),
	})
}

// IncrementUsage, belirtilen sayaç kolonunu atomik artırır (gün değiştiyse önce sıfırlar).
func (h *Handlers) IncrementUsage(column string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID := middleware.UserID(c)
		var user models.User
		if err := h.DB.First(&user, "id = ?", userID).Error; err != nil {
			return apiErr(fiber.StatusNotFound, "user_not_found", "kullanıcı bulunamadı")
		}
		now := time.Now().UnixMilli()
		if services.NeedsReset(user.LastResetDate) {
			h.resetUsage(userID)
		}
		h.DB.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
			column:            gorm.Expr(column + " + 1"),
			"last_reset_date": now,
		})
		h.DB.First(&user, "id = ?", userID)
		return c.JSON(usageDTO(&user))
	}
}

// GrantNoFillPass: POST /users/me/usage/no-fill-pass
//
// Reklam hiç yüklenemediğinde (no-fill) istemci bu geçişi ister. Günlük sınır
// (services.MaxNoFillPassesPerDay) dolmuşsa 429 döner ve istemci akışı bloke
// eder. Sınır sunucuda tutulur; istemci taraflı bir bayrakla aşılamaz.
//
// Artırım koşullu UPDATE ile yapılır: eşzamanlı istekler sınırı aşamaz.
func (h *Handlers) GrantNoFillPass(c *fiber.Ctx) error {
	userID := middleware.UserID(c)

	var user models.User
	if err := h.DB.First(&user, "id = ?", userID).Error; err != nil {
		return apiErr(fiber.StatusNotFound, "user_not_found", "kullanıcı bulunamadı")
	}
	if services.NeedsReset(user.LastResetDate) {
		h.resetUsage(userID)
	}

	res := h.DB.Model(&models.User{}).
		Where("id = ? AND no_fill_passes_today < ?", userID, services.MaxNoFillPassesPerDay).
		Updates(map[string]any{
			"no_fill_passes_today": gorm.Expr("no_fill_passes_today + 1"),
			"last_reset_date":      time.Now().UnixMilli(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apiErr(fiber.StatusTooManyRequests, "no_fill_limit_reached",
			"reklamsız geçiş hakkın bugünlük doldu")
	}

	h.DB.First(&user, "id = ?", userID)
	return c.JSON(usageDTO(&user))
}
