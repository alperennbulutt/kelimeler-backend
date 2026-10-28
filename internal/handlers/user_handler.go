package handlers

import (
	"errors"
	"slices"
	"time"
	"unicode/utf8"

	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UpdateProfile: PATCH /users/me {displayName?}
func (h *Handlers) UpdateProfile(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var req dto.UpdateProfileRequest
	if err := c.BodyParser(&req); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "geçersiz istek")
	}
	if req.DisplayName != nil {
		name, err := dto.NormalizeDisplayName(*req.DisplayName)
		if err != nil {
			return apiErr(fiber.StatusBadRequest, "invalid_display_name", err.Error())
		}
		if err := h.DB.Model(&models.User{}).Where("id = ?", userID).Update("display_name", name).Error; err != nil {
			return err
		}
	}
	return h.respondMe(c, userID)
}

// current-group gövdesindeki serbest metin ve sayıların sınırları.
const (
	maxGroupTitleLen       = 120
	maxGroupDescriptionLen = 500
	maxGroupTotalWords     = 500
)

// errInvalidGroup, seviye/grup doğrulamasının ortak hatasıdır.
var errInvalidGroup = apiErr(fiber.StatusBadRequest, "invalid_group", "geçersiz seviye ya da grup")

// findUser, kullanıcıyı okur: satır yoksa 404 user_not_found, diğer
// veritabanı hataları olduğu gibi (500) döner. Bağlantı hatasını "hesap yok"
// diye bildirmek istemciyi yanlış yönlendirir.
func findUser(db *gorm.DB, userID string, user *models.User) error {
	err := db.First(user, "id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apiErr(fiber.StatusNotFound, "user_not_found", "kullanıcı bulunamadı")
	}
	return err
}

// lockUser, kullanıcı satırını transaction sonuna kadar kilitleyerek okur.
func lockUser(tx *gorm.DB, userID string, user *models.User) error {
	return findUser(tx.Clauses(clause.Locking{Strength: "UPDATE"}), userID, user)
}

// respondMe, güncel kullanıcıyı bugünkü {"user": ...} şeklinde döner.
func (h *Handlers) respondMe(c *fiber.Ctx, userID string) error {
	var user models.User
	if err := findUser(h.DB, userID, &user); err != nil {
		return err
	}
	u, err := h.userJSON(&user)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"user": u})
}

// userJSON, /users/me uçlarının bugünkü yanıt şeklidir ({"user": ...}).
//
// Bağlı giriş yöntemleri okunamazsa hata döner (500). linkedProviders gibi
// boş listeye düşmek, istemciye "hiçbir yöntem bağlı değil" dedirtir: hesap
// bağlama/ayırma ekranı yanlış seçenek gösterir.
func (h *Handlers) userJSON(user *models.User) (*dto.UserDTO, error) {
	providers, err := h.loadLinkedProviders(user.ID)
	if err != nil {
		return nil, err
	}
	return dto.NewUserDTO(user, providers), nil
}

// txWithUserResponse, fn'i bir transaction'da çalıştırır ve fn'in okuduğu
// user'ın yanıt gövdesini döner.
//
// Bağlı giriş yöntemleri transaction'dan ÖNCE okunur: okunamazsa hiçbir şey
// yazılmadan 500 döner. Yazımdan sonra okunup hata verseydi istemci 500 görüp
// yeniden denerken yazım çoktan kalıcı olurdu (gövdesiz advance-group'ta
// işaretçi iki kez ilerlerdi). Transaction içinde h.DB ile okumak ise
// bağlantı havuzunda ikinci bir bağlantı bekletirdi.
func (h *Handlers) txWithUserResponse(userID string, user *models.User, fn func(tx *gorm.DB) error) (*dto.UserDTO, error) {
	providers, err := h.loadLinkedProviders(userID)
	if err != nil {
		return nil, err
	}
	if err := h.DB.Transaction(fn); err != nil {
		return nil, err
	}
	return dto.NewUserDTO(user, providers), nil
}

