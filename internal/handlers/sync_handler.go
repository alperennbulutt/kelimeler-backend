package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// maxSyncOps, POST /progress/sync'in tek istekte kabul ettiği en çok işlem
// sayısıdır; istemci kuyruğu parça parça gönderir.
const maxSyncOps = 200

// Senkronizasyonda işlem reddinin kodları (kapı kodları dışında).
var (
	errInvalidOp   = apiErr(fiber.StatusBadRequest, "invalid_op", "işlem eksik ya da geçersiz")
	errInvalidDate = apiErr(fiber.StatusBadRequest, "invalid_date", "tarih geçersiz ya da kabul edilen aralığın dışında")
)

// codeFreeGroupConflict, çevrimdışı geçmiş günde ücretsiz açılan grubun o
// günün ücretsiz hakkıyla çakıştığını bildirir (işlem yine uygulanır).
const codeFreeGroupConflict = "free_group_conflict"

// SyncProgress: POST /progress/sync {"ops":[...]} — çevrimdışı kuyruğun
// gönderimi (bkz. kutularla_kelimeler/docs/CEVRIMDISI_MOD.md, "API
// sözleşmesi").
//
// En çok maxSyncOps işlem; fazlası 400 too_many_ops. İşlemler sırayla, her
// biri kendi transaction'ında uygulanır ve her biri bir sonuç üretir
// (applied | noop | rejected). Reddedilen işlem sonrakileri durdurmaz.
// Veritabanı hatası isteği 500 ile bitirir: o ana kadar uygulananlar
// kayıtlıdır (applied_ops), istemci aynı parçayı yeniden gönderince onlar
// yeniden uygulanmaz, ilk sonuçları döner.
//
// Her işlemin opId'si (UUID) zorunludur. Aynı opId daha önce uygulanmışsa
// (tek tek uçlardan da olabilir) hiçbir şey yapılmaz, saklı sonuç döner.
// Reddedilen işlem kaydedilmez: kapı geçildikten sonra yeniden gönderilirse
// uygulanabilir.
//
// Cevap kuralları:
//   - answeredAt [şimdi − 7 gün, şimdi + 5 dk] aralığına kırpılır; Leitner
//     tarihleri ve vade bu ana göre (services.AcceptsSwipe).
//   - Geçmiş günün cevabı bugünkü sayaçlara ve tekrar kapısına girmez;
//     öğrenme ve kutu 1'deki doğru cevap o günün açılışlarına
//     (daily_group_unlocks) göre denetlenir. O günün tekrar cevapları
//     pastDayReviewReportThreshold'u aşarsa raporlanır, reddedilmez.
//   - Bugünün cevabı bugünkü sayaca yazılır; tekrar kapısı bir aralık (30
//     cevap) aşılabilir, fazlası review_gate_required ile reddedilir.
//   - group_started: usageDate bugünse group-started ile aynı; geçmiş günse
//     yalnızca daily_group_unlocks'a offline_free yazılır (o günün ücretsiz
//     hakkı başka gruba kullanılmışsa yine yazılır, kod free_group_conflict
//     ve rapor).
//   - advance_group: gövdeli advance-group ile aynı; ilerlemediyse noop ve
//     kod blockedReason.
//   - daily_login: date en çok 7 gün önce olabilir.
//   - set_current_group: PUT /users/me/current-group ile aynı (yalnızca
//     ileri; açık grup sınırı doluyken başlanmamış gruba open_group_limit).
//
// Kapı kararları GATE_ENFORCEMENT kipine tek tek uçlardaki gibi uyar: log'da
// ihlal raporlanıp uygulanır, enforce'ta reddedilir.
//
// Yanıt: {"results":[{opId,status,code?,progress?}], "user": GET /auth/me'nin
// "user"ı, "usage": GET /users/me/usage}.
func (h *Handlers) SyncProgress(c *fiber.Ctx) error {
	userID := middleware.UserID(c)
	var req dto.SyncRequest
	if err := c.BodyParser(&req); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "ops gerekli")
	}
	if len(req.Ops) > maxSyncOps {
		return apiErr(fiber.StatusBadRequest, "too_many_ops",
			fmt.Sprintf("tek istekte en fazla %d işlem gönderilebilir", maxSyncOps))
	}

	results := make([]dto.SyncOpResult, 0, len(req.Ops))
	for _, raw := range req.Ops {
		res, err := h.syncOp(c, userID, raw)
		if err != nil {
			return err
		}
		results = append(results, res)
	}

	if err := resetUsageIfStale(h.DB, userID, time.Now()); err != nil {
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
	return c.JSON(dto.SyncResponse{Results: results, User: u, Usage: usageDTO(&user)})
}

