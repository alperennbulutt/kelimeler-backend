package handlers

import (
	"errors"
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
	return c.JSON(fiber.Map{"user": h.userJSON(&user)})
}

// userJSON, /users/me uçlarının bugünkü yanıt şeklidir ({"user": ...}).
func (h *Handlers) userJSON(user *models.User) *dto.UserDTO {
	return dto.NewUserDTO(user, h.linkedProviders(user.ID))
}

// SetCurrentGroup: PUT /users/me/current-group — user_profile_repository.setCurrentGroup.
//
// level A1..C2 olmalı, group 1 ile seviyedeki grup sayısı arasında olmalıdır;
// aksi halde 400 invalid_group. İşaretçi yalnızca İLERİ yazılır: hedef mevcut
// konumun gerisindeyse hiçbir alan değişmez ve güncel kullanıcı 200 ile döner
// (eski bir gruba dönmek "kaldığın yer"i geri almaz).
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
	err := h.DB.Transaction(func(tx *gorm.DB) error {
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
		if services.CompareGroupPositions(req.Level, req.Group, user.CurrentLevel, user.CurrentGroup) < 0 {
			return nil
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
	return c.JSON(fiber.Map{"user": h.userJSON(&user)})
}

// AdvanceGroup: POST /users/me/advance-group — sonraki level/grup'u sunucu hesaplar.
//
// İsteğe bağlı gövde {"level","group"}, istemcinin tamamladığı gruptur:
// işaretçi tam o gruptaysa bir ileri gider, değilse (zaten ileride ya da
// geride) hiçbir şey değişmez. Yanıtı kaybolan isteğin yeniden denenmesi bu
// yüzden grup atlatmaz. Gövdesiz istek (eski istemciler) işaretçiyi koşulsuz
// bir ileri götürür.
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

	var user models.User
	levelChanged := false
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockUser(tx, userID, &user); err != nil {
			return err
		}
		if completed != nil && (completed.Level != user.CurrentLevel || completed.Group != user.CurrentGroup) {
			return nil
		}

		maxGroup, err := maxGroupForLevel(tx, user.CurrentLevel)
		if err != nil {
			return err
		}
		nextLevel, nextGroup, changed := services.AdvanceGroup(user.CurrentLevel, user.CurrentGroup, maxGroup)
		if nextLevel == user.CurrentLevel && nextGroup == user.CurrentGroup {
			return nil
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
		"user":         h.userJSON(&user),
		"levelChanged": levelChanged,
		"newLevel":     user.CurrentLevel,
		"newGroup":     user.CurrentGroup,
	})
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

// UnlockGroup: POST /users/me/usage/group-started ve /group-unlocked-by-ad
// Gövde (isteğe bağlı): {"level":"A1","group":3}
//
// Grup "bugün açılanlar" kümesinde değilse kümeye eklenir ve sayaç bir artar;
// zaten kümedeyse hiçbir şey değişmez. Böylece yanıtı kaybolan bir isteği
// yeniden denemek ya da aynı gruba gün içinde tekrar girmek, hakkı ikinci kez
// tüketmez.
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
		if err := resetUsageIfStale(h.DB, userID, now); err != nil {
			return err
		}

		// "Kümede yoksa ekle + artır" tek koşullu UPDATE'tir: aynı anahtarla
		// eşzamanlı gelen isteklerden yalnızca biri satırı değiştirir.
		//
		// concat_ws değişken sayıda ve her tipte argüman alır; parametrenin tipi
		// açıkça verilmezse PostgreSQL onu yalnızca değer sorgu metnine gömülü
		// gittiğinde (basit protokol) çözebilir, hazırlanan sorguda çözemez.
		res := h.DB.Model(&models.User{}).
			Where("id = ? AND NOT (? = ANY(string_to_array(unlocked_groups_today, ',')))", userID, key).
			Updates(map[string]any{
				column:                  gorm.Expr(column + " + 1"),
				"unlocked_groups_today": gorm.Expr("concat_ws(',', NULLIF(unlocked_groups_today, ''), ?::text)", key),
				"last_reset_date":       usageTouchedAt(now),
			})
		if res.Error != nil {
			return res.Error
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