// SetCurrentGroup: PUT /users/me/current-group — user_profile_repository.setCurrentGroup.
//
// level A1..C2 olmalı, group 1 ile seviyedeki grup sayısı arasında olmalıdır;
// aksi halde 400 invalid_group. İşaretçi yalnızca İLERİ yazılır: hedef mevcut
// konumun gerisindeyse hiçbir alan değişmez ve güncel kullanıcı 200 ile döner
// (eski bir gruba dönmek "kaldığın yer"i geri almaz).
//
// İleri yazım, açık grup sınırı (services.MaxOpenGroups) doluyken
// başlanmamış bir gruba yapılamaz: 403 open_group_limit döner ve hiçbir alan
// değişmez. Dürüst istemci haritada kilitli gruba girmez; istemci bu ucun
// hatasını yok sayar (işaretçi yalnızca "kaldığın yer"dir).
func (h *Handlers) SetCurrentGroup(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var req dto.SetCurrentGroupRequest
	if err := c.BodyParser(&req); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "geçersiz istek")
	}
	if services.IndexOfLevel(req.Level) < 0 || req.Group < 1 {
		return errInvalidGroup
	}
	if (req.Title != nil && utf8.RuneCountInString(*req.Title) > maxGroupTitleLen) ||
		(req.Description != nil && utf8.RuneCountInString(*req.Description) > maxGroupDescriptionLen) ||
		(req.TotalWords != nil && (*req.TotalWords < 1 || *req.TotalWords > maxGroupTotalWords)) {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "başlık, açıklama ya da kelime sayısı sınır dışında")
	}

	var user models.User
	u, err := h.txWithUserResponse(userID, &user, func(tx *gorm.DB) error {
		maxGroup, err := maxGroupForLevel(tx, req.Level)
		if err != nil {
			return err
		}
		if req.Group > maxGroup {
			return errInvalidGroup
		}
		if err := lockUser(tx, userID, &user); err != nil {
			return err
		}
		switch services.CompareGroupPositions(req.Level, req.Group, user.CurrentLevel, user.CurrentGroup) {
		case -1:
			return nil
		case 1:
			limited, err := h.openGroupLimitReached(tx, userID, req.Level, req.Group)
			if err != nil {
				return err
			}
			if limited {
				return errOpenGroupLimit
			}
		}

		updates := map[string]any{
			"current_level": req.Level,
			"current_group": req.Group,
		}
		if req.Title != nil {
			updates["current_group_title"] = *req.Title
		}
		if req.Description != nil {
			updates["current_group_description"] = *req.Description
		}
		if req.TotalWords != nil {
			updates["current_group_total_words"] = *req.TotalWords
		}
		if err := tx.Model(&models.User{}).Where("id = ?", userID).Updates(updates).Error; err != nil {
			return err
		}
		return tx.First(&user, "id = ?", userID).Error
	})
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"user": u})
}

