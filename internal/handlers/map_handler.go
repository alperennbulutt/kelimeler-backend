package handlers

import (
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
)

// LevelGroups: GET /map/levels/:level/groups — map_repository.getLevelGroups portu.
func (h *Handlers) LevelGroups(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	groups := h.computeLevelGroups(userID, c.Params("level"))
	return c.JSON(groups)
}

// computeLevelGroups, bir seviye için grup durumlarını (kilit/tamamlanma) hesaplar.
func (h *Handlers) computeLevelGroups(userID, level string) []dto.LevelGroupDTO {
	previousLevelCompleted := h.isPreviousLevelCompletedFast(userID, level)

	// 1) Bu seviyenin dictionary kelimeleri (sadece id + grup).
	var dictWords []models.DictionaryWord
	h.DB.Select("id", "grp").Where("level = ?", level).Find(&dictWords)

	groupToWordIDs := map[int][]string{}
	for _, w := range dictWords {
		groupToWordIDs[w.Grp] = append(groupToWordIDs[w.Grp], w.ID)
	}
	result := []dto.LevelGroupDTO{}
	if len(groupToWordIDs) == 0 {
		return result
	}

	// 2) Bu seviyedeki kullanıcı ilerlemesi.
	var progress []models.WordProgress
	h.DB.Select("word_id", "box").Where("user_id = ? AND level = ?", userID, level).Find(&progress)
	progByWordID := map[string]models.WordProgress{}
	for _, p := range progress {
		progByWordID[p.WordID] = p
	}

	// 3) Grup grup hesapla (sıralı-kilit mantığı korunur).
	previousCompleted := true
	for _, gnum := range sortedIntKeys(groupToWordIDs) {
		wordIDs := groupToWordIDs[gnum]
		totalWords := len(wordIDs)

		hasStarted := false
		remainingInBox1 := 0
		for _, wid := range wordIDs {
			if p, ok := progByWordID[wid]; ok {
				hasStarted = true
				if p.Box == 1 {
					remainingInBox1++
				}
			} else {
				remainingInBox1++
			}
		}

		isCompleted := totalWords > 0 && hasStarted && remainingInBox1 == 0

		var isUnlocked bool
		switch {
		case h.Cfg.TestModeUnlockAll:
			isUnlocked = true
		case !previousLevelCompleted:
			isUnlocked = false
		case !hasStarted && !previousCompleted:
			isUnlocked = false
		default:
			isUnlocked = totalWords > 0
		}

		result = append(result, dto.LevelGroupDTO{
			GroupNumber:     gnum,
			TotalWords:      totalWords,
			RemainingInBox1: remainingInBox1,
			IsCompleted:     isCompleted,
			IsUnlocked:      isUnlocked,
			HasStarted:      hasStarted,
		})

		if totalWords > 0 {
			previousCompleted = isCompleted
		}
	}
	return result
}

// isPreviousLevelCompletedFast: earnedBadges hızlı yolu, yoksa tarama fallback'i.
func (h *Handlers) isPreviousLevelCompletedFast(userID, level string) bool {
	idx := services.IndexOfLevel(level)
	if idx <= 0 {
		return true // A1 için önceki seviye yok
	}
	prev := services.Levels[idx-1]

	var cnt int64
	h.DB.Model(&models.EarnedBadge{}).Where("user_id = ? AND badge_id = ?", userID, "level_"+prev).Count(&cnt)
	if cnt > 0 {
		return true
	}
	return h.isLevelCompletedByScan(userID, prev)
}

func (h *Handlers) isLevelCompletedByScan(userID, level string) bool {
	groups := h.computeLevelGroups(userID, level)
	if len(groups) == 0 {
		return false
	}
	for _, g := range groups {
		if !g.IsCompleted {
			return false
		}
	}
	return true
}

// MaxGroupForLevel: GET /map/levels/:level/max-group
func (h *Handlers) MaxGroupForLevel(c *fiber.Ctx) error {
	var maxGroup int
	h.DB.Model(&models.DictionaryWord{}).Where("level = ?", c.Params("level")).
		Select("COALESCE(MAX(grp),0)").Scan(&maxGroup)
	return c.JSON(fiber.Map{"maxGroup": maxGroup})
}

// ActiveBoxesCount: GET /map/active-boxes-count — kutu 2..5 toplamı.
func (h *Handlers) ActiveBoxesCount(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var cnt int64
	h.DB.Model(&models.WordProgress{}).Where("user_id = ? AND box IN ?", userID, []int{2, 3, 4, 5}).Count(&cnt)
	return c.JSON(fiber.Map{"count": cnt})
}
