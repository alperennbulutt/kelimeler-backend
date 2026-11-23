package handlers

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/observability"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GetUserWord: GET /progress/word/:wordId
func (h *Handlers) GetUserWord(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var wp models.WordProgress
	err := h.DB.Where("user_id = ? AND word_id = ?", userID, c.Params("wordId")).First(&wp).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apiErr(fiber.StatusNotFound, "not_found", "kelime ilerlemesi bulunamadı")
	}
	if err != nil {
		return err
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
// İdempotenttir: kelimenin zaten ilerlemesi varsa (kutu 2 ve üstü) ilerleme
// değiştirilmez (yalnızca sözlükten farklı yazılmış seviye/grup/metin
// düzeltilir) ve mevcut durum döner. Aksi halde iki cihaz ya da bayat bir
// öğrenme destesi, ilerlemiş bir kelimeyi sessizce kutu 2'ye geri atardı.
// Yalnızca kutu 1'deki kayıtlar (Firestore'dan gelen eski kayıtlar ve
// "Bilmiyorum" ile yazılanlar, bkz. MarkUnknown) kutu 2'ye alınır.
//
// Yeni yazım reklam kapısından geçer (sözleşme K4, bkz. learnGate): grup bugün
// açılmamışsa ya da haritada kilitliyse GATE_ENFORCEMENT=enforce'ta 403
// gate_required, açık grup sınırı doluyken başlanmamış grupta 403
// open_group_limit döner; log'da ihlal raporlanıp yazılır.
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
	now := time.Now()
	services.LearnNewWord(&wp, now)

	mapState, err := h.learnMapLocked(userID, &word)
	if err != nil {
		return err
	}
	var resp models.WordProgress
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		if err := h.learnGate(c, tx, userID, &word, mapState, now); err != nil {
			return err
		}

		// Çakışmada yalnızca kutu 1'deki satır güncellenir; diğerlerinde INSERT
		// hiçbir satırı değiştirmez ve mevcut kayıt okunup döner.
		res := tx.Clauses(clause.OnConflict{
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
		if res.RowsAffected > 0 {
			resp = wp
			return nil
		}
		// İlerleme (kutu, tarihler, sayaçlar) korunur; yalnızca sözlükten gelen
		// alanlar sözlük satırıyla eşitlenir. Seviyesi/grubu farklı yazılmış eski
		// bir kayıt aksi halde ne öğrenme ekranının grup sorgusunda ne de harita
		// tamamlanmasında görünür: kelime her seferinde yeniden sorulur ve grup
		// hiçbir zaman tamamlanmış sayılmaz (eski kod bunu kutu 2'ye sıfırlayarak
		// düzeltiyordu).
		if err := tx.Model(&models.WordProgress{}).
			Where("user_id = ? AND word_id = ? AND (level <> ? OR grp <> ? OR english <> ? OR turkish <> ?)",
				userID, word.ID, word.Level, word.Grp, word.English, word.Turkish).
			Updates(map[string]any{
				"level": word.Level, "grp": word.Grp, "english": word.English, "turkish": word.Turkish,
			}).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ? AND word_id = ?", userID, word.ID).First(&resp).Error
	})
	if err != nil {
		return err
	}
	return c.JSON(&resp)
}

// --- Reklam kapısı (sözleşme K4) ---

// gateReporter, kapı ihlallerini loglar ve Sentry'ye bildirir; testler
// değiştirebilir.
var gateReporter = services.NewGateReporter()

// Kapı reddinin yanıtları. İstemci kodu görünce kullanım durumunu
// (/users/me/usage) yeniden çeker ve ilgili kapıyı gösterir; mesaj
// kullanıcıya gösterilmez.
var (
	errGateRequired = apiErr(fiber.StatusForbidden, "gate_required",
		"bu grup bugün açılmadı; devam etmek için grubu açman gerekiyor")
	errReviewGateRequired = apiErr(fiber.StatusForbidden, "review_gate_required",
		"tekrara devam etmek için önce reklam kapısını geçmen gerekiyor")
	// Açık grup sınırı (services.MaxOpenGroups) doluyken başlanmamış bir grup
	// açılmak istendi. Reklam kapısı değildir: istemci açık grupları
	// (/map/open-groups) gösterip önce onların bitirilmesini ister.
	errOpenGroupLimit = apiErr(fiber.StatusForbidden, "open_group_limit",
		"yeni bir grup açmak için önce açık gruplarından birini bitir")
)

