package handlers

import (
	"strconv"
	"strings"
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
// cards, gereken grupların (ilerlemesi olan her grup, işaretçideki grup ve
// sonraki grup) kartlarıdır. İstemci elindeki grupları haveGroups'ta
// ("A1:1,A1:2,B1:7") bildirir: dictionaryVersion sözlüğün sürümüyle
// (db.DictionaryVersion) aynıysa yalnızca o gruplar atlanır, diğer gereken
// grupların kartları her zaman gelir. Sürüm verilmemiş, sayı olmayan ya da
// farklıysa haveGroups yok sayılır ve gereken bütün gruplar gönderilir.
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
	if snap.LearnedWordIDs, err = learnedWordIDs(h.DB, userID); err != nil {
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
	skip := map[string]bool{}
	if clientHasDictionary(c.Query("dictionaryVersion"), version) {
		skip = parseHaveGroups(c.Query("haveGroups"))
	}
	if snap.Cards, err = snapshotCards(h.DB, &user, skip); err != nil {
		return err
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

// learnedWordIDs, kullanıcının kutu 6'daki kelimelerinin kimlikleridir
// (wordId sırasıyla; yoksa boş liste).
func learnedWordIDs(gdb *gorm.DB, userID string) ([]string, error) {
	ids := []string{}
	err := gdb.Model(&models.WordProgress{}).Where("user_id = ? AND box = 6", userID).
		Order("word_id").Pluck("word_id", &ids).Error
	return ids, err
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

// parseHaveGroups, "A1:1,A1:2" biçimindeki grup listesini küme olarak döner;
// tanınmayan parçalar yok sayılır.
func parseHaveGroups(raw string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		level, grp, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(grp)
		if err != nil {
			continue
		}
		if key, ok := services.UnlockedGroupKey(level, n); ok {
			out[key] = true
		}
	}
	return out
}

// cardGroup, kartları gönderilecek bir gruptur.
type cardGroup struct {
	Level string
	Grp   int
}

// snapshotCards, gereken grupların kartlarını seviye, grup ve sıra sırasıyla
// döner: kullanıcının ilerlemesi olan her grup (grup, kelimenin sözlükteki
// grubudur), işaretçideki grup ve sonraki grup. skip'teki gruplar ("A1:3")
// atlanır. İşaretçi "tümü tamamlandı"daysa (C2, 0) yalnızca ilerlemeli
// gruplar gerekir.
func snapshotCards(gdb *gorm.DB, user *models.User, skip map[string]bool) ([]models.DictionaryWord, error) {
	var groups []cardGroup
	if err := gdb.Raw(`SELECT DISTINCT dw.level AS level, dw.grp AS grp
		FROM word_progresses wp
		JOIN dictionary_words dw ON dw.id = wp.word_id
		WHERE wp.user_id = ?`, user.ID).Scan(&groups).Error; err != nil {
		return nil, err
	}
	if services.IndexOfLevel(user.CurrentLevel) >= 0 && user.CurrentGroup >= 1 {
		groups = append(groups, cardGroup{user.CurrentLevel, user.CurrentGroup})
		maxGroup, err := maxGroupForLevel(gdb, user.CurrentLevel)
		if err != nil {
			return nil, err
		}
		if nl, ng, _ := services.AdvanceGroup(user.CurrentLevel, user.CurrentGroup, maxGroup); ng >= 1 {
			groups = append(groups, cardGroup{nl, ng})
		}
	}

	seen := map[cardGroup]bool{}
	var conds []string
	var args []any
	for _, g := range groups {
		if seen[g] || skip[g.Level+":"+strconv.Itoa(g.Grp)] {
			continue
		}
		seen[g] = true
		conds = append(conds, "(d.level = ? AND d.grp = ?)")
		args = append(args, g.Level, g.Grp)
	}
	cards := []models.DictionaryWord{}
	if len(conds) == 0 {
		return cards, nil
	}
	err := gdb.Raw(`SELECT d.* FROM dictionary_words d WHERE `+strings.Join(conds, " OR ")+`
		ORDER BY d.level, d.grp, d.word_order, d.id`, args...).Scan(&cards).Error
	return cards, err
}