// syncOp, tek bir işlemi çözer ve uygular. Dönen hata yalnızca isteği
// bitirecek (veritabanı) hatasıdır; istemci kaynaklı her sorun bir rejected
// sonucudur.
func (h *Handlers) syncOp(c *fiber.Ctx, userID string, raw json.RawMessage) (dto.SyncOpResult, error) {
	var op dto.SyncOp
	if err := json.Unmarshal(raw, &op); err != nil {
		// Alanlardan biri bozuk (ör. answeredAt): yanıtta eşlenebilsin diye
		// en azından opId okunur.
		var idOnly struct {
			OpID string `json:"opId"`
		}
		_ = json.Unmarshal(raw, &idOnly)
		return rejectedResult(idOnly.OpID, errInvalidOp.Code), nil
	}
	if !syncKinds[op.Kind] {
		return rejectedResult(op.OpID, errInvalidOp.Code), nil
	}
	ref, err := newOpRef(op.OpID, op.Kind)
	if err != nil || ref == nil {
		return rejectedResult(op.OpID, errInvalidOp.Code), nil
	}

	res, found, err := findStoredResult(h.DB, userID, ref)
	if err == nil && !found {
		t := resolveAnswerTime(time.Now(), op.AnsweredAt)
		res, err = h.applySyncOp(c, userID, &op, ref, t)
		if errors.Is(err, errOpDuplicate) {
			// Aynı opId eşzamanlı bir istekte uygulandı; bu transaction geri
			// alındı.
			res, found, err = findStoredResult(h.DB, userID, ref)
			if err == nil && !found {
				err = errors.New("işlem kaydı bulunamadı")
			}
		}
	}
	if code, ok := isClientRejection(err); ok {
		res = rejectedResult(op.OpID, code)
		if isWordOp(op.Kind) && op.WordID != "" {
			if res.Progress, err = currentProgress(h.DB, userID, op.WordID); err != nil {
				return dto.SyncOpResult{}, err
			}
		}
		return res, nil
	}
	if err != nil {
		return dto.SyncOpResult{}, err
	}
	res.OpID = op.OpID
	return res, nil
}

// syncKinds, senkronizasyonun tanıdığı işlem türleridir.
var syncKinds = map[string]bool{
	opKindLearn: true, opKindUnknown: true, opKindSwipe: true,
	opKindGroupStarted: true, opKindAdvanceGroup: true, opKindDailyLogin: true,
	opKindSetGroup: true,
}

func isWordOp(kind string) bool {
	return kind == opKindLearn || kind == opKindUnknown || kind == opKindSwipe
}

func rejectedResult(opID, code string) dto.SyncOpResult {
	return dto.SyncOpResult{OpID: opID, Status: opRejected, Code: code}
}

// currentProgress, kelimenin güncel kaydını döner; kayıt yoksa nil.
func currentProgress(db *gorm.DB, userID, wordID string) (json.RawMessage, error) {
	var rows []models.WordProgress
	if err := db.Where("user_id = ? AND word_id = ?", userID, wordID).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return progressJSON(&rows[0]), nil
}

// applySyncOp, işlemi türüne göre kendi transaction'ında uygular ve kaydını
// (applied_ops) aynı transaction'da yazar.
func (h *Handlers) applySyncOp(c *fiber.Ctx, userID string, op *dto.SyncOp, ref *opRef, t answerTime) (dto.SyncOpResult, error) {
	switch op.Kind {
	case opKindLearn, opKindUnknown, opKindSwipe:
		return h.syncWordOp(c, userID, op, ref, t)
	case opKindGroupStarted:
		return h.syncGroupStarted(c, userID, op, ref, t)
	case opKindAdvanceGroup:
		return h.syncAdvanceGroup(userID, op, ref, t)
	case opKindDailyLogin:
		return h.syncDailyLogin(userID, op, ref, t)
	case opKindSetGroup:
		return h.syncSetCurrentGroup(userID, op, ref, t)
	}
	return dto.SyncOpResult{}, errInvalidOp
}

