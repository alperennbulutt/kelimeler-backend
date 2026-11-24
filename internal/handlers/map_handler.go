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
	course, err := requestCourse(c)
	if err != nil {
		return err
	}
	groups, err := h.computeLevelGroups(userID, course, c.Params("level"))
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

// completed, haritadaki "tamamlandı" kuralıdır: grupta kelime var, en az
// birine başlanmış ve hiçbiri kaydı olmayan ya da kutu 1'de değil. Harita
// (computeLevelGroups) ve gövdesiz advance-group (groupCompleted) aynı kuralı
// buradan okur.
func (r levelGroupProgress) completed() bool {
	return r.TotalWords > 0 && r.StartedWords > 0 && r.RemainingInBox1 == 0
}

// seen, grubun "görüldü" kuralıdır: grupta kelime var ve hepsinin ilerleme
// kaydı var (her kelime en az bir kez cevaplandı; bilindi ya da bilinmedi).
// Seviyedeki sonraki grup, bu grup görülünce açılabilir (bkz.
// services.GroupLockReason). Tamamlanan grup görülmüştür de.
func (r levelGroupProgress) seen() bool {
	return r.TotalWords > 0 && r.StartedWords == r.TotalWords
}

// levelGroupProgressColumns, levelProgressQuery üzerinden grup özetini sayan
// SELECT listesidir.
const levelGroupProgressColumns = `SELECT COUNT(*) AS total_words,
		       COUNT(wp.id) AS started_words,
		       COUNT(*) FILTER (WHERE wp.id IS NULL OR wp.box = 1) AS remaining_in_box1`

// levelProgressQuery, seviyenin sözlük kelimelerini kullanıcının bir kurstaki
// ilerlemesiyle eşler; parametreleri sırasıyla kullanıcı, kurs ve seviyedir.
// İlerleme satırı yalnızca kendi seviyesi sözlük satırınınkiyle aynıysa
// sayılır (eski kayıtlarda seviye farklı yazılmış olabilir).
const levelProgressQuery = `
	FROM dictionary_words d
	LEFT JOIN word_progresses wp
	  ON wp.user_id = ? AND wp.course = ? AND wp.word_id = d.id AND wp.level = d.level
	WHERE d.level = ?`

