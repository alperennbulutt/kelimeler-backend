package handlers

import (
	"strconv"
	"time"

	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm/clause"
)

// LogDailyLogin: POST /stats/daily-login — bugünün girişini idempotent kaydeder, streak döner.
func (h *Handlers) LogDailyLogin(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	dl := models.DailyLogin{UserID: userID, LoginDate: startOfDay(time.Now())}
	h.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "login_date"}},
		DoNothing: true,
	}).Create(&dl)
	return c.JSON(fiber.Map{"currentStreak": h.calculateStreak(userID)})
}

// calculateStreak, son 365 günün giriş kayıtlarından streak hesaplar.
func (h *Handlers) calculateStreak(userID string) int {
	now := time.Now()
	from := startOfDay(now).AddDate(0, 0, -365)
	var dates []time.Time
	h.DB.Model(&models.DailyLogin{}).
		Where("user_id = ? AND login_date >= ?", userID, from).
		Pluck("login_date", &dates)

	set := make(map[string]bool, len(dates))
	for _, d := range dates {
		set[services.FormatDate(d)] = true
	}
	return services.CalculateStreak(set, now)
}

// LoginDates: GET /stats/login-dates — son 365 günün giriş tarihleri (takvim için).
func (h *Handlers) LoginDates(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	from := startOfDay(time.Now()).AddDate(0, 0, -365)
	var dates []time.Time
	h.DB.Model(&models.DailyLogin{}).
		Where("user_id = ? AND login_date >= ?", userID, from).
		Order("login_date DESC").
		Pluck("login_date", &dates)

	out := make([]string, 0, len(dates))
	for _, d := range dates {
		out = append(out, services.FormatDate(d))
	}
	return c.JSON(fiber.Map{"dates": out})
}

// StatsSummary: GET /stats/summary — fetchStatsSummary portu (özet + rozet + streak, tek çağrı).
func (h *Handlers) StatsSummary(c *fiber.Ctx) error {
	userID := middleware.UserID(c)

	// 1) box==6 öğrenilen kelimeler (lastReviewDate desc)
	var learned []models.WordProgress
	h.DB.Where("user_id = ? AND box = 6", userID).
		Order("last_review_date DESC NULLS LAST").Find(&learned)
	totalLearned := len(learned)
	box6ByLevel := map[string]int{}
	for _, w := range learned {
		box6ByLevel[w.Level]++
	}
	lastFive := learned
	if len(lastFive) > 5 {
		lastFive = lastFive[:5]
	}

	// 2) box>=2 aktif kelimeler
	var active []models.WordProgress
	h.DB.Where("user_id = ? AND box >= 2", userID).Find(&active)
	activeByLevel := map[string]int{}
	var box2, box3, box4, box5 int
	for _, w := range active {
		activeByLevel[w.Level]++
		switch w.Box {
		case 2:
			box2++
		case 3:
			box3++
		case 4:
			box4++
		case 5:
			box5++
		}
	}

	// 3) Son 10 gün giriş verisi
	now := time.Now()
	tenDaysAgo := startOfDay(now).AddDate(0, 0, -10)
	var loginDates []time.Time
	h.DB.Model(&models.DailyLogin{}).
		Where("user_id = ? AND login_date >= ?", userID, tenDaysAgo).
		Pluck("login_date", &loginDates)
	loggedSet := map[string]bool{}
	for _, d := range loginDates {
		loggedSet[services.FormatDate(d)] = true
	}
	weeklyLoginData := map[string]bool{}
	for i := 0; i < 10; i++ {
		dStr := services.FormatDate(now.AddDate(0, 0, -i))
		weeklyLoginData[dStr] = loggedSet[dStr]
	}

	// 4) Streak + rozetler
	streak := h.calculateStreak(userID)

	earnedBefore := map[string]bool{}
	var earnedIDs []string
	h.DB.Model(&models.EarnedBadge{}).Where("user_id = ?", userID).Pluck("badge_id", &earnedIDs)
	for _, id := range earnedIDs {
		earnedBefore[id] = true
	}

	allBadges, newlyEarned := services.ComputeBadges(services.BadgeInput{
		ActiveByLevel:    activeByLevel,
		TotalActiveCount: len(active),
		LearnedCount:     totalLearned,
		CurrentStreak:    streak,
	}, earnedBefore)

	// Yeni kazanılan rozetleri kalıcılaştır.
	if len(newlyEarned) > 0 {
		rows := make([]models.EarnedBadge, 0, len(newlyEarned))
		for _, id := range newlyEarned {
			rows = append(rows, models.EarnedBadge{UserID: userID, BadgeID: id, EarnedAt: now})
		}
		h.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows)
	}

	badgeDTOs := toBadgeDTOs(allBadges)
	newlySet := map[string]bool{}
	for _, id := range newlyEarned {
		newlySet[id] = true
	}
	newlyDTOs := []dto.BadgeDTO{}
	for _, b := range badgeDTOs {
		if newlySet[b.ID] {
			newlyDTOs = append(newlyDTOs, b)
		}
	}

	return c.JSON(dto.StatsSummaryDTO{
		TotalLearned:      totalLearned,
		CurrentStreak:     streak,
		LearnedByLevel:    activeByLevel,
		Box6ByLevel:       box6ByLevel,
		Box2Count:         box2,
		Box3Count:         box3,
		Box4Count:         box4,
		Box5Count:         box5,
		WeeklyLoginData:   weeklyLoginData,
		Badges:            badgeDTOs,
		LastLearnedWords:  h.toLearnedWordDTOs(lastFive),
		TotalWordsByLevel: services.LevelTargets,
		NewlyEarnedBadges: newlyDTOs,
	})
}

