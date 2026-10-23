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
	if err := h.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "login_date"}},
		DoNothing: true,
	}).Create(&dl).Error; err != nil {
		return err
	}
	streak, err := h.calculateStreak(userID)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"currentStreak": streak})
}

// calculateStreak, son 365 günün giriş kayıtlarından streak hesaplar.
func (h *Handlers) calculateStreak(userID string) (int, error) {
	now := time.Now()
	from := startOfDay(now).AddDate(0, 0, -365)
	var dates []time.Time
	if err := h.DB.Model(&models.DailyLogin{}).
		Where("user_id = ? AND login_date >= ?", userID, from).
		Pluck("login_date", &dates).Error; err != nil {
		return 0, err
	}

	set := make(map[string]bool, len(dates))
	for _, d := range dates {
		set[services.FormatDate(d)] = true
	}
	return services.CalculateStreak(set, now), nil
}

// LoginDates: GET /stats/login-dates — son 365 günün giriş tarihleri (takvim için).
func (h *Handlers) LoginDates(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	from := startOfDay(time.Now()).AddDate(0, 0, -365)
	var dates []time.Time
	if err := h.DB.Model(&models.DailyLogin{}).
		Where("user_id = ? AND login_date >= ?", userID, from).
		Order("login_date DESC").
		Pluck("login_date", &dates).Error; err != nil {
		return err
	}

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
	if err := h.DB.Where("user_id = ? AND box = 6", userID).
		Order("last_review_date DESC NULLS LAST").Find(&learned).Error; err != nil {
		return err
	}
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
	if err := h.DB.Where("user_id = ? AND box >= 2", userID).Find(&active).Error; err != nil {
		return err
	}
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
	if err := h.DB.Model(&models.DailyLogin{}).
		Where("user_id = ? AND login_date >= ?", userID, tenDaysAgo).
		Pluck("login_date", &loginDates).Error; err != nil {
		return err
	}
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
	streak, err := h.calculateStreak(userID)
	if err != nil {
		return err
	}

	earnedBefore := map[string]bool{}
	var earnedIDs []string
	if err := h.DB.Model(&models.EarnedBadge{}).Where("user_id = ?", userID).Pluck("badge_id", &earnedIDs).Error; err != nil {
		return err
	}
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
		if err := h.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
			return err
		}
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

	lastLearned, err := h.toLearnedWordDTOs(lastFive)
	if err != nil {
		return err
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
		LastLearnedWords:  lastLearned,
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
	if err := base.Count(&total).Error; err != nil {
		return err
	}

	var words []models.WordProgress
	if err := base.Order("last_review_date DESC NULLS LAST").Limit(limit).Offset(offset).Find(&words).Error; err != nil {
		return err
	}

	items, err := h.toLearnedWordDTOs(words)
	if err != nil {
		return err
	}
	return c.JSON(dto.LearnedWordsPageDTO{
		Items: items,
		Total: int(total),
	})
}

// toLearnedWordDTOs, eksik english/turkish'i dictionary'den doldurarak DTO'ya çevirir.
func (h *Handlers) toLearnedWordDTOs(words []models.WordProgress) ([]dto.LearnedWordDTO, error) {
	var missingIDs []string
	for _, w := range words {
		if w.English == "" {
			missingIDs = append(missingIDs, w.WordID)
		}
	}
	dictMap := map[string]models.DictionaryWord{}
	if len(missingIDs) > 0 {
		var dws []models.DictionaryWord
		if err := h.DB.Where("id IN ?", missingIDs).Find(&dws).Error; err != nil {
			return nil, err
		}
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
	return out, nil
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
