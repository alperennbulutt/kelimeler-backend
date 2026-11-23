package handlers

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
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
	out, err := h.loginDateList(middleware.UserID(c), statsNow())
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"dates": out})
}

// loginDateList, GET /stats/login-dates'in "dates" listesidir: son 365 günün
// giriş günleri, yeniden eskiye ("YYYY-MM-DD"). GET /sync/snapshot da kullanır.
func (h *Handlers) loginDateList(userID string, now time.Time) ([]string, error) {
	days, err := h.loginDays(userID, now)
	if err != nil {
		return nil, err
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
	return out, nil
}

// StatsSummary: GET /stats/summary — fetchStatsSummary portu (özet + rozet + streak, tek çağrı).
func (h *Handlers) StatsSummary(c *fiber.Ctx) error {
	summary, err := h.statsSummary(middleware.UserID(c), statsNow(), true)
	if err != nil {
		return err
	}
	return c.JSON(summary)
}

// statsSummary, GET /stats/summary yanıtını kurar.
//
// persistBadges true ise yeni kazanılan rozetler earned_badges'e yazılır ve
// yalnızca bu çağrıda gerçekten eklenenler newlyEarnedBadges'te döner (rozet
// bildirimi bir kez çıkar). false ise (GET /sync/snapshot) hiçbir şey
// yazılmaz ve newlyEarnedBadges boştur: anlık görüntü bildirimi tüketmez,
// rozet bir sonraki GET /stats/summary'de bildirilir. badges listesindeki
// isEarned iki durumda da aynıdır.
func (h *Handlers) statsSummary(userID string, now time.Time, persistBadges bool) (dto.StatsSummaryDTO, error) {
	// 1) Kutu sayıları: satırları belleğe almadan seviye × kutu kırılımı.
	var counts []struct {
		Level string
		Box   int
		N     int
	}
	if err := h.DB.Raw(`SELECT level, box, COUNT(*) AS n
		FROM word_progresses
		WHERE user_id = ? AND box >= 2
		GROUP BY level, box`, userID).Scan(&counts).Error; err != nil {
		return dto.StatsSummaryDTO{}, err
	}
	activeByLevel := map[string]int{} // box>=2
	box6ByLevel := map[string]int{}
	var totalActive, totalLearned, box2, box3, box4, box5 int
	for _, r := range counts {
		activeByLevel[r.Level] += r.N
		totalActive += r.N
		switch r.Box {
		case 2:
			box2 += r.N
		case 3:
			box3 += r.N
		case 4:
			box4 += r.N
		case 5:
			box5 += r.N
		case 6:
			box6ByLevel[r.Level] += r.N
			totalLearned += r.N
		}
	}

	// 2) Son öğrenilen 5 kelime (box==6, lastReviewDate desc)
	var lastFive []models.WordProgress
	if err := h.DB.Where("user_id = ? AND box = 6", userID).
		Order("last_review_date DESC NULLS LAST, id DESC").Limit(5).
		Find(&lastFive).Error; err != nil {
		return dto.StatsSummaryDTO{}, err
	}

	// 3) Son 10 gün giriş verisi + streak (aynı küme, kullanım günüyle)
	loggedSet, err := h.loginDays(userID, now)
	if err != nil {
		return dto.StatsSummaryDTO{}, err
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
		return dto.StatsSummaryDTO{}, err
	}
	for _, id := range earnedIDs {
		earnedBefore[id] = true
	}

	levelTotals, err := h.levelTotals()
	if err != nil {
		return dto.StatsSummaryDTO{}, err
	}
	levelProgress, err := h.levelProgress(userID)
	if err != nil {
		return dto.StatsSummaryDTO{}, err
	}

	allBadges, newlyEarned := services.ComputeBadges(services.BadgeInput{
		ActiveByLevel:    levelProgress,
		LevelTotals:      levelTotals,
		TotalActiveCount: totalActive,
		LearnedCount:     totalLearned,
		CurrentStreak:    streak,
	}, earnedBefore)

	// Yeni kazanılan rozetleri kalıcılaştır. Yalnızca bu istekte gerçekten
	// eklenen satırlar "yeni" sayılır: eşzamanlı iki özet isteği aynı rozeti
	// yeni görse de popup bir kez çıkar.
	inserted := map[string]bool{}
	if persistBadges {
		if inserted, err = h.insertEarnedBadges(userID, newlyEarned, now); err != nil {
			return dto.StatsSummaryDTO{}, err
		}
	}

	badgeDTOs := toBadgeDTOs(allBadges)
	newlyDTOs := []dto.BadgeDTO{}
	for _, b := range badgeDTOs {
		if inserted[b.ID] {
			newlyDTOs = append(newlyDTOs, b)
		}
	}

	lastLearned, err := h.toLearnedWordDTOs(lastFive)
	if err != nil {
		return dto.StatsSummaryDTO{}, err
	}

	return dto.StatsSummaryDTO{
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
		TotalWordsByLevel: services.TotalWordsByLevel(levelTotals),
		LevelTotals:       levelTotals,
		NewlyEarnedBadges: newlyDTOs,
	}, nil
}

// levelTotals, sözlükteki seviye başına kelime sayısıdır (önbellekli; sözlük
// yalnızca seed/import ile değişir). Yalnızca kelimesi olan seviyeler döner.
func (h *Handlers) levelTotals() (map[string]int, error) {
	return services.CachedLevelTotals(time.Now(), func() (map[string]int, error) {
		var rows []struct {
			Level string
			N     int
		}
		if err := h.DB.Model(&models.DictionaryWord{}).
			Select("level, COUNT(*) AS n").Group("level").
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		totals := map[string]int{}
		for _, r := range rows {
			if services.IndexOfLevel(r.Level) >= 0 && r.N > 0 {
				totals[r.Level] = r.N
			}
		}
		return totals, nil
	})
}

// levelProgress, kullanıcının box>=2 kelimelerini sözlükteki seviyesine göre
// sayar. Seviye rozeti bununla levelTotals'ı karşılaştırır; haritanın grup
// tamamlama ölçütüyle (her kelime ilerlemeli ve kutu 1'de değil) aynıdır.
func (h *Handlers) levelProgress(userID string) (map[string]int, error) {
	var rows []struct {
		Level string
		N     int
	}
	if err := h.DB.Raw(`SELECT dw.level, COUNT(*) AS n
		FROM word_progresses wp
		JOIN dictionary_words dw ON dw.id = wp.word_id
		WHERE wp.user_id = ? AND wp.box >= 2
		GROUP BY dw.level`, userID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.Level] = r.N
	}
	return out, nil
}