// LearnedWords: GET /stats/learned-words?level=&limit=&offset= — sayfalı öğrenilmiş kelimeler.
func (h *Handlers) LearnedWords(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	level := c.Query("level")
	limit, _ := strconv.Atoi(c.Query("limit", "25"))
	offset, _ := strconv.Atoi(c.Query("offset", "0"))
	if limit <= 0 {
		limit = 25
	}

	base := h.DB.Model(&models.WordProgress{}).Where("user_id = ? AND box = 6", userID)
	if level != "" {
		base = base.Where("level = ?", level)
	}

	var total int64
	base.Count(&total)

	var words []models.WordProgress
	base.Order("last_review_date DESC NULLS LAST").Limit(limit).Offset(offset).Find(&words)

	return c.JSON(dto.LearnedWordsPageDTO{
		Items: h.toLearnedWordDTOs(words),
		Total: int(total),
	})
}

// toLearnedWordDTOs, eksik english/turkish'i dictionary'den doldurarak DTO'ya çevirir.
func (h *Handlers) toLearnedWordDTOs(words []models.WordProgress) []dto.LearnedWordDTO {
	var missingIDs []string
	for _, w := range words {
		if w.English == "" {
			missingIDs = append(missingIDs, w.WordID)
		}
	}
	dictMap := map[string]models.DictionaryWord{}
	if len(missingIDs) > 0 {
		var dws []models.DictionaryWord
		h.DB.Where("id IN ?", missingIDs).Find(&dws)
		for _, d := range dws {
			dictMap[d.ID] = d
		}
	}

	out := make([]dto.LearnedWordDTO, 0, len(words))
	for _, w := range words {
		eng, tur := w.English, w.Turkish
		if eng == "" {
			if d, ok := dictMap[w.WordID]; ok {
				eng, tur = d.English, d.Turkish
			} else {
				eng = w.WordID
			}
		}
		out = append(out, dto.LearnedWordDTO{
			WordID:         w.WordID,
			English:        eng,
			Turkish:        tur,
			Box:            w.Box,
			Level:          w.Level,
			LastReviewDate: w.LastReviewDate,
		})
	}
	return out
}

func toBadgeDTOs(badges []services.ComputedBadge) []dto.BadgeDTO {
	out := make([]dto.BadgeDTO, 0, len(badges))
	for _, b := range badges {
		out = append(out, dto.BadgeDTO{
			ID:       b.ID,
			Title:    b.Title,
			Subtitle: b.Subtitle,
			IsEarned: b.IsEarned,
			IconKey:  b.IconKey,
			Category: b.Category,
		})
	}
	return out
}