// syncWordOp, learn / unknown / swipe işlemini tek tek uçlarla aynı kurallarla
// uygular. Fark: bugünün tekrar cevabında kapı bir aralık aşılabilir.
func (h *Handlers) syncWordOp(c *fiber.Ctx, userID string, op *dto.SyncOp, ref *opRef, t answerTime) (dto.SyncOpResult, error) {
	if op.WordID == "" {
		return dto.SyncOpResult{}, errInvalidOp
	}
	var apply func(tx *gorm.DB) (wordOutcome, error)
	switch op.Kind {
	case opKindLearn:
		word, err := h.dictionaryWord(op.WordID)
		if err != nil {
			return dto.SyncOpResult{}, err
		}
		mapState, err := h.learnMapLocked(userID, word)
		if err != nil {
			return dto.SyncOpResult{}, err
		}
		apply = func(tx *gorm.DB) (wordOutcome, error) {
			return h.applyLearn(c, tx, userID, word, mapState, t)
		}
	case opKindUnknown:
		word, err := h.dictionaryWord(op.WordID)
		if err != nil {
			return dto.SyncOpResult{}, err
		}
		mapState, _, err := h.unknownMapState(userID, word)
		if err != nil {
			return dto.SyncOpResult{}, err
		}
		apply = func(tx *gorm.DB) (wordOutcome, error) {
			return h.applyUnknown(c, tx, userID, word, mapState, t)
		}
	case opKindSwipe:
		if op.Direction != "right" && op.Direction != "left" {
			return dto.SyncOpResult{}, errInvalidOp
		}
		apply = func(tx *gorm.DB) (wordOutcome, error) {
			return h.applySwipe(c, tx, userID, op.WordID, op.Direction, t, services.ReviewAdCheckpointInterval)
		}
	}

	var out wordOutcome
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		if out, err = apply(tx); err != nil {
			return err
		}
		return claimOp(tx, userID, ref, out.result(), out.reviewDay, t.now)
	})
	if err != nil {
		return dto.SyncOpResult{}, err
	}
	return out.result(), nil
}

// syncGroupStarted, günün ücretsiz grup açılışıdır. usageDate bugünse
// POST /users/me/usage/group-started ile aynıdır (sayaç, küme, kapı). Geçmiş
// günse (en çok 7 gün önce) yalnızca o günün açılış geçmişine offline_free
// yazılır: o günün sayaçları çoktan sıfırlanmıştır. O gün başka bir grup
// zaten ücretsiz açılmışsa açılış yine yazılır, sonuç kodu
// free_group_conflict olur ve durum raporlanır. Bugünden sonraki gün (saat
// kayması) bugün sayılır.
func (h *Handlers) syncGroupStarted(c *fiber.Ctx, userID string, op *dto.SyncOp, ref *opRef, t answerTime) (dto.SyncOpResult, error) {
	if _, ok := services.UnlockedGroupKey(op.Level, op.Group); !ok {
		return dto.SyncOpResult{}, errInvalidGroup
	}
	day := op.UsageDate
	if day == "" {
		day = t.day()
	}
	if !validUsageDate(day) {
		return dto.SyncOpResult{}, errInvalidDate
	}
	today := services.UsageDate(t.now)
	if day > today {
		day = today
	}
	if day < services.UsageDateDaysAgo(t.now, 7) {
		return dto.SyncOpResult{}, errInvalidDate
	}

	var res dto.SyncOpResult
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		res = dto.SyncOpResult{Status: opNoop}
		if day == today {
			added, err := h.unlockGroupTx(c, tx, userID, op.Level, op.Group, freeGroupColumn, t.now)
			if err != nil {
				return err
			}
			if added {
				res.Status = opApplied
			}
		} else {
			conflict, err := freeGroupUsedOn(tx, userID, day, op.Level, op.Group)
			if err != nil {
				return err
			}
			inserted, err := recordGroupUnlock(tx, userID, day, op.Level, op.Group, models.UnlockViaOfflineFree, t.now)
			if err != nil {
				return err
			}
			if inserted {
				res.Status = opApplied
				if conflict {
					res.Code = codeFreeGroupConflict
					h.reportOnly(c, services.GateViolation{
						Kind:   services.GateKindLearn,
						Reason: services.GateReasonOfflineFreeGroupConflict,
						UserID: userID,
						Level:  op.Level,
						Group:  op.Group,
					}, t.now)
				}
			}
		}
		return claimOp(tx, userID, ref, res, "", t.now)
	})
	return res, err
}

