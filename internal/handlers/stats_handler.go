package handlers

import (
	"sort"
	"strconv"
	"time"

	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm/clause"
)

// statsNow, istatistik uçlarının saatidir; testler gün sınırını sınamak için değiştirir.
var statsNow = time.Now

// LogDailyLogin: POST /stats/daily-login — bugünün girişini idempotent kaydeder, streak döner.
//
// "Bugün", kullanım konumundaki (UsageLocation) gündür ve "YYYY-MM-DD" metni
// olarak yazılır (bkz. services.UsageDate: date kolonu + PreferSimpleProtocol).
func (h *Handlers) LogDailyLogin(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	now := statsNow()
	if err := h.DB.Exec(`INSERT INTO daily_logins (user_id, login_date, created_at)
		VALUES (?, CAST(? AS date), ?)
		ON CONFLICT (user_id, login_date) DO NOTHING`,
		userID, services.UsageDate(now), now.UTC()).Error; err != nil {
		return err
	}
	days, err := h.loginDays(userID, now)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"currentStreak": services.CalculateStreak(days, now)})
}

// loginDays, son 365 günün giriş günlerini kullanım günü kümesi olarak döndürür.
// Bir gün fazlası okunur: eski (UTC günlü) bir kaydın created_at'i pencerenin
// ilk gününe düşebilir (bkz. services.LoginDays).
func (h *Handlers) loginDays(userID string, now time.Time) (map[string]bool, error) {
	var rows []services.LoginRecord
	if err := h.DB.Raw(`SELECT to_char(login_date, 'YYYY-MM-DD') AS login_date, created_at
		FROM daily_logins
		WHERE user_id = ? AND login_date >= CAST(? AS date)`,
		userID, services.UsageDateDaysAgo(now, 366)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return services.LoginDays(rows), nil
}

// LoginDates: GET /stats/login-dates — son 365 günün giriş tarihleri (takvim için, yeniden eskiye).
func (h *Handlers) LoginDates(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	now := statsNow()
	days, err := h.loginDays(userID, now)
	if err != nil {
		return err
	}

	from, today := services.UsageDateDaysAgo(now, 365), services.UsageDateDaysAgo(now, 0)
	out := make([]string, 0, len(days))
	for d := range days {
		// "YYYY-MM-DD" metinleri sözlük sırasıyla tarih sırasıdır.
		if d >= from && d <= today {
			out = append(out, d)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
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

	// 3) Son 10 gün giriş verisi + streak (aynı küme, kullanım günüyle)
	now := statsNow()
	loggedSet, err := h.loginDays(userID, now)
	if err != nil {
		return err
	}
	weeklyLoginData := map[string]bool{}
	for i := 0; i < 10; i++ {
		dStr := services.UsageDateDaysAgo(now, i)
		weeklyLoginData[dStr] = loggedSet[dStr]
	}

	// 4) Streak + rozetler
	streak := services.CalculateStreak(loggedSet, now)

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