// insertEarnedBadges, rozetleri earned_badges'e ekler (zaten varsa dokunmaz) ve
// yalnızca bu çağrıda gerçekten eklenenlerin kümesini döner.
func (h *Handlers) insertEarnedBadges(userID string, badgeIDs []string, at time.Time) (map[string]bool, error) {
	inserted := map[string]bool{}
	if len(badgeIDs) == 0 {
		return inserted, nil
	}
	values := make([]string, 0, len(badgeIDs))
	args := make([]any, 0, 3*len(badgeIDs))
	for _, id := range badgeIDs {
		values = append(values, "(?, ?, ?)")
		args = append(args, userID, id, at.UTC())
	}
	var ids []string
	if err := h.DB.Raw(`INSERT INTO earned_badges (user_id, badge_id, earned_at) VALUES `+
		strings.Join(values, ", ")+
		` ON CONFLICT (user_id, badge_id) DO NOTHING RETURNING badge_id`, args...).
		Scan(&ids).Error; err != nil {
		return nil, err
	}
	for _, id := range ids {
		inserted[id] = true
	}
	return inserted, nil
}

// Öğrenilmiş kelimeler sayfasının boyutu: varsayılan ve üst sınır.
const (
	defaultLearnedWordsLimit = 25
	maxLearnedWordsLimit     = 200
)

// LearnedWords: GET /stats/learned-words?level=&limit=&offset= — sayfalı öğrenilmiş kelimeler.
// limit 1..200 aralığına çekilir (verilmezse 25); sıralama sabittir, sayfalar
// eşit tarihli kelimelerde kaymaz.
func (h *Handlers) LearnedWords(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	level := c.Query("level")
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	if limit <= 0 {
		limit = defaultLearnedWordsLimit
	}
	limit = min(limit, maxLearnedWordsLimit)
	offset = max(offset, 0)

	// Her sorgu kendi zincirini kurar: gorm zincirini Count'tan sonra yeniden
	// kullanmak koşulları/seçimi bir sonraki sorguya taşıyabilir.
	learned := func() *gorm.DB {
		q := h.DB.Model(&models.WordProgress{}).Where("user_id = ? AND box = 6", userID)
		if level != "" {
			q = q.Where("level = ?", level)
		}
		return q
	}

	var total int64
	if err := learned().Count(&total).Error; err != nil {
		return err
	}

	var words []models.WordProgress
	if err := learned().Order("last_review_date DESC NULLS LAST, id DESC").
		Limit(limit).Offset(offset).Find(&words).Error; err != nil {
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