// freeGroupUsedOn, day gününün ücretsiz hakkının (level, grp) dışında bir
// gruba kullanılıp kullanılmadığını söyler.
func freeGroupUsedOn(tx *gorm.DB, userID, day, level string, grp int) (bool, error) {
	var used bool
	err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM daily_group_unlocks
		WHERE user_id = ? AND usage_date = CAST(? AS date) AND via IN (?, ?)
		  AND NOT (level = ? AND grp = ?))`,
		userID, day, models.UnlockViaFree, models.UnlockViaOfflineFree, level, grp).Scan(&used).Error
	return used, err
}

// syncAdvanceGroup, gövdeli POST /users/me/advance-group ile aynıdır: işaretçi
// tam o gruptaysa ve grup görülmüşse ilerler (applied); değilse noop, kod
// blockedReason (boş olabilir).
func (h *Handlers) syncAdvanceGroup(userID string, op *dto.SyncOp, ref *opRef, t answerTime) (dto.SyncOpResult, error) {
	if services.IndexOfLevel(op.Level) < 0 || op.Group < 1 {
		return dto.SyncOpResult{}, errInvalidGroup
	}
	completed := &dto.AdvanceGroupRequest{Level: op.Level, Group: op.Group}
	var res dto.SyncOpResult
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		var user models.User
		adv, err := h.advanceGroupTx(tx, userID, completed, &user)
		if err != nil {
			return err
		}
		res = dto.SyncOpResult{Status: opNoop, Code: adv.blockedReason}
		if adv.moved {
			res.Status = opApplied
		}
		return claimOp(tx, userID, ref, res, "", t.now)
	})
	return res, err
}

// syncSetCurrentGroup, PUT /users/me/current-group ile aynıdır: işaretçi
// yalnızca ileri yazılır. İleri taşındıysa applied; hedef işaretçinin kendisi
// ya da gerisindeyse noop. Geçersiz seviye/grup invalid_group, açık grup sınırı
// doluyken başlanmamış gruba open_group_limit ile reddedilir (reddedilen
// işlem kaydedilmez; sınır açılınca aynı opId yeniden gönderilirse
// uygulanabilir). İşlem başlık, açıklama ve kelime sayısı taşımaz: onlar
// grubun bilgisidir, işaretçi taşınırken sunucudaki değerleri kalır.
func (h *Handlers) syncSetCurrentGroup(userID string, op *dto.SyncOp, ref *opRef, t answerTime) (dto.SyncOpResult, error) {
	if services.IndexOfLevel(op.Level) < 0 || op.Group < 1 {
		return dto.SyncOpResult{}, errInvalidGroup
	}
	req := &dto.SetCurrentGroupRequest{Level: op.Level, Group: op.Group}
	var res dto.SyncOpResult
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		var user models.User
		moved, err := h.setCurrentGroupTx(tx, userID, req, &user)
		if err != nil {
			return err
		}
		res = dto.SyncOpResult{Status: opNoop}
		if moved {
			res.Status = opApplied
		}
		return claimOp(tx, userID, ref, res, "", t.now)
	})
	return res, err
}

// syncDailyLogin, date gününün girişini kaydeder (POST /stats/daily-login'in
// geçmiş gün karşılığı). date bugün ya da en çok 7 gün önce olmalıdır.
func (h *Handlers) syncDailyLogin(userID string, op *dto.SyncOp, ref *opRef, t answerTime) (dto.SyncOpResult, error) {
	if !validUsageDate(op.Date) {
		return dto.SyncOpResult{}, errInvalidDate
	}
	if op.Date > services.UsageDate(t.now) || op.Date < services.UsageDateDaysAgo(t.now, 7) {
		return dto.SyncOpResult{}, errInvalidDate
	}
	var res dto.SyncOpResult
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		r := tx.Exec(`INSERT INTO daily_logins (user_id, login_date, created_at)
			VALUES (?, CAST(? AS date), ?)
			ON CONFLICT (user_id, login_date) DO NOTHING`,
			userID, op.Date, loginCreatedAt(op.Date, t).UTC())
		if r.Error != nil {
			return r.Error
		}
		res = dto.SyncOpResult{Status: opNoop}
		if r.RowsAffected > 0 {
			res.Status = opApplied
		}
		return claimOp(tx, userID, ref, res, "", t.now)
	})
	return res, err
}

// loginCreatedAt, geçmiş günün giriş kaydının created_at'idir. Seri hesabı
// eski kayıtlarda created_at'in gününü de girişli sayar (services.LoginDays);
// senkronizasyon anı yazılsaydı bugün de girişli görünürdü. Cevabın anı o
// güne düşüyorsa o, değilse o günün öğlesi (kullanım saat dilimi) yazılır.
func loginCreatedAt(day string, t answerTime) time.Time {
	if services.UsageDate(t.at) == day {
		return t.at
	}
	d, err := time.ParseInLocation("2006-01-02", day, services.UsageLocation())
	if err != nil {
		return t.at
	}
	return d.Add(12 * time.Hour)
}
