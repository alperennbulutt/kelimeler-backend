package handlers

import (
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// LearnedCount: GET /home/learned-count — otoriter count(box==6).
func (h *Handlers) LearnedCount(c *fiber.Ctx) error {
	cnt, err := learnedCount(h.DB, middleware.UserID(c))
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"learnedCount": cnt})
}

// learnedCount, kullanıcının kutu 6'daki (öğrenilmiş) kelime sayısıdır.
func learnedCount(db *gorm.DB, userID string) (int64, error) {
	var cnt int64
	err := db.Model(&models.WordProgress{}).Where("user_id = ? AND box = 6", userID).Count(&cnt).Error
	return cnt, err
}

// CurrentGroup: GET /home/current-group — home_repository.getCurrentGroupProgress (fallback'larla).
func (h *Handlers) CurrentGroup(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var user models.User
	if err := findUser(h.DB, userID, &user); err != nil {
		return err
	}

	title := "Yeni Grup"
	if user.CurrentGroupTitle != nil && *user.CurrentGroupTitle != "" {
		title = *user.CurrentGroupTitle
	}
	totalWords := user.CurrentGroupTotalWords
	if totalWords == 0 {
		totalWords = 50
	}
	level := user.CurrentLevel
	if level == "" {
		level = "A1"
	}

	return c.JSON(dto.CurrentGroupDTO{
		Level:       level,
		Group:       user.CurrentGroup,
		Title:       title,
		Description: user.CurrentGroupDescription,
		TotalWords:  totalWords,
	})
}