// gateMode, GATE_ENFORCEMENT kipidir (boşsa varsayılan).
func (h *Handlers) gateMode() config.GateEnforcement {
	if h.Cfg == nil {
		return config.DefaultGateEnforcement
	}
	return services.NormalizeGateMode(h.Cfg.GateEnforcement)
}

// applyGate, bir kapı kararını kipe göre uygular: izin verildiyse ya da kip
// off ise nil; log kipinde ihlali raporlar ve nil; enforce kipinde raporlar
// ve rejection'ı döner (çağıranın transaction'ı geri alınır).
func (h *Handlers) applyGate(c *fiber.Ctx, v services.GateViolation, allowed bool, now time.Time, rejection error) error {
	mode := h.gateMode()
	action := services.GateActionFor(mode, allowed)
	if action == services.GateAllow {
		return nil
	}
	v.Mode = mode
	v.RequestID = observability.RequestIDFrom(c)
	v.AppVersion = observability.AppVersionFrom(c)
	gateReporter.Report(v, now)
	if action == services.GateReject {
		return rejection
	}
	return nil
}

// learnMapState, öğrenme yazımından önce okunan harita durumudur (bkz.
// learnMapLocked).
type learnMapState struct {
	// Grup haritada sıralı kilitle ya da önceki seviye bitmediği için
	// kilitli (açık grup sınırı burada sayılmaz; o karar transaction içinde
	// verilir, bkz. learnGate).
	locked bool
	// Grupta yazımdan önce ilerleme kaydı vardı. Kayıtlar silinmediği için
	// (hesap silme dışında) transaction'da da başlanmış kalır.
	started bool
}

// learnMapLocked, kelimenin grubunun haritadaki durumunu döner (bkz.
// groupLockReason). Kapı kapalıysa ya da kelimenin zaten ilerlemesi varsa
// (yazım olmayacak) hiç bakılmaz.
//
// Transaction'dan önce çalışır: sıralı kilit ve önceki seviye yalnızca açılma
// yönünde değişir (görülme ve tamamlanma kalıcıdır), yazımdan önce bakmak
// yarış açmaz; transaction içinde h.DB'yi kullanmak ise havuzdan ikinci bir
// bağlantı bekletirdi. Açık grup sınırı ise başka bir grubun başlatılmasıyla
// kapanabilir; o yüzden learnGate'te kilit altında yeniden sorulur.
func (h *Handlers) learnMapLocked(userID string, word *models.DictionaryWord) (learnMapState, error) {
	if h.gateMode() == config.GateOff {
		return learnMapState{}, nil
	}
	var existing []models.WordProgress
	if err := h.DB.Select("box").Where("user_id = ? AND word_id = ?", userID, word.ID).
		Limit(1).Find(&existing).Error; err != nil {
		return learnMapState{}, err
	}
	if len(existing) > 0 && existing[0].Box >= 2 {
		return learnMapState{started: true}, nil
	}
	reason, started, err := h.groupLockReason(userID, word.Level, word.Grp)
	if err != nil {
		return learnMapState{}, err
	}
	locked := reason == services.LockReasonPreviousLevel || reason == services.LockReasonPreviousNotSeen
	return learnMapState{locked: locked, started: started}, nil
}

