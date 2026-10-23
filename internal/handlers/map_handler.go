package handlers

import (
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// LevelGroups: GET /map/levels/:level/groups — map_repository.getLevelGroups portu.
func (h *Handlers) LevelGroups(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	groups, err := h.computeLevelGroups(userID, c.Params("level"))
	if err != nil {
		return err
	}
	return c.JSON(groups)
}

// levelGroupProgress, bir grubun sözlük ve kullanıcı ilerlemesi özetidir.
type levelGroupProgress struct {
	Grp             int
	TotalWords      int
	StartedWords    int // ilerleme kaydı olan kelimeler (kutu fark etmez)
	RemainingInBox1 int // kaydı olmayan ya da kutu 1'deki kelimeler
}

// levelProgressQuery, seviyenin sözlük kelimelerini kullanıcının ilerlemesiyle
// eşler. İlerleme satırı yalnızca kendi seviyesi sözlük satırınınkiyle aynıysa
// sayılır (eski kayıtlarda seviye farklı yazılmış olabilir).
const levelProgressQuery = `
	FROM dictionary_words d
	LEFT JOIN word_progresses wp
	  ON wp.user_id = ? AND wp.word_id = d.id AND wp.level = d.level
	WHERE d.level = ?`

// computeLevelGroups, bir seviye için grup durumlarını (kilit/tamamlanma) hesaplar.
// Sözlük ve ilerleme satırları belleğe alınmaz; grup başına sayılar tek bir
// aggregate sorguyla gelir.
func (h *Handlers) computeLevelGroups(userID, level string) ([]dto.LevelGroupDTO, error) {
	previousLevelCompleted, err := h.isPreviousLevelCompleted(userID, level)
	if err != nil {
		return nil, err
	}

	var rows []levelGroupProgress
	if err := h.DB.Raw(`SELECT d.grp AS grp,
		       COUNT(*) AS total_words,
		       COUNT(wp.id) AS started_words,
		       COUNT(*) FILTER (WHERE wp.id IS NULL OR wp.box = 1) AS remaining_in_box1`+
		levelProgressQuery+`
		GROUP BY d.grp
		ORDER BY d.grp`, userID, level).Scan(&rows).Error; err != nil {
		return nil, err
	}

	// Grup grup hesapla (sıralı-kilit mantığı korunur).
	result := []dto.LevelGroupDTO{}
	previousCompleted := true
	for _, r := range rows {
		hasStarted := r.StartedWords > 0
		isCompleted := r.TotalWords > 0 && hasStarted && r.RemainingInBox1 == 0

		var isUnlocked bool
		switch {
		case h.Cfg.TestModeUnlockAll:
			isUnlocked = true
		case !previousLevelCompleted:
			isUnlocked = false
		case !hasStarted && !previousCompleted:
			isUnlocked = false
		default:
			isUnlocked = r.TotalWords > 0
		}

		result = append(result, dto.LevelGroupDTO{
			GroupNumber:     r.Grp,
			TotalWords:      r.TotalWords,
			RemainingInBox1: r.RemainingInBox1,
			IsCompleted:     isCompleted,
			IsUnlocked:      isUnlocked,
			HasStarted:      hasStarted,
		})

		if r.TotalWords > 0 {
			previousCompleted = isCompleted
		}
	}
	return result, nil
}

// groupLockedOnMap, (level, grp) grubunun haritada (computeLevelGroups)
// kilitli görünüp görünmediğini söyler. Öğrenme yazımının kapısı bunu sorar
// (bkz. LearnWord): haritada kilitli bir grubu yalnızca değiştirilmiş bir
// istemci açabilir.
//
// Ucuz yol: grupta ilerleme varsa (başlanmış grup) sıralı kilit artık
// uygulanmaz, yalnızca önceki seviyenin tamamlanmasına bakılır. Seviyenin
// bütün gruplarını sayan computeLevelGroups yalnızca başlanmamış bir grubun
// ilk kelimesinde çalışır.
func (h *Handlers) groupLockedOnMap(userID, level string, grp int) (bool, error) {
	if h.Cfg != nil && h.Cfg.TestModeUnlockAll {
		return false, nil
	}
	var started bool
	if err := h.DB.Raw(`SELECT EXISTS (SELECT 1`+levelProgressQuery+`
		  AND d.grp = ? AND wp.id IS NOT NULL)`, userID, level, grp).Scan(&started).Error; err != nil {
		return false, err
	}
	if started {
		done, err := h.isPreviousLevelCompleted(userID, level)
		return !done, err
	}
	groups, err := h.computeLevelGroups(userID, level)
	if err != nil {
		return false, err
	}
	for _, g := range groups {
		if g.GroupNumber == grp {
			return !g.IsUnlocked, nil
		}
	}
	// Grup sözlükte yok: kelime sözlükten okunduğu için olmamalı; kilit
	// kararı verilecek bir veri de yok.
	return false, nil
}

// isPreviousLevelCompleted: earnedBadges hızlı yolu, yoksa önceki seviyenin
// tamamlanma sayımı. A1'in önceki seviyesi yoktur.
func (h *Handlers) isPreviousLevelCompleted(userID, level string) (bool, error) {
	idx := services.IndexOfLevel(level)
	if idx <= 0 {
		return true, nil // A1 için önceki seviye yok
	}
	prev := services.Levels[idx-1]

	var cnt int64
	if err := h.DB.Model(&models.EarnedBadge{}).
		Where("user_id = ? AND badge_id = ?", userID, "level_"+prev).Count(&cnt).Error; err != nil {
		return false, err
	}
	if cnt > 0 {
		return true, nil
	}
	return h.isLevelCompleted(userID, prev)
}

// isLevelCompleted, seviyenin bütün grupları tamamlanmış mı söyler: seviyede
// kelime vardır ve hiçbiri kaydı olmayan ya da kutu 1'de değildir. Bir grubun
// tamamlanması kilit durumuna bağlı olmadığı için önceki seviyelere inmek
// gerekmez.
func (h *Handlers) isLevelCompleted(userID, level string) (bool, error) {
	var r levelGroupProgress
	if err := h.DB.Raw(`SELECT COUNT(*) AS total_words,
		       COUNT(*) FILTER (WHERE wp.id IS NULL OR wp.box = 1) AS remaining_in_box1`+
		levelProgressQuery, userID, level).Scan(&r).Error; err != nil {
		return false, err
	}
	return r.TotalWords > 0 && r.RemainingInBox1 == 0, nil
}

// maxGroupForLevel, seviyenin sözlükteki en büyük grup numarasıdır (seviye
// boşsa ya da tanınmıyorsa 0).
func maxGroupForLevel(db *gorm.DB, level string) (int, error) {
	var maxGroup int
	err := db.Model(&models.DictionaryWord{}).Where("level = ?", level).
		Select("COALESCE(MAX(grp),0)").Scan(&maxGroup).Error
	return maxGroup, err
}

// MaxGroupForLevel: GET /map/levels/:level/max-group
func (h *Handlers) MaxGroupForLevel(c *fiber.Ctx) error {
	maxGroup, err := maxGroupForLevel(h.DB, c.Params("level"))
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"maxGroup": maxGroup})
}

// ActiveBoxesCount: GET /map/active-boxes-count — kutu 2..5 toplamı.
func (h *Handlers) ActiveBoxesCount(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var cnt int64
	if err := h.DB.Model(&models.WordProgress{}).
		Where("user_id = ? AND box IN ?", userID, []int{2, 3, 4, 5}).Count(&cnt).Error; err != nil {
		return err
	}
	return c.JSON(fiber.Map{"count": cnt})
}