// AdvanceGroup: POST /users/me/advance-group — sonraki level/grup'u sunucu hesaplar.
//
// İsteğe bağlı gövde {"level","group"}, istemcinin tamamladığı ya da bütün
// kelimelerini gördüğü (bkz. levelGroupProgress.seen) gruptur: işaretçi tam o
// gruptaysa bir ileri gider, değilse (zaten ileride ya da geride) hiçbir şey
// değişmez. Yanıtı kaybolan isteğin yeniden denenmesi bu yüzden grup
// atlatmaz.
//
// Gövdesiz istek (yayındaki eski istemci) işaretçiyi yalnızca işaretçinin
// gösterdiği grup haritada tamamlanmışsa (bkz. groupCompleted) bir ileri
// götürür; değilse hiçbir şey değişmez ve güncel kullanıcı aynı şekilde 200
// ile döner. Eski istemci tur sonunda, sola kaydırılan ("Bilmiyorum")
// kelimeler kutu 1'de kalsa da ve geride bir grubu tekrarlarken de
// (current-group geri yazmaz) gövdesiz advance çağırır; koşulsuz ilerleme
// işaretçiyi her turda bir grup, seviye sonunda bir seviye ileri kaydırırdı.
//
// İki yolda da işaretçi, açık grup sınırı (services.MaxOpenGroups) doluyken
// başlanmamış bir gruba taşınmaz: hiçbir şey değişmez ve yanıtın
// blockedReason alanı "open_group_limit" olur (ilerlediğinde ya da başka bir
// nedenle değişmediğinde ""). Sınır açılınca (açık gruplardan biri
// tamamlanınca) aynı istek işaretçiyi ilerletir.
//
// İki yolda da kullanıcı satırı kilitlenir: eşzamanlı istekler aynı işaretçiyi
// okuyup birbirinin yazdığını ezmez. C2'nin son grubu bitince işaretçi
// "tümü tamamlandı"da (C2, grup 0) kalır, başa sarılmaz.
func (h *Handlers) AdvanceGroup(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var completed *dto.AdvanceGroupRequest
	if len(c.Body()) > 0 {
		var req dto.AdvanceGroupRequest
		if err := c.BodyParser(&req); err != nil {
			return apiErr(fiber.StatusBadRequest, "invalid_request", "geçersiz istek")
		}
		if req.Level != "" {
			completed = &req
		}
	}
	if completed == nil {
		if err := h.awaitPointerGroupCompleted(userID); err != nil {
			return err
		}
	}

	var user models.User
	levelChanged := false
	blockedReason := ""
	u, err := h.txWithUserResponse(userID, &user, func(tx *gorm.DB) error {
		if err := lockUser(tx, userID, &user); err != nil {
			return err
		}
		if completed != nil && (completed.Level != user.CurrentLevel || completed.Group != user.CurrentGroup) {
			return nil
		}
		if completed == nil {
			// Karar kilitli satırdaki işaretçiyle verilir: bekleme sırasında
			// başka bir istek işaretçiyi ilerletmiş olabilir.
			done, err := groupCompleted(tx, userID, user.CurrentLevel, user.CurrentGroup)
			if err != nil || !done {
				return err
			}
		}

		maxGroup, err := maxGroupForLevel(tx, user.CurrentLevel)
		if err != nil {
			return err
		}
		nextLevel, nextGroup, changed := services.AdvanceGroup(user.CurrentLevel, user.CurrentGroup, maxGroup)
		if nextLevel == user.CurrentLevel && nextGroup == user.CurrentGroup {
			return nil
		}
		if nextGroup >= 1 {
			limited, err := h.openGroupLimitReached(tx, userID, nextLevel, nextGroup)
			if err != nil {
				return err
			}
			if limited {
				blockedReason = services.LockReasonOpenGroupLimit
				return nil
			}
		}
		if err := tx.Model(&models.User{}).Where("id = ?", userID).
			Updates(map[string]any{"current_level": nextLevel, "current_group": nextGroup}).Error; err != nil {
			return err
		}
		levelChanged = changed
		return tx.First(&user, "id = ?", userID).Error
	})
	if err != nil {
		return err
	}

	return c.JSON(fiber.Map{
		"user":         u,
		"levelChanged": levelChanged,
		"newLevel":     user.CurrentLevel,
		"newGroup":     user.CurrentGroup,
		// Sonradan eklendi: eski istemci tanımaz ve yok sayar.
		"blockedReason": blockedReason,
	})
}

// Gövdesiz advance-group'un, son kelimenin yazımını bekleme süresi ve
// yoklama aralığı; testler değiştirebilir.
//
// Eski istemci grubun son kartı kaydırılınca learn/swipe kaydını beklemeden
// advance-group'u da gönderir; iki istek sunucuya ters sırada varabilir.
// İşaretçideki grup tamamlanmamış görünürse karar, havadaki yazımın commit
// olması için bu süre boyunca yeniden sorulur.
var (
	legacyAdvanceSettle       = time.Second
	legacyAdvancePollInterval = 100 * time.Millisecond
)