// learnGate, kelimenin öğrenme yazımının kapısını tx içinde denetler.
// mapState.locked, grubun haritada kilitli olduğunu söyler (learnMapLocked);
// bu da ihlaldir (neden map_locked, kod yine gate_required).
//
// Başlanmamış grubun ilk yazımında açık grup sınırı da sorulur: sınır
// doluysa ihlal open_group_limit'tir, enforce'ta 403 open_group_limit döner.
// Bu ihlal diğerlerinden önce gelir: reklam izlemek onu gidermez, istemci
// kullanıcıya reklam göstermemelidir. Sınır kullanıcı başına bir advisory
// lock altında sayılır: iki farklı yeni gruba aynı anda gelen yazımlar
// sınırı birlikte aşamaz.
//
// Kapı yalnızca YENİ yazımda sorulur: kelimenin zaten ilerlemesi varsa
// (kutu 2+) learn isteği hiçbir şey yazmaz (yinelenen istek, ikinci cihaz,
// bayat deste) ve denetlenmez. TEST_MODE_UNLOCK_ALL kapıyı kapatır.
//
// Kilit sırası Swipe ile aynıdır (önce kelime satırı, sonra kullanıcı satırı);
// aynı kelimeye eşzamanlı learn ve swipe birbirini kilitlenmede bekletmez.
// Kullanıcı satırı paylaşımlı kilitlenir: aynı kullanıcının learn'leri
// birbirini beklemez, ama karar ile yazım arasında "bugün açılan gruplar"
// değişemez.
func (h *Handlers) learnGate(c *fiber.Ctx, tx *gorm.DB, userID string, word *models.DictionaryWord, mapState learnMapState, now time.Time) error {
	if h.gateMode() == config.GateOff || h.unlockAll() {
		return nil
	}

	var existing []models.WordProgress
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ? AND word_id = ?", userID, word.ID).Limit(1).Find(&existing).Error; err != nil {
		return err
	}
	if len(existing) > 0 && existing[0].Box >= 2 {
		return nil
	}

	var user models.User
	if err := findUser(tx.Clauses(clause.Locking{Strength: "SHARE"}), userID, &user); err != nil {
		return err
	}

	if !mapState.started {
		// Kilit transaction sonunda bırakılır; aynı kullanıcının başka bir
		// yeni gruba gelen yazımı, bunun commit'ini görerek sayar.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?::text))", "open_groups:"+userID).Error; err != nil {
			return err
		}
		limited, err := h.openGroupLimitReached(tx, userID, word.Level, word.Grp)
		if err != nil {
			return err
		}
		if limited {
			return h.applyGate(c, services.GateViolation{
				Kind:   services.GateKindLearn,
				Reason: services.GateReasonOpenGroupLimit,
				UserID: userID,
				WordID: word.ID,
				Level:  word.Level,
				Group:  word.Grp,
			}, false, now, errOpenGroupLimit)
		}
	}
	return h.dailyLearnGate(c, tx, &user, word, mapState.locked, now)
}

// dailyLearnGate, kelimenin grubunun bugün açılıp açılmadığını (ücretsiz hak,
// reklam ya da gövdesiz eski kayıt) user'ın günlük durumuyla denetler.
// mapLocked, haritadaki kilidi de ihlal sayar (neden map_locked). user,
// çağıranın tx'inde en az paylaşımlı kilitli okunmuş olmalıdır.
func (h *Handlers) dailyLearnGate(c *fiber.Ctx, tx *gorm.DB, user *models.User, word *models.DictionaryWord, mapLocked bool, now time.Time) error {
	userID := user.ID
	state := services.GateStateAt(user, now)
	key, ok := services.UnlockedGroupKey(word.Level, word.Grp)
	allowed := ok && services.LearnAllowedToday(state, key)
	if !allowed && ok {
		// Eski istemci grubu gövdesiz kaydetmiş olabilir: hangi grubu açtığı
		// bilinmez, yalnızca kaç grup açtığı bilinir.
		if slots := services.KeylessUnlockSlots(state); slots > 0 {
			// Kullanıcı satırı paylaşımlı kilitli: iki farklı gruba aynı
			// anda gelen learn'ler "bugün öğrenilen gruplar"ı birlikte boş
			// görüp tek hakkı ikisi de kullanırdı. Bu dal kullanıcı başına
			// sıraya girer; sonraki sorgu (READ COMMITTED) öncekinin
			// yazdığı kelimeyi görür. Kilit transaction sonunda bırakılır;
			// satır kilidi yükseltilmediği için kilitlenme (deadlock) olmaz.
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?::text))", "gate_keyless:"+userID).Error; err != nil {
				return err
			}
			learned, err := groupsLearnedToday(tx, userID, now)
			if err != nil {
				return err
			}
			allowed = services.KeylessSlotCovers(slots, learned, state.UnlockedGroups, key)
		}
	}
	// Bir istek tek ihlal satırı üretir; bugün açılmamış grup önce gelir.
	reason := services.LearnDenialReason(state, key)
	if allowed && mapLocked {
		allowed, reason = false, services.GateReasonMapLocked
	}
	return h.applyGate(c, services.GateViolation{
		Kind:   services.GateKindLearn,
		Reason: reason,
		UserID: userID,
		WordID: word.ID,
		Level:  word.Level,
		Group:  word.Grp,
	}, allowed, now, errGateRequired)
}

