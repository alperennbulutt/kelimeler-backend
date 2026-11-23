package handlers

import (
	"strconv"
	"time"

	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// SyncSnapshot: GET /sync/snapshot?dictionaryVersion=N — çevrimdışı mod
// istemcisinin indirdiği anlık görüntü (bkz. dto.SnapshotDTO ve
// kutularla_kelimeler/docs/CEVRIMDISI_MOD.md).
//
// Her bölüm, adı geçen ucun yanıtını kuran kodla üretilir; biçimler
// birebirdir. Kullanım bölümü GET /users/me/usage gibi önce gün dönümünü
// uygular. İstatistik bölümü yeni kazanılan rozetleri yazmaz (bkz.
// statsSummary).
//
// dictionaryVersion verilmişse ve sözlüğün sürümüyle (db.DictionaryVersion)
// aynıysa cards boş döner; verilmemiş, sayı olmayan ya da farklı sürümde
// kartlar gönderilir.
//
// Bölümler tek bir transaction'da okunmaz: anlık görüntü indirilirken başka
// bir cihazdan yazım gelirse bölümler birbirinden bir cevap farklı olabilir.
// İstemci her senkronizasyondan sonra görüntüyü yeniden indirir.
func (h *Handlers) SyncSnapshot(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	now := time.Now()

	version, err := db.DictionaryVersion(h.DB)
	if err != nil {
		return err
	}

	if err := resetUsageIfStale(h.DB, userID, now); err != nil {
		return err
	}
	var user models.User
	if err := findUser(h.DB, userID, &user); err != nil {
		return err
	}
	u, err := h.userJSON(&user)
	if err != nil {
		return err
	}

	snap := dto.SnapshotDTO{
		ServerNow:         now.UTC(),
		UsageDate:         services.UsageDate(now),
		DictionaryVersion: version,
		User:              u,
		Usage:             usageDTO(&user),
		Levels:            map[string][]dto.LevelGroupDTO{},
		Cards:             []models.DictionaryWord{},
	}

	snap.Progress = []models.WordProgress{}
	if err := h.DB.Where("user_id = ? AND box BETWEEN 1 AND 5", userID).
		Order("word_id").Find(&snap.Progress).Error; err != nil {
		return err
	}
	if snap.LearnedCount, err = learnedCount(h.DB, userID); err != nil {
		return err
	}
	for _, level := range services.Levels {
		groups, err := h.computeLevelGroups(userID, level)
		if err != nil {
			return err
		}
		snap.Levels[level] = groups
	}
	if snap.OpenGroups, err = h.openGroupsDTO(h.DB, userID); err != nil {
		return err
	}
	if !clientHasDictionary(c.Query("dictionaryVersion"), version) {
		if snap.Cards, err = snapshotCards(h.DB, &user); err != nil {
			return err
		}
	}
	statsAt := statsNow()
	if snap.Stats, err = h.statsSummary(userID, statsAt, false); err != nil {
		return err
	}
	if snap.LoginDates, err = h.loginDateList(userID, statsAt); err != nil {
		return err
	}
	return c.JSON(snap)
}

// clientHasDictionary, istemcinin bildirdiği sözlük sürümünün güncel olup
// olmadığını söyler.
func clientHasDictionary(param string, version int64) bool {
	if param == "" {
		return false
	}
	n, err := strconv.ParseInt(param, 10, 64)
	return err == nil && n == version
}

// snapshotCards, kullanıcının ilerlemesi olan her grubun (grup, kelimenin
// sözlükteki grubudur) ve işaretçideki grupla ondan sonraki grubun bütün
// kartlarını seviye, grup ve sıra sırasıyla döner. İşaretçi "tümü
// tamamlandı"daysa (C2, 0) yalnızca ilerlemeli gruplar gelir.
func snapshotCards(gdb *gorm.DB, user *models.User) ([]models.DictionaryWord, error) {
	type group struct {
		level string
		grp   int
	}
	var extra []group
	if services.IndexOfLevel(user.CurrentLevel) >= 0 && user.CurrentGroup >= 1 {
		extra = append(extra, group{user.CurrentLevel, user.CurrentGroup})
		maxGroup, err := maxGroupForLevel(gdb, user.CurrentLevel)
		if err != nil {
			return nil, err
		}
		if nl, ng, _ := services.AdvanceGroup(user.CurrentLevel, user.CurrentGroup, maxGroup); ng >= 1 {
			extra = append(extra, group{nl, ng})
		}
	}
	for len(extra) < 2 {
		// Sorgu her zaman iki çift alır; olmayan grup hiçbir satırla eşleşmez.
		extra = append(extra, group{"", 0})
	}

	cards := []models.DictionaryWord{}
	err := gdb.Raw(`SELECT d.* FROM dictionary_words d
		WHERE (d.level, d.grp) IN (
			SELECT DISTINCT dw.level, dw.grp
			FROM word_progresses wp
			JOIN dictionary_words dw ON dw.id = wp.word_id
			WHERE wp.user_id = ?)
		   OR (d.level = ? AND d.grp = ?)
		   OR (d.level = ? AND d.grp = ?)
		ORDER BY d.level, d.grp, d.word_order, d.id`,
		user.ID, extra[0].level, extra[0].grp, extra[1].level, extra[1].grp).Scan(&cards).Error
	return cards, err
}