// awaitPointerGroupCompleted, işaretçinin gösterdiği grup tamamlanana ya da
// legacyAdvanceSettle dolana kadar bekler. Asıl karar kilitli satırla
// AdvanceGroup'un transaction'ında verilir; bu bekleme transaction'ın dışında
// ve kilitsizdir (kilit tutarak beklemek, beklenen learn'ün kullanıcı satırı
// kilidini bekletirdi).
func (h *Handlers) awaitPointerGroupCompleted(userID string) error {
	deadline := time.Now().Add(legacyAdvanceSettle)
	for {
		var user models.User
		if err := findUser(h.DB, userID, &user); err != nil {
			return err
		}
		if services.IndexOfLevel(user.CurrentLevel) < 0 || services.IsAllLevelsCompleted(user.CurrentLevel, user.CurrentGroup) {
			return nil
		}
		done, err := groupCompleted(h.DB, userID, user.CurrentLevel, user.CurrentGroup)
		if err != nil || done || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(legacyAdvancePollInterval)
	}
}

// --- Günlük kullanım ---

func usageDTO(u *models.User) dto.DailyUsageDTO {
	remaining := services.MaxNoFillPassesPerDay - u.NoFillPassesToday
	if remaining < 0 {
		remaining = 0
	}
	return dto.DailyUsageDTO{
		GroupsStartedTodayCount: u.GroupsStartedTodayCount,
		GroupsUnlockedByAdToday: u.GroupsUnlockedByAdToday,
		ReviewAdPassCountToday:  u.ReviewAdPassCountToday,
		ReviewAnswersToday:      u.ReviewAnswersToday,
		UnlockedGroupsToday:     services.SplitUnlockedGroups(u.UnlockedGroupsToday),
		NoFillPassesToday:       u.NoFillPassesToday,
		NoFillPassesRemaining:   remaining,
		LastResetDate:           u.LastResetDate,
	}
}

// respondUsage, kullanıcının güncel günlük kullanım durumunu döner.
func (h *Handlers) respondUsage(c *fiber.Ctx, userID string) error {
	var user models.User
	if err := findUser(h.DB, userID, &user); err != nil {
		return err
	}
	return c.JSON(usageDTO(&user))
}

// usageResetValues, gün dönümünde sıfırlanan kolonların tek kaynağıdır.
// Yeni bir günlük sayaç buraya eklenmezse hiç sıfırlanmaz ve kullanıcı ertesi
// gün dünkü kotayla karşılaşır.
func usageResetValues(now time.Time) map[string]any {
	return map[string]any{
		"groups_started_today_count":  0,
		"groups_unlocked_by_ad_today": 0,
		"review_ad_pass_count_today":  0,
		"no_fill_passes_today":        0,
		"review_answers_today":        0,
		"unlocked_groups_today":       "",
		"last_reset_date":             now.UnixMilli(),
	}
}

// resetUsageIfStale, last_reset_date now'ın kullanım gününden önceyse günlük
// sayaçları sıfırlar; aynı gün içindeyse hiçbir şey yapmaz.
//
// "Gün değişti mi" koşulu UPDATE'in içindedir. Ayrı bir SELECT ile bakılıp
// koşulsuz sıfırlansaydı, bayat değeri aynı anda okuyan iki istekten ikincisi
// birincinin artırımını silerdi. Burada ikinci istek satır kilidini bekler,
// koşul güncel satırda yeniden değerlendirilir ve satıra dokunulmaz.
func resetUsageIfStale(tx *gorm.DB, userID string, now time.Time) error {
	return tx.Model(&models.User{}).
		Where("id = ? AND last_reset_date < ?", userID, services.UsageDayStart(now).UnixMilli()).
		Updates(usageResetValues(now)).Error
}

// usageTouchedAt, sayaç artıran UPDATE'lerin last_reset_date'e yazdığı ifadedir.
//
// Tarih yalnızca ileri gider. Gece yarısından hemen önce başlayan bir istek,
// yeni günün sıfırlamasından sonra commit olabilir; kendi (dünkü) "now" değerini
// düz yazsaydı tarihi düne çeker, sonraki istek günü ikinci kez sıfırlar ve
// arada yazılan artırımlar silinirdi.
func usageTouchedAt(now time.Time) clause.Expr {
	return gorm.Expr("GREATEST(last_reset_date, ?)", now.UnixMilli())
}

func (h *Handlers) GetDailyUsage(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	if err := resetUsageIfStale(h.DB, userID, time.Now()); err != nil {
		return err
	}
	return h.respondUsage(c, userID)
}