// groupsLearnedToday, kullanıcının bugün (kullanım günü) yeni kelime
// öğrendiği grupların anahtarlarıdır.
func groupsLearnedToday(tx *gorm.DB, userID string, now time.Time) ([]string, error) {
	var rows []struct {
		Level string
		Grp   int
	}
	if err := tx.Model(&models.WordProgress{}).Distinct("level", "grp").
		Where("user_id = ? AND created_at >= ?", userID, services.UsageDayStart(now)).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		if k, ok := services.UnlockedGroupKey(r.Level, r.Grp); ok {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// reviewGate, bir tekrar cevabının (swipe öncesi kutu 2..5, vadesi gelmiş)
// kapısını tx içinde denetler. Kullanıcı satırı sayaç artırımına kadar
// kilitli kalır: eşzamanlı cevaplar sırayla karar verir ve kontrol noktasını
// birlikte aşamaz.
func (h *Handlers) reviewGate(c *fiber.Ctx, tx *gorm.DB, userID string, now time.Time) error {
	if h.gateMode() == config.GateOff {
		return nil
	}
	var user models.User
	if err := lockUser(tx, userID, &user); err != nil {
		return err
	}
	state := services.GateStateAt(&user, now)
	return h.applyGate(c, services.GateViolation{
		Kind:           services.GateKindReview,
		Reason:         services.GateReasonReviewCheckpoint,
		UserID:         userID,
		ReviewAnswers:  state.ReviewAnswers,
		ReviewAdPasses: state.ReviewAdPasses,
	}, services.ReviewAnswerAllowed(state.ReviewAnswers, state.ReviewAdPasses), now, errReviewGateRequired)
}

// boxOneLearnGate, kutu 1'deki kelimenin sağa kaydırılmasının (öğrenme)
// günlük grup kapısıdır; wp, tx'te kilitli okunmuş satırdır. Grup anahtarı
// kayıttaki seviye/gruptur (learn bunları sözlükle eşitler).
func (h *Handlers) boxOneLearnGate(c *fiber.Ctx, tx *gorm.DB, wp *models.WordProgress, now time.Time) error {
	if h.gateMode() == config.GateOff || h.unlockAll() {
		return nil
	}
	var user models.User
	if err := findUser(tx.Clauses(clause.Locking{Strength: "SHARE"}), wp.UserID, &user); err != nil {
		return err
	}
	word := models.DictionaryWord{ID: wp.WordID, Level: wp.Level, Grp: wp.Grp}
	return h.dailyLearnGate(c, tx, &user, &word, false, now)
}

// MarkUnknown: POST /progress/unknown {wordId} — öğrenme ekranında kaydı
// olmayan kelimeye "Bilmiyorum" denildi. Kelime kutu 1'de kaydedilir: grup
// tamamlanmaz (kutu 1 "kalan"dır) ama kelime görülmüş sayılır; grubun bütün
// kelimeleri görülünce seviyedeki sonraki grup açılabilir (bkz.
// services.GroupLockReason). Kutu 1 kaydı tekrar listelerine girmez, öğrenilen
// sayılara katılmaz.
//
// Kelimenin zaten kaydı varsa (kutu fark etmez) hiçbir şey değişmez ve mevcut
// kayıt döner: kayıtlı kelimenin "Bilmiyorum"u POST /progress/swipe (left)
// ile gönderilir; bu uç ilerlemiş bir kelimeyi kutu 1'e geri atmaz. İstek
// yinelenebilir.
//
// Yeni kayıt learn ile aynı kapıdan geçer (bkz. learnGate): grup bugün
// açılmamışsa ya da haritada kilitliyse 403 gate_required, açık grup sınırı
// doluyken başlanmamış grupta 403 open_group_limit (enforce kipinde). Yanıt
// learn'ünkiyle aynı biçimde kelime ilerlemesidir.
//
// Eski sunucuda bu uç yoktur (404); istemci bunu "kaydedilmedi" sayıp akışı
// sürdürebilir.
func (h *Handlers) MarkUnknown(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var req dto.UnknownWordRequest
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

	var existing []models.WordProgress
	if err := h.DB.Where("user_id = ? AND word_id = ?", userID, word.ID).Limit(1).Find(&existing).Error; err != nil {
		return err
	}
	if len(existing) > 0 {
		return c.JSON(&existing[0])
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
	now := time.Now()
	services.OnLeftSwipe(&wp, now)

	mapState, err := h.learnMapLocked(userID, &word)
	if err != nil {
		return err
	}
	var resp models.WordProgress
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		if err := h.learnGate(c, tx, userID, &word, mapState, now); err != nil {
			return err
		}
		// Arada başka bir istek (learn ya da yinelenen unknown) kaydı
		// yazdıysa ona dokunulmaz.
		res := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "word_id"}},
			DoNothing: true,
		}).Create(&wp)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			resp = wp
			return nil
		}
		return tx.Where("user_id = ? AND word_id = ?", userID, word.ID).First(&resp).Error
	})
	if err != nil {
		return err
	}
	return c.JSON(&resp)
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
//
// Sayılacak tekrar cevabı reklam kapısından geçer (sözleşme K4, bkz.
// reviewGate): günün cevapları geçilmemiş bir kontrol noktasına (30/60/90)
// dayanmışsa enforce'ta 403 review_gate_required döner ve hiçbir şey yazılmaz.
// No-op swipe kapıya sorulmaz.
//
// Kutu 1'deki kelimeye "Biliyorum" (sağ) kelimeyi kutu 2'ye alır: bu bir
// öğrenmedir ve learn gibi günlük grup kapısından geçer (bkz.
// dailyLearnGate; enforce'ta 403 gate_required). Kutu 1 kaydı "Bilmiyorum"
// ile (POST /progress/unknown) yazılabildiği için, aksi halde grubu bir kez
// açıp bütün kelimelerine "Bilmiyorum" diyen istemci sonraki günlerde grubu
// açmadan öğrenebilirdi. Grup başlanmış olduğundan harita kilidi ve açık grup
// sınırı sorulmaz. Kutu 1'de "Bilmiyorum" (sol) kapıya sorulmaz.
func (h *Handlers) Swipe(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var req dto.SwipeRequest
	if err := c.BodyParser(&req); err != nil || req.WordID == "" {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "wordId gerekli")
	}
	var apply func(*models.WordProgress, time.Time)
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
		// bakılırsa tekrar cevabı gibi görünür). Öğrenme swipe'ı (kutu 1)
		// tekrar kapısına hiç girmez.
		preBox := wp.Box
		if preBox <= 1 && req.Direction == "right" {
			if err := h.boxOneLearnGate(c, tx, &wp, now); err != nil {
				return err
			}
		}
		countsAsReview := services.CountsAsReviewAnswer(preBox)
		if countsAsReview {
			if err := h.reviewGate(c, tx, userID, now); err != nil {
				return err
			}
		}
		apply(&wp, now)
		if err := tx.Save(&wp).Error; err != nil {
			return err
		}
		if !countsAsReview {
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

// maxReviewListLimit, tekrar listesinde ?limit ile istenebilecek en çok kelime.
const maxReviewListLimit = 200

// reviewBox, kullanıcının bir kutusundaki kelime ilerlemeleridir.
func reviewBox(userID string, box int) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Model(&models.WordProgress{}).Where("user_id = ? AND box = ?", userID, box)
	}
}

