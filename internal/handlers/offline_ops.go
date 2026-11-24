package handlers

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/observability"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Çevrimdışı mod (bkz. kutularla_kelimeler/docs/CEVRIMDISI_MOD.md): istemci
// her cevaba bir kimlik (opId) ve zaman (answeredAt) verir. Bu dosya ikisinin
// ortak işleyişidir; tek tek uçlar (learn, unknown, swipe) ve POST
// /progress/sync aynı kuralları kullanır.

// answeredAt'in kabul edildiği aralık: en çok bu kadar geriye ve ileriye.
const (
	maxAnswerAge  = 7 * 24 * time.Hour
	maxAnswerSkew = 5 * time.Minute
)

// answerTime, bir cevabın hangi anda ve hangi günde uygulanacağıdır.
type answerTime struct {
	// now, sunucunun saatidir; "bugün" ve kapı raporlarının günü buna göredir.
	now time.Time
	// at, cevabın verildiği andır (kırpılmış); answeredAt verilmezse now.
	at time.Time
	// past, at'ın kullanım günü bugünden önceyse true: geçmiş günün cevabı
	// bugünkü sayaçlara ve tekrar kapısına girmez.
	past bool
}

// resolveAnswerTime, istemcinin answeredAt'ini [now − 7 gün, now + 5 dk]
// aralığına kırpar. İleri saat kayması cevabı yarına taşıyamaz: now'dan
// sonraki an başka bir kullanım gününe düşüyorsa now kullanılır.
func resolveAnswerTime(now time.Time, answeredAt *time.Time) answerTime {
	at := now
	if answeredAt != nil && !answeredAt.IsZero() {
		at = *answeredAt
		if oldest := now.Add(-maxAnswerAge); at.Before(oldest) {
			at = oldest
		}
		if latest := now.Add(maxAnswerSkew); at.After(latest) {
			at = latest
		}
		if at.After(now) && services.UsageDate(at) != services.UsageDate(now) {
			at = now
		}
	}
	return answerTime{
		now:  now,
		at:   at,
		past: services.UsageDayStart(at).Before(services.UsageDayStart(now)),
	}
}

// day, cevabın kullanım günüdür ("YYYY-MM-DD").
func (t answerTime) day() string { return services.UsageDate(t.at) }

// endOfDay, cevabın gününün bitişidir (ertesi gün 00:00, hariç): kelimenin
// vadesi bu güne göre denetlenir (services.AcceptsSwipe).
func (t answerTime) endOfDay() time.Time {
	_, end := reviewDayBounds(t.at)
	return end
}

// İşlem sonuçlarının durumları (dto.SyncOpResult.Status).
const (
	opApplied  = "applied"
	opNoop     = "noop"
	opRejected = "rejected"
)

// İşlem türleri (applied_ops.kind ve sync'teki "kind").
const (
	opKindLearn        = "learn"
	opKindUnknown      = "unknown"
	opKindSwipe        = "swipe"
	opKindGroupStarted = "group_started"
	opKindAdvanceGroup = "advance_group"
	opKindDailyLogin   = "daily_login"
	opKindSetGroup     = "set_current_group"
)

// opRef, istemcinin kimlikli bir işlemidir. nil ise işlem kimliksizdir ve
// idempotentlik kaydı yazılmaz (yayındaki istemci).
type opRef struct {
	id   string // küçük harfli, kanonik UUID
	raw  string // istemcinin gönderdiği biçim (yanıtta aynen döner)
	kind string
}

// errInvalidOpID, opId'nin UUID olmadığını bildirir.
var errInvalidOpID = apiErr(fiber.StatusBadRequest, "invalid_request", "opId geçerli bir UUID olmalı")

// errOpIDConflict, aynı opId'nin daha önce başka türde bir işlem için
// kullanıldığını bildirir (istemci hatası).
var errOpIDConflict = apiErr(fiber.StatusConflict, "op_id_conflict",
	"bu işlem kimliği başka bir işlem için kullanılmış")

// errOpDuplicate, transaction'ın sonunda işlemin kaydının zaten var olduğu
// (aynı opId'nin eşzamanlı ya da önceki bir isteği) anlaşıldığında döner;
// transaction'ı geri aldırır, çağıran saklı sonucu döner.
var errOpDuplicate = errors.New("işlem zaten uygulanmış")

// newOpRef, isteğe bağlı opId'yi çözer: boşsa nil, UUID değilse
// errInvalidOpID.
func newOpRef(raw, kind string) (*opRef, error) {
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, errInvalidOpID
	}
	return &opRef{id: id.String(), raw: raw, kind: kind}, nil
}