// ResetDailyUsage: POST /users/me/usage/reset
func (h *Handlers) ResetDailyUsage(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	if err := h.DB.Model(&models.User{}).Where("id = ?", userID).Updates(usageResetValues(time.Now())).Error; err != nil {
		return err
	}
	return h.respondUsage(c, userID)
}

// incrementUsage, belirtilen sayaç kolonunu atomik artırır (gün değiştiyse önce sıfırlar).
func (h *Handlers) incrementUsage(c *fiber.Ctx, column string) error {
	userID := middleware.UserID(c)
	now := time.Now()
	if err := resetUsageIfStale(h.DB, userID, now); err != nil {
		return err
	}

	// İstemci "kayıt yazılamadıysa içeriği açma" kararını bu yanıta dayandırır;
	// yazılamayan bir artırım başarı gibi dönmemelidir.
	res := h.DB.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
		column:            gorm.Expr(column + " + 1"),
		"last_reset_date": usageTouchedAt(now),
	})
	if res.Error != nil {
		return res.Error
	}
	return h.respondUsage(c, userID)
}

// freeGroupColumn, günün ücretsiz grup açılışının sayacıdır
// (POST /users/me/usage/group-started).
const freeGroupColumn = "groups_started_today_count"

// UnlockGroup: POST /users/me/usage/group-started ve /group-unlocked-by-ad
// Gövde (isteğe bağlı): {"level":"A1","group":3}
//
// Grup "bugün açılanlar" kümesinde değilse kümeye eklenir ve sayaç bir artar;
// zaten kümedeyse hiçbir şey değişmez. Böylece yanıtı kaybolan bir isteği
// yeniden denemek ya da aynı gruba gün içinde tekrar girmek, hakkı ikinci kez
// tüketmez.
//
// Ücretsiz açılış (group-started) günde birdir: bugün zaten ücretsiz bir grup
// açılmışsa (groups_started_today_count ≥ 1) ve grup kümede değilse bu bir
// kapı ihlalidir (neden free_group_reused). GATE_ENFORCEMENT=off'ta kabul
// edilir, log'da kabul edilip raporlanır, enforce'ta 403 gate_required döner
// ve hiçbir şey yazılmaz. Dürüst istemci group-started'ı yalnızca sayaç 0
// iken çağırır; sonraki gruplar reklamla (group-unlocked-by-ad) açılır.
//
// Kullanıcı satırı karar ile yazım arasında kilitlidir: farklı iki gruba
// aynı anda gelen ücretsiz açılışlardan yalnızca biri ücretsiz sayılır.
//
// Açık grup sınırı (services.MaxOpenGroups) doluyken başlanmamış bir grubun
// açılışı da ihlaldir (neden open_group_limit; ücretsiz hak ihlalinden önce
// gelir): log'da kabul edilip raporlanır, enforce'ta 403 open_group_limit
// döner ve hiçbir şey yazılmaz, yani hak ya da reklam sayacı yanmaz. Dürüst
// istemci haritada kilitli gruba girmez.
//
// Gövde göndermeyen (ya da geçersiz gönderen) istemcilerde uç yalnızca sayacı
// artırır.
func (h *Handlers) UnlockGroup(column string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req dto.UnlockGroupRequest
		if err := c.BodyParser(&req); err != nil {
			return h.incrementUsage(c, column)
		}
		key, ok := services.UnlockedGroupKey(req.Level, req.Group)
		if !ok {
			return h.incrementUsage(c, column)
		}

		userID := middleware.UserID(c)
		now := time.Now()
		err := h.DB.Transaction(func(tx *gorm.DB) error {
			if err := resetUsageIfStale(tx, userID, now); err != nil {
				return err
			}
			var user models.User
			if err := lockUser(tx, userID, &user); err != nil {
				return err
			}
			if slices.Contains(services.SplitUnlockedGroups(user.UnlockedGroupsToday), key) {
				return nil
			}
			limited, err := h.openGroupLimitReached(tx, userID, req.Level, req.Group)
			if err != nil {
				return err
			}
			if limited {
				if err := h.applyGate(c, services.GateViolation{
					Kind:   services.GateKindLearn,
					Reason: services.GateReasonOpenGroupLimit,
					UserID: userID,
					Level:  req.Level,
					Group:  req.Group,
				}, false, now, errOpenGroupLimit); err != nil {
					return err
				}
			} else if column == freeGroupColumn && user.GroupsStartedTodayCount >= 1 {
				if err := h.applyGate(c, services.GateViolation{
					Kind:   services.GateKindLearn,
					Reason: services.GateReasonFreeGroupReused,
					UserID: userID,
					Level:  req.Level,
					Group:  req.Group,
				}, false, now, errGateRequired); err != nil {
					return err
				}
			}

			// concat_ws değişken sayıda ve her tipte argüman alır; parametrenin
			// tipi açıkça verilmezse PostgreSQL onu yalnızca değer sorgu
			// metnine gömülü gittiğinde (basit protokol) çözebilir, hazırlanan
			// sorguda çözemez.
			return tx.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
				column:                  gorm.Expr(column + " + 1"),
				"unlocked_groups_today": gorm.Expr("concat_ws(',', NULLIF(unlocked_groups_today, ''), ?::text)", key),
				"last_reset_date":       usageTouchedAt(now),
			}).Error
		})
		if err != nil {
			return err
		}
		return h.respondUsage(c, userID)
	}
}