// dueForReview, "bugün tekrar edilecek" kümesinin tek tanımıdır: sonraki
// tekrar tarihi yok ya da bugünün bitişinden (end, hariç) önce. Liste ucu ve
// /counts'taki today aynı kümeyi bu koşulla sayar; swipe vadesi
// (services.AcceptsSwipe) de aynı kuraldır.
func dueForReview(end time.Time) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("(next_review_date IS NULL OR next_review_date < ?)", end)
	}
}

// reviewDay, tekrar uçlarının "bugün"ünü çözer: ?date=YYYY-MM-DD verilmişse o
// takvim günü, yoksa şimdi; ikisi de kullanım saat diliminde
// (services.UsageLocation). Çözülemeyen tarih "şimdi" sayılır.
//
// next_review_date bir timestamptz'dir (mutlak an): sınırları burada saat
// dilimiyle hesaplayıp anla karşılaştırmak, oturumun ya da sürecin saat
// diliminden etkilenmez.
func reviewDay(dateParam string) (start, end time.Time) {
	if dateParam != "" {
		if d, err := time.ParseInLocation("2006-01-02", dateParam, services.UsageLocation()); err == nil {
			return reviewDayBounds(d)
		}
	}
	return reviewDayBounds(time.Now())
}

// ReviewWordsForBox: GET /progress/review/box/:box?date=YYYY-MM-DD&limit=N
// Bugün vadesi gelmiş kelimeler (bkz. dueForReview), sabit sırayla: önce
// tarihsizler, sonra en eski vade; eşitlikte eklenme sırası. limit (1..200)
// verilmezse liste sınırsızdır (eski istemciler).
func (h *Handlers) ReviewWordsForBox(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	box, _ := strconv.Atoi(c.Params("box"))
	limit := -1
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxReviewListLimit {
			return apiErr(fiber.StatusBadRequest, "invalid_limit",
				fmt.Sprintf("limit 1 ile %d arasında olmalı", maxReviewListLimit))
		}
		limit = n
	}
	_, end := reviewDay(c.Query("date"))

	list := []models.WordProgress{}
	if err := h.DB.Scopes(reviewBox(userID, box), dueForReview(end)).
		Order("next_review_date ASC NULLS FIRST").Order("id").
		Limit(limit).Find(&list).Error; err != nil {
		return err
	}
	return c.JSON(list)
}