// computeLevelGroups, bir seviye için kursun grup durumlarını
// (kilit/tamamlanma) hesaplar. Sözlük ve ilerleme satırları belleğe alınmaz;
// grup başına sayılar tek bir aggregate sorguyla gelir. Kilit kuralı
// services.GroupLockReason'dadır.
func (h *Handlers) computeLevelGroups(userID, course, level string) ([]dto.LevelGroupDTO, error) {
	previousLevelCompleted, err := isPreviousLevelCompleted(h.DB, userID, course, level)
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
		ORDER BY d.grp`, userID, course, level).Scan(&rows).Error; err != nil {
		return nil, err
	}

	// Açık grup sayısı yalnızca başlanmamış bir grubun kararında gerekir;
	// önceki seviye bitmemişse ya da test modunda hiç sorulmaz.
	openGroups := 0
	if previousLevelCompleted && !h.unlockAll() {
		if openGroups, err = h.countOpenGroups(h.DB, userID, course); err != nil {
			return nil, err
		}
	}

	// Grup grup hesapla (sıralı kilit: önceki grup görülmeden sonraki açılmaz).
	result := []dto.LevelGroupDTO{}
	previousSeen := true
	for _, r := range rows {
		hasStarted := r.StartedWords > 0

		lockReason := ""
		switch {
		case h.unlockAll():
		case r.TotalWords == 0:
			// Kelimesiz grup sorgudan gelmez (gruplar sözlük satırlarından
			// sayılır); gelseydi açılacak bir şeyi olmazdı.
		default:
			lockReason = services.GroupLockReason(previousLevelCompleted, hasStarted, previousSeen, openGroups)
		}
		isUnlocked := lockReason == "" && (h.unlockAll() || r.TotalWords > 0)

		result = append(result, dto.LevelGroupDTO{
			GroupNumber:     r.Grp,
			TotalWords:      r.TotalWords,
			RemainingInBox1: r.RemainingInBox1,
			IsCompleted:     r.completed(),
			IsUnlocked:      isUnlocked,
			HasStarted:      hasStarted,
			IsSeen:          r.seen(),
			SeenWords:       r.StartedWords,
			LockReason:      lockReason,
		})

		if r.TotalWords > 0 {
			previousSeen = r.seen()
		}
	}
	return result, nil
}

// unlockAll, TEST_MODE_UNLOCK_ALL açık mı söyler: bütün gruplar kilitsizdir,
// açık grup sınırı da uygulanmaz.
func (h *Handlers) unlockAll() bool {
	return h.Cfg != nil && h.Cfg.TestModeUnlockAll
}

// openGroupsQuery, kullanıcının bir kurstaki başlanmış ama tamamlanmamış
// gruplarını döner; parametreleri kullanıcı, kurs, kullanıcı, kurstur (önceki
// seviye süzgeci listOpenGroups'tadır). Sözlüğün tamamı değil, yalnızca
// kullanıcının kaydı olan gruplar taranır. "Başlanmış" ve "tamamlanmamış"
// levelProgressQuery ile aynı eşlemeyle sayılır: ilerleme satırı yalnızca
// kendi seviyesi sözlük satırınınkiyle aynıysa sayılır, grup sözlükten okunur.
const openGroupsQuery = `
	WITH started AS (
		SELECT DISTINCT d.level, d.grp
		FROM word_progresses wp
		JOIN dictionary_words d ON d.id = wp.word_id AND d.level = wp.level
		WHERE wp.user_id = ? AND wp.course = ?
	)
	SELECT s.level AS level, s.grp AS grp,
	       COUNT(*) AS total_words,
	       COUNT(*) FILTER (WHERE wp.id IS NULL OR wp.box = 1) AS remaining_in_box1
	FROM started s
	JOIN dictionary_words d ON d.level = s.level AND d.grp = s.grp
	LEFT JOIN word_progresses wp
	  ON wp.user_id = ? AND wp.course = ? AND wp.word_id = d.id AND wp.level = d.level
	GROUP BY s.level, s.grp
	HAVING COUNT(*) FILTER (WHERE wp.id IS NULL OR wp.box = 1) > 0`

// openGroupRow, openGroupsQuery'nin satırıdır.
type openGroupRow struct {
	Level           string
	Grp             int
	TotalWords      int
	RemainingInBox1 int
}

// listOpenGroups, kullanıcının bir kursta sınıra sayılan açık gruplarını
// seviye ve grup sırasıyla döner. Sınır kurs başınadır: başka bir kursun açık
// grupları sayılmaz. Önceki seviyesi tamamlanmamış seviyenin grupları başlanmış
// olsa da sayılmaz: kendileri previous_level ile kilitlidir, kullanıcı onlara
// zaten giremez. Sayılsalardı taşınmış veri ya da sözlüğe sonradan eklenen
// bir grup (önceki seviyeyi yeniden "tamamlanmamış" yapar) kullanıcıyı
// büsbütün takabilirdi: sayılan gruplar kilitli, yeni grup da sınırdan
// açılamaz. TEST_MODE_UNLOCK_ALL'da kilit olmadığı için hepsi sayılır. db,
// çağıranın transaction'ı olabilir.
func (h *Handlers) listOpenGroups(db *gorm.DB, userID, course string) ([]openGroupRow, error) {
	// Seviye adları (A1..C2) alfabetik sırada da seviye sırasındadır.
	var rows []openGroupRow
	if err := db.Raw(openGroupsQuery+`
	ORDER BY s.level, s.grp`, userID, course, userID, course).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if h.unlockAll() {
		return rows, nil
	}
	// Önceki seviye her seviye için bir kez sorulur.
	levelOpen := map[string]bool{}
	counted := rows[:0]
	for _, r := range rows {
		open, ok := levelOpen[r.Level]
		if !ok {
			var err error
			if open, err = isPreviousLevelCompleted(db, userID, course, r.Level); err != nil {
				return nil, err
			}
			levelOpen[r.Level] = open
		}
		if open {
			counted = append(counted, r)
		}
	}
	return counted, nil
}

// countOpenGroups, kullanıcının sınıra sayılan açık grup sayısıdır (bkz.
// listOpenGroups).
func (h *Handlers) countOpenGroups(db *gorm.DB, userID, course string) (int, error) {
	rows, err := h.listOpenGroups(db, userID, course)
	return len(rows), err
}

// groupStarted, (level, grp) grubunda kullanıcının kurstaki en az bir
// ilerleme kaydı olup olmadığını söyler (haritadaki hasStarted). db, çağıranın
// transaction'ı olabilir.
func groupStarted(db *gorm.DB, userID, course, level string, grp int) (bool, error) {
	var started bool
	err := db.Raw(`SELECT EXISTS (SELECT 1`+levelProgressQuery+`
		  AND d.grp = ? AND wp.id IS NOT NULL)`, userID, course, level, grp).Scan(&started).Error
	return started, err
}

// openGroupLimitReached, (level, grp) grubu başlanmamışken açık grup sınırı
// (services.MaxOpenGroups) dolu mu söyler: true ise grup açılamaz. Başlanmış
// grup sınırdan etkilenmez; önceki seviyesi tamamlanmamış grup da etkilenmez:
// başlasa da sınıra sayılmaz (bkz. listOpenGroups), kilidi previous_level'dir.
// TEST_MODE_UNLOCK_ALL sınırı kapatır. db, çağıranın transaction'ı olabilir.
func (h *Handlers) openGroupLimitReached(db *gorm.DB, userID, course, level string, grp int) (bool, error) {
	if h.unlockAll() {
		return false, nil
	}
	started, err := groupStarted(db, userID, course, level, grp)
	if err != nil || started {
		return false, err
	}
	if done, err := isPreviousLevelCompleted(db, userID, course, level); err != nil || !done {
		return false, err
	}
	n, err := h.countOpenGroups(db, userID, course)
	return n >= services.MaxOpenGroups, err
}

// OpenGroups: GET /map/open-groups — açık gruplar ve sınır. İstemci "yeni bir
// grup açmak için önce açık gruplarını bitir" mesajında bunları gösterir.
func (h *Handlers) OpenGroups(c *fiber.Ctx) error {
	course, err := requestCourse(c)
	if err != nil {
		return err
	}
	out, err := h.openGroupsDTO(h.DB, middleware.UserID(c), course)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// openGroupsDTO, GET /map/open-groups yanıtını kursun gruplarıyla kurar (GET
// /sync/snapshot da kullanır).
func (h *Handlers) openGroupsDTO(db *gorm.DB, userID, course string) (dto.OpenGroupsDTO, error) {
	rows, err := h.listOpenGroups(db, userID, course)
	if err != nil {
		return dto.OpenGroupsDTO{}, err
	}
	groups := make([]dto.OpenGroupDTO, 0, len(rows))
	for _, r := range rows {
		groups = append(groups, dto.OpenGroupDTO{
			Level:           r.Level,
			Group:           r.Grp,
			TotalWords:      r.TotalWords,
			RemainingInBox1: r.RemainingInBox1,
		})
	}
	return dto.OpenGroupsDTO{
		OpenGroupCount: len(groups),
		MaxOpenGroups:  services.MaxOpenGroups,
		OpenGroups:     groups,
	}, nil
}

// groupProgress, (level, grp) grubunun ilerleme özetidir
// (computeLevelGroups'taki satırla aynı sayım). db, çağıranın transaction'ı
// olabilir.
func groupProgress(db *gorm.DB, userID, course, level string, grp int) (levelGroupProgress, error) {
	var r levelGroupProgress
	err := db.Raw(levelGroupProgressColumns+levelProgressQuery+`
		  AND d.grp = ?`, userID, course, level, grp).Scan(&r).Error
	return r, err
}

// groupCompleted, (level, grp) grubunun haritada tamamlanmış görünüp
// görünmediğini söyler (computeLevelGroups'taki IsCompleted ile aynı kural).
// db, çağıranın transaction'ı olabilir.
func groupCompleted(db *gorm.DB, userID, course, level string, grp int) (bool, error) {
	r, err := groupProgress(db, userID, course, level, grp)
	return r.completed(), err
}

// groupSeen, (level, grp) grubunun haritada görülmüş görünüp görünmediğini
// söyler (computeLevelGroups'taki IsSeen ile aynı kural). db, çağıranın
// transaction'ı olabilir.
func groupSeen(db *gorm.DB, userID, course, level string, grp int) (bool, error) {
	r, err := groupProgress(db, userID, course, level, grp)
	return r.seen(), err
}

// groupLockReason, (level, grp) grubunun haritadaki (computeLevelGroups)
// kilit nedenini döner; grup açıksa "". Öğrenme yazımının kapısı bunu sorar
// (bkz. learnMapLocked): haritada kilitli bir grubu yalnızca değiştirilmiş
// bir istemci açabilir.
//
// Ucuz yol: grupta ilerleme varsa (başlanmış grup) sıralı kilit ve açık grup
// sınırı uygulanmaz, yalnızca önceki seviyenin tamamlanmasına bakılır.
// Seviyenin bütün gruplarını sayan computeLevelGroups yalnızca başlanmamış
// bir grubun ilk kelimesinde çalışır.
func (h *Handlers) groupLockReason(userID, course, level string, grp int) (reason string, started bool, err error) {
	if h.unlockAll() {
		return "", false, nil
	}
	if started, err = groupStarted(h.DB, userID, course, level, grp); err != nil {
		return "", false, err
	}
	if started {
		done, err := isPreviousLevelCompleted(h.DB, userID, course, level)
		if err != nil || done {
			return "", true, err
		}
		return services.LockReasonPreviousLevel, true, nil
	}
	groups, err := h.computeLevelGroups(userID, course, level)
	if err != nil {
		return "", false, err
	}
	for _, g := range groups {
		if g.GroupNumber == grp {
			return g.LockReason, false, nil
		}
	}
	// Grup sözlükte yok: kelime sözlükten okunduğu için olmamalı; kilit
	// kararı verilecek bir veri de yok.
	return "", false, nil
}

// isPreviousLevelCompleted: önceki seviye tamamlanmış mı (bkz. isLevelDone).
// A1'in önceki seviyesi yoktur. db, çağıranın transaction'ı olabilir.
func isPreviousLevelCompleted(db *gorm.DB, userID, course, level string) (bool, error) {
	idx := services.IndexOfLevel(level)
	if idx <= 0 {
		return true, nil // A1 için önceki seviye yok
	}
	return isLevelDone(db, userID, course, services.Levels[idx-1])
}

// isLevelDone: earnedBadges hızlı yolu, yoksa seviyenin kurstaki tamamlanma
// sayımı. Haritanın previous_level kilidi ve gövdeli advance-group'un seviye
// geçişi aynı kuralı buradan okur. db, çağıranın transaction'ı olabilir.
//
// Seviye rozetleri (earned_badges) kullanıcı başınadır ve kurs öncesinde
// İngilizce ilerlemeyle kazanıldı; hızlı yol yalnızca İngilizce kursunda
// geçerlidir. Diğer kurslarda seviye yalnızca o kursun ilerlemesiyle
// tamamlanır (aksi halde İngilizce A1 rozeti Almanca A2'yi açardı). Diğer
// kurslar seviye rozeti de yazmaz (bkz. statsSummary).
func isLevelDone(db *gorm.DB, userID, course, level string) (bool, error) {
	if course != services.DefaultCourse {
		return isLevelCompleted(db, userID, course, level)
	}
	var cnt int64
	if err := db.Model(&models.EarnedBadge{}).
		Where("user_id = ? AND badge_id = ?", userID, "level_"+level).Count(&cnt).Error; err != nil {
		return false, err
	}
	if cnt > 0 {
		return true, nil
	}
	return isLevelCompleted(db, userID, course, level)
}

// isLevelCompleted, seviyenin bütün grupları tamamlanmış mı söyler: seviyede
// kelime vardır ve hiçbiri kaydı olmayan ya da kutu 1'de değildir. Bir grubun
// tamamlanması kilit durumuna bağlı olmadığı için önceki seviyelere inmek
// gerekmez.
func isLevelCompleted(db *gorm.DB, userID, course, level string) (bool, error) {
	var r levelGroupProgress
	if err := db.Raw(`SELECT COUNT(*) AS total_words,
		       COUNT(*) FILTER (WHERE wp.id IS NULL OR wp.box = 1) AS remaining_in_box1`+
		levelProgressQuery, userID, course, level).Scan(&r).Error; err != nil {
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

// ActiveBoxesCount: GET /map/active-boxes-count — kursun kutu 2..5 toplamı.
func (h *Handlers) ActiveBoxesCount(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	course, err := requestCourse(c)
	if err != nil {
		return err
	}
	var cnt int64
	if err := h.DB.Model(&models.WordProgress{}).
		Where("user_id = ? AND course = ? AND box IN ?", userID, course, []int{2, 3, 4, 5}).Count(&cnt).Error; err != nil {
		return err
	}
	return c.JSON(fiber.Map{"count": cnt})
}