// RecordReviewAdPass: POST /users/me/usage/review-ad-pass
// Gövde (isteğe bağlı): {"checkpoint": 30 | 60 | 90}
//
// Sayaç "bugün kaç reklam kapısı geçildi" bilgisidir ve istemci sıradaki kapıyı
// ondan hesaplar. Gövdedeki kapıyla sayaç en az o kapının sırasına çekilir;
// zaten oradaysa değişmez. Böylece yanıtı kaybolan bir isteği yeniden denemek
// aynı kapıyı ikinci kez saymaz ve sıradaki kapıyı atlatmaz.
//
// Gövde göndermeyen (ya da geçersiz gönderen) istemcilerde uç yalnızca sayacı
// artırır.
func (h *Handlers) RecordReviewAdPass(c *fiber.Ctx) error {
	const column = "review_ad_pass_count_today"

	var req dto.ReviewAdPassRequest
	if err := c.BodyParser(&req); err != nil {
		return h.incrementUsage(c, column)
	}
	passes, ok := services.ReviewAdPassesForCheckpoint(req.Checkpoint)
	if !ok {
		return h.incrementUsage(c, column)
	}

	userID := middleware.UserID(c)
	now := time.Now()
	if err := resetUsageIfStale(h.DB, userID, now); err != nil {
		return err
	}

	res := h.DB.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
		column:            gorm.Expr("GREATEST("+column+", ?)", passes),
		"last_reset_date": usageTouchedAt(now),
	})
	if res.Error != nil {
		return res.Error
	}
	return h.respondUsage(c, userID)
}

// GrantNoFillPass: POST /users/me/usage/no-fill-pass
//
// Reklam hiç yüklenemediğinde (no-fill) istemci bu geçişi ister. Günlük sınır
// (services.MaxNoFillPassesPerDay) dolmuşsa 429 döner ve istemci akışı bloke
// eder. Sınır sunucuda tutulur; istemci taraflı bir bayrakla aşılamaz.
//
// Artırım koşullu UPDATE ile yapılır: eşzamanlı istekler sınırı aşamaz.
func (h *Handlers) GrantNoFillPass(c *fiber.Ctx) error {
	userID := middleware.UserID(c)

	var user models.User
	if err := findUser(h.DB, userID, &user); err != nil {
		return err
	}
	now := time.Now()
	if err := resetUsageIfStale(h.DB, userID, now); err != nil {
		return err
	}

	res := h.DB.Model(&models.User{}).
		Where("id = ? AND no_fill_passes_today < ?", userID, services.MaxNoFillPassesPerDay).
		Updates(map[string]any{
			"no_fill_passes_today": gorm.Expr("no_fill_passes_today + 1"),
			"last_reset_date":      usageTouchedAt(now),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apiErr(fiber.StatusTooManyRequests, "no_fill_limit_reached",
			"reklamsız geçiş hakkın bugünlük doldu")
	}

	return h.respondUsage(c, userID)
}