// UpcomingCountsForBox: GET /progress/review/box/:box/counts?date=YYYY-MM-DD
// user_word_repository.getUpcomingCountsForBox portu (0..30 gün dağılımı + en yoğun gün).
// Gün farkları kullanım saat diliminin takvim günleriyle hesaplanır; 0. gün
// (today) liste ucuyla aynı koşuldan (dueForReview) sayılır.
func (h *Handlers) UpcomingCountsForBox(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	box, _ := strconv.Atoi(c.Params("box"))
	start, end := reviewDay(c.Query("date"))

	var today int64
	if err := h.DB.Scopes(reviewBox(userID, box), dueForReview(end)).Count(&today).Error; err != nil {
		return err
	}
	// 1..30. günler: [end, start+31 gün) aralığındaki vadeler.
	var upcoming []time.Time
	if err := h.DB.Scopes(reviewBox(userID, box)).
		Where("next_review_date >= ? AND next_review_date < ?", end, start.AddDate(0, 0, 31)).
		Pluck("next_review_date", &upcoming).Error; err != nil {
		return err
	}

	dailyCounts := map[int]int{}
	if today > 0 {
		dailyCounts[0] = int(today)
	}
	totalMonth := int(today)
	for _, next := range upcoming {
		dailyCounts[usageDaysBetween(start, next)]++
		totalMonth++
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
		Today:           int(today),
		Tomorrow:        dailyCounts[1],
		Later:           later,
		DailyCounts:     dcStr,
		TotalMonthCount: totalMonth,
		BusiestDayLabel: busiestLabel,
		BusiestDayCount: maxCount,
	})
}