// loadAppliedOp, işlemin saklı kaydını okur; yoksa nil.
func loadAppliedOp(db *gorm.DB, userID string, op *opRef) (*models.AppliedOp, error) {
	var rows []models.AppliedOp
	if err := db.Where("user_id = ? AND op_id = ?", userID, op.id).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// storedResult, saklı kaydın sonucudur; tür uyuşmuyorsa errOpIDConflict.
// Yanıttaki opId istemcinin gönderdiği biçimdir.
func storedResult(row *models.AppliedOp, op *opRef) (dto.SyncOpResult, error) {
	if row.Kind != op.kind {
		return dto.SyncOpResult{}, errOpIDConflict
	}
	var res dto.SyncOpResult
	if err := json.Unmarshal([]byte(row.Result), &res); err != nil {
		return dto.SyncOpResult{}, err
	}
	res.OpID = op.raw
	return res, nil
}

// findStoredResult, işlem daha önce uygulanmışsa saklı sonucunu döner
// (found true).
func findStoredResult(db *gorm.DB, userID string, op *opRef) (res dto.SyncOpResult, found bool, err error) {
	row, err := loadAppliedOp(db, userID, op)
	if err != nil || row == nil {
		return dto.SyncOpResult{}, false, err
	}
	res, err = storedResult(row, op)
	return res, err == nil, err
}

// claimOp, işlemin uygulandığını sonucuyla birlikte tx içinde kaydeder;
// op nil ise hiçbir şey yapmaz. Transaction'ın SONUNDA çağrılır: kilit sırası
// tek tek uçlarınkiyle aynı kalır (önce kelime, sonra kullanıcı satırı).
//
// Aynı opId'yi taşıyan eşzamanlı bir transaction kaydını henüz commit
// etmemişse INSERT onu bekler; o commit olursa kayıt yazılmaz ve
// errOpDuplicate döner: çağıranın transaction'ı (yaptığı her şeyle) geri
// alınır ve işlem bir kez uygulanmış olur. reviewDay, işlem uygulanmış bir
// tekrar cevabıysa cevabın günüdür, değilse "".
//
// Kayıt kursa bağlı değildir (anahtar user_id, op_id): opId istemcinin her
// işlem için ürettiği bir UUID'dir ve kurslar arasında da tekildir. Aynı opId
// başka bir kursla yeniden gelirse (istemci hatası) ilk kursun saklı sonucu
// döner, ikinci kursta hiçbir şey uygulanmaz; işlem iki kez uygulanmaz.
func claimOp(tx *gorm.DB, userID string, op *opRef, res dto.SyncOpResult, reviewDay string, now time.Time) error {
	if op == nil {
		return nil
	}
	res.OpID = op.raw
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	r := tx.Exec(`INSERT INTO applied_ops (user_id, op_id, kind, applied_at, result, review_day)
		VALUES (?, ?, ?, ?, CAST(? AS jsonb), CAST(NULLIF(?, '') AS date))
		ON CONFLICT (user_id, op_id) DO NOTHING`,
		userID, op.id, op.kind, now.UTC(), string(raw), reviewDay)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected == 0 {
		return errOpDuplicate
	}
	return nil
}

// progressJSON, kelime ilerlemesinin GET /progress/word/:id biçimidir.
func progressJSON(wp *models.WordProgress) json.RawMessage {
	raw, err := json.Marshal(wp)
	if err != nil {
		// WordProgress'in alanları her zaman serileştirilebilir.
		panic(err)
	}
	return raw
}

// wordOutcome, bir kelime işleminin (learn, unknown, swipe) sonucudur.
type wordOutcome struct {
	status string // opApplied | opNoop
	wp     models.WordProgress
	// reviewDay, cevap sayılan bir tekrar cevabıysa (swipe öncesi kutu 2..5,
	// uygulandı) cevabın günüdür; değilse "".
	reviewDay string
}

// result, sonucun saklanan ve sync'te dönen biçimidir.
func (o wordOutcome) result() dto.SyncOpResult {
	return dto.SyncOpResult{Status: o.status, Progress: progressJSON(&o.wp)}
}

// respondStoredWordOp, tek tek uçlarda saklı sonucu döner: yanıt ilk
// yanıtla aynı gövdedir (kelime ilerlemesi).
func respondStoredWordOp(c *fiber.Ctx, res dto.SyncOpResult) error {
	if len(res.Progress) == 0 {
		return errOpIDConflict
	}
	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	return c.Send(res.Progress)
}

// runWordOp, tek tek cevap uçlarının ortak akışıdır: op verilmişse ve daha
// önce uygulanmışsa saklı yanıt döner; değilse apply tek transaction'da
// çalışır, kayıt aynı transaction'da yazılır ve kelime ilerlemesi döner.
func (h *Handlers) runWordOp(c *fiber.Ctx, userID string, op *opRef, now time.Time, apply func(tx *gorm.DB) (wordOutcome, error)) error {
	var out wordOutcome
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		if out, err = apply(tx); err != nil {
			return err
		}
		return claimOp(tx, userID, op, out.result(), out.reviewDay, now)
	})
	if errors.Is(err, errOpDuplicate) {
		return h.respondStoredOp(c, userID, op)
	}
	if err != nil {
		return err
	}
	return c.JSON(&out.wp)
}

