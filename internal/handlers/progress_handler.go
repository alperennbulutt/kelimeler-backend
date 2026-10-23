package handlers

import (
	"errors"
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

// LearnWord: POST /progress/learn — yeni kelimeyi kutu 2'ye alır.
//
// Kelime sözlükte olmalıdır; seviye, grup ve metinler istemciden değil sözlük
// satırından alınır (gövdedeki level/group/english/turkish yok sayılır; mevcut
// istemci onları göndermeye devam edebilir).
//
// İdempotenttir: kelimenin zaten ilerlemesi varsa (kutu 2 ve üstü) kayıt
// değiştirilmez ve mevcut durum döner. Aksi halde iki cihaz ya da bayat bir
// öğrenme destesi, ilerlemiş bir kelimeyi sessizce kutu 2'ye geri atardı.
// Yalnızca kutu 1'deki eski (Firestore'dan gelen) kayıtlar kutu 2'ye alınır.
func (h *Handlers) LearnWord(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var req dto.LearnRequest
	if err := c.BodyParser(&req); err != nil || req.WordID == "" {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "wordId gerekli")
	}

	var word models.DictionaryWord
	if err := h.DB.First(&word, "id = ?", req.WordID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiErr(fiber.StatusNotFound, "unknown_word", "kelime sözlükte bulunamadı")
		}
		return err
	}

	wp := models.WordProgress{
		UserID:  userID,
		WordID:  word.ID,
		Level:   word.Level,
		Grp:     word.Grp,
		English: word.English,
		Turkish: word.Turkish,
		Box:     1,
	}
	services.LearnNewWord(&wp)

	// Çakışmada yalnızca kutu 1'deki satır güncellenir; diğerlerinde INSERT
	// hiçbir satırı değiştirmez ve mevcut kayıt okunup döner.
	res := h.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "word_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"level", "grp", "box", "last_review_date", "next_review_date",
			"wrong_count", "learned", "english", "turkish", "updated_at",
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "word_progresses.box <= 1"}}},
	}).Create(&wp)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		var existing models.WordProgress
		if err := h.DB.Where("user_id = ? AND word_id = ?", userID, word.ID).First(&existing).Error; err != nil {
			return err
		}
		return c.JSON(&existing)
	}
	return c.JSON(&wp)
}

// Swipe: POST /progress/swipe {wordId, direction} — box matematiği sunucuda.
// Tekrar cevapları (swipe öncesi kutu 2..5) günlük review_answers_today
// sayacına da yazılır; yanıt gövdesi yalnızca kelime ilerlemesidir.
//
// Kelime satırı transaction boyunca kilitlidir (SELECT ... FOR UPDATE): aynı
// kelimeye eşzamanlı gelen cevaplar sıraya girer ve her biri öncekinin
// yazdığı kutuyu görür. Vadesi gelmemiş kelimeye (bkz. services.AcceptsSwipe)
// gelen swipe hiçbir şeyi değiştirmez, sayılmaz ve mevcut durumu döner; böylece
// yinelenen ya da eşzamanlı cevap kelimeyi iki kez ilerletmez.
func (h *Handlers) Swipe(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var req dto.SwipeRequest
	if err := c.BodyParser(&req); err != nil || req.WordID == "" {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "wordId gerekli")
	}
	var apply func(*models.WordProgress)
	switch req.Direction {
	case "right":
		apply = services.OnRightSwipe
	case "left":
		apply = services.OnLeftSwipe
	default:
		return apiErr(fiber.StatusBadRequest, "invalid_request", "direction 'right' veya 'left' olmalı")
	}

	// İlerleme ve günlük tekrar sayacı birlikte yazılır: kelime ilerleyip cevap
	// sayılmazsa kapı gevşer, cevap sayılıp kelime ilerlemezse aynı kelime
	// yeniden gelir ve iki kez sayılır.
	now := time.Now()
	_, endOfToday := reviewDayBounds(now)
	var wp models.WordProgress
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND word_id = ?", userID, req.WordID).First(&wp).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiErr(fiber.StatusNotFound, "not_found", "kelime ilerlemesi bulunamadı")
		}
		if err != nil {
			return err
		}
		if !services.AcceptsSwipe(&wp, endOfToday) {
			return nil
		}

		// Cevabın tekrar sayılıp sayılmayacağı swipe ÖNCESİ kutuya bağlıdır;
		// mutasyon kutuyu değiştirir (öğrenme ekranının 1 → 2 swipe'ı sonradan
		// bakılırsa tekrar cevabı gibi görünür).
		preBox := wp.Box
		apply(&wp)
		if err := tx.Save(&wp).Error; err != nil {
			return err
		}
		if !services.CountsAsReviewAnswer(preBox) {
			return nil
		}
		if err := resetUsageIfStale(tx, userID, now); err != nil {
			return err
		}
		if err := tx.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
			"review_answers_today": gorm.Expr("review_answers_today + 1"),
			"last_reset_date":      usageTouchedAt(now),
		}).Error; err != nil {
			return err
		}
		if (preBox == 6) == (wp.Box == 6) {
			return nil
		}
		// Kullanıcı satırı yukarıdaki UPDATE'ten beri bu transaction'da kilitli:
		// aynı kullanıcının eşzamanlı başka bir swipe'ı burada sayımı yapmadan
		// önce bekler ve bu transaction'ın commit'ini görür.
		return syncTotalLearned(tx, userID)
	})
	if err != nil {
		return err
	}
	return c.JSON(&wp)
}

// syncTotalLearned, users.total_learned cache'ini count(box==6) ile eşitler.
func syncTotalLearned(tx *gorm.DB, userID string) error {
	return tx.Model(&models.User{}).Where("id = ?", userID).Update("total_learned",
		tx.Model(&models.WordProgress{}).Select("COUNT(*)").Where("user_id = ? AND box = 6", userID),
	).Error
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
