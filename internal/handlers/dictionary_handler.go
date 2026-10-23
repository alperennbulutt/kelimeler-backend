package handlers

import (
	"fmt"
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

// maxBatchIDs, POST /dictionary/words/batch'in tek istekte kabul ettiği en
// fazla (tekil, boş olmayan) id sayısıdır. İstemci bir tekrar batch'i kadar
// (reviewBatchSize = 50) gönderir; sınır ona geniş pay bırakır. Sınırsız
// liste, 1 MiB'lık gövdeyle ~100 bin elemanlı bir IN sorgusu demekti
// (PreferSimpleProtocol ile hepsi sorgu metnine gömülür).
const maxBatchIDs = 500

// GetWordsByIds: POST /dictionary/words/batch {ids:[...]}
// dictionary_repository.getWordsByIds karşılığı (Postgres IN — chunk gerekmez).
//
// Boş id'ler yok sayılır, tekrarlar bir kez sorgulanır (istemci yanıtı id'ye
// göre eşler; sıra garanti edilmez). Tekil id sayısı maxBatchIDs'i aşarsa
// 400 invalid_request döner.
func (h *Handlers) GetWordsByIds(c *fiber.Ctx) error {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := c.BodyParser(&req); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "ids gerekli")
	}
	ids, ok := uniqueBatchIDs(req.IDs, maxBatchIDs)
	if !ok {
		return apiErr(fiber.StatusBadRequest, "invalid_request",
			fmt.Sprintf("tek istekte en fazla %d id gönderilebilir", maxBatchIDs))
	}
	if len(ids) == 0 {
		return c.JSON([]models.DictionaryWord{})
	}
	var words []models.DictionaryWord
	if err := h.DB.Where("id IN ?", ids).Find(&words).Error; err != nil {
		return err
	}
	return c.JSON(words)
}

// uniqueBatchIDs, boş id'leri atar ve tekrarları ilk görülme sırasıyla tekile
// indirir. Tekil id sayısı max'ı aşarsa ok=false döner.
func uniqueBatchIDs(raw []string, max int) (ids []string, ok bool) {
	seen := make(map[string]struct{}, min(len(raw), max+1))
	ids = make([]string, 0, min(len(raw), max))
	for _, id := range raw {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		if len(ids) == max {
			return nil, false
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, true
}