// respondStoredOp, op daha önce uygulanmışsa saklı yanıtı döner. Kayıt
// yoksa (olmamalı: errOpDuplicate kaydın commit edildiğini gösterir) 500.
func (h *Handlers) respondStoredOp(c *fiber.Ctx, userID string, op *opRef) error {
	res, found, err := findStoredResult(h.DB, userID, op)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("işlem kaydı bulunamadı")
	}
	return respondStoredWordOp(c, res)
}

// replayWordOp, op daha önce uygulanmışsa saklı yanıtı döner (done true).
// Tek tek uçların ilk adımıdır: yeniden gelen işlem hiçbir kapıya ya da
// sözlük sorgusuna girmez.
func (h *Handlers) replayWordOp(c *fiber.Ctx, userID string, op *opRef) (done bool, err error) {
	if op == nil {
		return false, nil
	}
	res, found, err := findStoredResult(h.DB, userID, op)
	if err != nil || !found {
		return false, err
	}
	return true, respondStoredWordOp(c, res)
}

// isClientRejection, err bir istemci reddiyse (4xx APIError) kodunu döner:
// senkronizasyonda işlem reddedilir, diğer işlemler sürer. Diğer hatalar
// (veritabanı) isteği 500 ile bitirir.
func isClientRejection(err error) (code string, ok bool) {
	var e *middleware.APIError
	if errors.As(err, &e) && e.Status < fiber.StatusInternalServerError {
		return e.Code, true
	}
	return "", false
}

// validUsageDate, s'nin "YYYY-MM-DD" biçiminde bir gün olup olmadığını söyler.
func validUsageDate(s string) bool {
	if len(s) != len("2006-01-02") || strings.TrimSpace(s) != s {
		return false
	}
	_, err := time.ParseInLocation("2006-01-02", s, services.UsageLocation())
	return err == nil
}

// --- Geçmiş günün cevapları ---

// pastDayLearnGate, geçmiş bir güne (t.past) ait öğrenme cevabının günlük
// kapısıdır: kelimenin grubu o gün açılmış olmalıdır (daily_group_unlocks).
// Cevap gece yarısından sonraki GateRolloverGrace içindeyse önceki günün
// açılışı da sayılır (bugünkü kapının gece toleransıyla aynı kural).
// mapLocked, haritadaki kilidi de ihlal sayar. Karar kipe göre uygulanır
// (applyGate): enforce'ta 403 gate_required. Açılış course kursunda
// aranır: başka bir kursta açılan grup sayılmaz.
func (h *Handlers) pastDayLearnGate(c *fiber.Ctx, tx *gorm.DB, userID, course string, word *models.DictionaryWord, mapLocked bool, t answerTime) error {
	if h.gateMode() == config.GateOff || h.unlockAll() {
		return nil
	}
	allowed := false
	if _, ok := services.UnlockedGroupKey(word.Level, word.Grp); ok {
		var err error
		if allowed, err = groupUnlockedOn(tx, userID, course, word.Level, word.Grp, t.at); err != nil {
			return err
		}
	}
	reason := services.GateReasonPastDayNotUnlocked
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
	}, allowed, t.now, errGateRequired)
}

// groupUnlockedOn, course kursunun (level, grp) grubunun at'ın kullanım
// gününde açılmış olup olmadığını söyler; at gün başından GateRolloverGrace
// içindeyse önceki günün açılışı da sayılır.
func groupUnlockedOn(tx *gorm.DB, userID, course, level string, grp int, at time.Time) (bool, error) {
	days := []string{services.UsageDate(at)}
	if at.Sub(services.UsageDayStart(at)) < services.GateRolloverGrace {
		days = append(days, services.UsageDateDaysAgo(at, 1))
	}
	var found bool
	err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM daily_group_unlocks
		WHERE user_id = ? AND course = ? AND level = ? AND grp = ? AND usage_date IN (CAST(? AS date), CAST(? AS date)))`,
		userID, course, level, grp, days[0], days[len(days)-1]).Scan(&found).Error
	return found, err
}

// reportOnly, bir durumu kapı raporlayıcısıyla raporlar ama hiçbir zaman
// reddetmez (çevrimdışı modun kabul edilen tavizleri: geçmiş günde fazla
// tekrar, aynı gün iki ücretsiz grup). GATE_ENFORCEMENT=off'ta raporlanmaz.
func (h *Handlers) reportOnly(c *fiber.Ctx, v services.GateViolation, now time.Time) {
	mode := h.gateMode()
	if mode == config.GateOff {
		return
	}
	v.Mode = mode
	v.RequestID = observability.RequestIDFrom(c)
	v.AppVersion = observability.AppVersionFrom(c)
	gateReporter.Report(v, now)
}
