package handlers

import (
	"strconv"

	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
)

// GetWordsForGroup: GET /dictionary/words?level=A1&group=3
// dictionary_repository.getWordsForGroup karşılığı (order'a göre sıralı).
func (h *Handlers) GetWordsForGroup(c *fiber.Ctx) error {
	level := c.Query("level")
	group, _ := strconv.Atoi(c.Query("group"))

	var words []models.DictionaryWord
	if err := h.DB.Where("level = ? AND grp = ?", level, group).
		Order("word_order").Find(&words).Error; err != nil {
		return err
	}
	return c.JSON(words)
}

// GetWordsByIds: POST /dictionary/words/batch {ids:[...]}
// dictionary_repository.getWordsByIds karşılığı (Postgres IN — chunk gerekmez).
func (h *Handlers) GetWordsByIds(c *fiber.Ctx) error {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := c.BodyParser(&req); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "ids gerekli")
	}
	if len(req.IDs) == 0 {
		return c.JSON([]models.DictionaryWord{})
	}
	var words []models.DictionaryWord
	if err := h.DB.Where("id IN ?", req.IDs).Find(&words).Error; err != nil {
		return err
	}
	return c.JSON(words)
}
