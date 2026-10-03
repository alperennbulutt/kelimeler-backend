package handlers

import (
	"fmt"
	"strconv"
	"time"

	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GetUserWord: GET /progress/word/:wordId
func (h *Handlers) GetUserWord(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var wp models.WordProgress
	if err := h.DB.Where("user_id = ? AND word_id = ?", userID, c.Params("wordId")).First(&wp).Error; err != nil {
		return apiErr(fiber.StatusNotFound, "not_found", "kelime ilerlemesi bulunamadı")
	}
	return c.JSON(&wp)
}

// GetProgressForGroup: GET /progress/group?level=A1&group=3
func (h *Handlers) GetProgressForGroup(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	level := c.Query("level")
	group, _ := strconv.Atoi(c.Query("group"))

	var list []models.WordProgress
	if err := h.DB.Where("user_id = ? AND level = ? AND grp = ?", userID, level, group).Find(&list).Error; err != nil {
		return err
	}
	return c.JSON(list)
}

// LearnWord: POST /progress/learn — yeni kelimeyi kutu 2'ye alır (idempotent upsert).
func (h *Handlers) LearnWord(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var req dto.LearnRequest
	if err := c.BodyParser(&req); err != nil || req.WordID == "" {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "wordId gerekli")
	}

	wp := models.WordProgress{
		UserID:  userID,
		WordID:  req.WordID,
		Level:   req.Level,
		Grp:     req.Group,
		English: req.English,
		Turkish: req.Turkish,
		Box:     1,
	}
	services.LearnNewWord(&wp)

	if err := h.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "word_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"level", "grp", "box", "last_review_date", "next_review_date",
			"wrong_count", "learned", "english", "turkish", "updated_at",
		}),
	}).Create(&wp).Error; err != nil {
		return err
	}
	return c.JSON(&wp)
}

// Swipe: POST /progress/swipe {wordId, direction} — box matematiği sunucuda.
// Tekrar cevapları (swipe öncesi kutu 2..5) günlük review_answers_today
// sayacına da yazılır; yanıt gövdesi yalnızca kelime ilerlemesidir.
func (h *Handlers) Swipe(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var req dto.SwipeRequest
	if err := c.BodyParser(&req); err != nil || req.WordID == "" {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "wordId gerekli")
	}

	var wp models.WordProgress
	if err := h.DB.Where("user_id = ? AND word_id = ?", userID, req.WordID).First(&wp).Error; err != nil {
		return apiErr(fiber.StatusNotFound, "not_found", "kelime ilerlemesi bulunamadı")
	}

	// Cevabın tekrar sayılıp sayılmayacağı swipe ÖNCESİ kutuya bağlıdır; aşağıdaki
	// mutasyon kutuyu değiştirir (öğrenme ekranının 1 → 2 swipe'ı sonradan
	// bakılırsa tekrar cevabı gibi görünür).
	preBox := wp.Box

	switch req.Direction {
	case "right":
		services.OnRightSwipe(&wp)
	case "left":
		services.OnLeftSwipe(&wp)
	default:
		return apiErr(fiber.StatusBadRequest, "invalid_request", "direction 'right' veya 'left' olmalı")
	}

	// İlerleme ve günlük tekrar sayacı birlikte yazılır: kelime ilerleyip cevap
	// sayılmazsa kapı gevşer, cevap sayılıp kelime ilerlemezse aynı kelime
	// yeniden gelir ve iki kez sayılır.
	now := time.Now()
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&wp).Error; err != nil {
			return err
		}
		if !services.CountsAsReviewAnswer(preBox) {
			return nil
		}
		if err := resetUsageIfStale(tx, userID, now); err != nil {
			return err
		}
		return tx.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
			"review_answers_today": gorm.Expr("review_answers_today + 1"),
			"last_reset_date":      usageTouchedAt(now),
		}).Error
	})
	if err != nil {
		return err
	}
	h.syncTotalLearned(userID)
	return c.JSON(&wp)
}

// syncTotalLearned, users.total_learned cache'ini count(box==6) ile eşitler.
func (h *Handlers) syncTotalLearned(userID string) {
	var cnt int64
	h.DB.Model(&models.WordProgress{}).Where("user_id = ? AND box = 6", userID).Count(&cnt)
	h.DB.Model(&models.User{}).Where("id = ?", userID).Update("total_learned", cnt)
}

// ReviewWordsForBox: GET /progress/review/box/:box?date=YYYY-MM-DD
// nextReviewDate <= bugün filtresi sunucuda (Postgres index'i kullanır).
func (h *Handlers) ReviewWordsForBox(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	box, _ := strconv.Atoi(c.Params("box"))
	today := parseQueryDate(c.Query("date"))
	endOfToday := startOfDay(today).AddDate(0, 0, 1) // ertesi gün 00:00 (exclusive üst sınır)

	var list []models.WordProgress
	if err := h.DB.Where(
		"user_id = ? AND box = ? AND (next_review_date IS NULL OR next_review_date < ?)",
		userID, box, endOfToday,
	).Find(&list).Error; err != nil {
		return err
	}
	return c.JSON(list)
}

// UpcomingCountsForBox: GET /progress/review/box/:box/counts?date=YYYY-MM-DD
// user_word_repository.getUpcomingCountsForBox portu (0..30 gün dağılımı + en yoğun gün).
func (h *Handlers) UpcomingCountsForBox(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	box, _ := strconv.Atoi(c.Params("box"))
	todayDate := startOfDay(parseQueryDate(c.Query("date")))

	var list []models.WordProgress
	if err := h.DB.Where("user_id = ? AND box = ?", userID, box).Find(&list).Error; err != nil {
		return err
	}

	dailyCounts := map[int]int{}
	totalMonth := 0
	for _, wp := range list {
		if wp.NextReviewDate == nil {
			dailyCounts[0]++
			totalMonth++
			continue
		}
		nextDate := startOfDay(*wp.NextReviewDate)
		diffDays := int(nextDate.Sub(todayDate).Hours() / 24)
		switch {
		case diffDays <= 0:
			dailyCounts[0]++
			totalMonth++
		case diffDays <= 30:
			dailyCounts[diffDays]++
			totalMonth++
		}
	}

	busiestIdx, maxCount := -1, -1
	for i := 1; i <= 30; i++ {
		if dailyCounts[i] > maxCount {
			maxCount = dailyCounts[i]
			busiestIdx = i
		}
	}
	busiestLabel := "-"
	if maxCount > 0 {
		busiestLabel = fmt.Sprintf("%d gün sonra", busiestIdx)
	} else {
		maxCount = 0
	}

	later := 0
	dcStr := map[string]int{}
	for _, day := range sortedIntKeys(dailyCounts) {
		dcStr[strconv.Itoa(day)] = dailyCounts[day]
		if day >= 2 {
			later += dailyCounts[day]
		}
	}

	return c.JSON(dto.UpcomingCounts{
		Today:           dailyCounts[0],
		Tomorrow:        dailyCounts[1],
		Later:           later,
		DailyCounts:     dcStr,
		TotalMonthCount: totalMonth,
		BusiestDayLabel: busiestLabel,
		BusiestDayCount: maxCount,
	})
}
