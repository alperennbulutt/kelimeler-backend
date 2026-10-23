package services

import (
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/models"
)

// Reklam kapısı kararları (sözleşme K4).
//
// Buradaki fonksiyonlar yalnızca karar verir; veritabanına dokunmaz. Handler,
// kullanıcı satırını yazımla aynı transaction'da kilitleyip okur ve bu
// fonksiyonlara verir: eşzamanlı istekler aynı (kilit öncesi) değeri görüp
// kapıyı birlikte aşamaz.
//
// Kurallar istemcideki kapılarla birebir aynıdır; dürüst bir istemci hiçbir
// zaman ihlal üretmemelidir (bkz. gate_test.go'daki uç durumlar).

// Kapı ihlalinin nedenleri: log satırında ve Sentry mesajında görünür.
const (
	// GateReasonGroupNotUnlocked: kelimenin grubu bugün açılmamış (ne ücretsiz
	// hak ne reklam kaydı var).
	GateReasonGroupNotUnlocked = "group_not_unlocked"
	// GateReasonMapLocked: grup haritada kilitli (sıralı kilit ya da önceki
	// seviye bitmemiş).
	GateReasonMapLocked = "map_locked"
	// GateReasonFreeGroupReused: günün ücretsiz grup açılışı (group-started)
	// zaten kullanılmışken başka bir grup ücretsiz açılmak istendi.
	GateReasonFreeGroupReused = "free_group_reused"
	// GateReasonReviewCheckpoint: günün tekrar cevapları bir kontrol noktasına
	// ulaşmış ve o nokta için geçiş kaydı yok.
	GateReasonReviewCheckpoint = "review_checkpoint"
)

// Kapı türleri: aynı kullanıcının iki türdeki ihlali ayrı sayılır.
const (
	GateKindLearn  = "learn"
	GateKindReview = "review"
)

// GateRolloverGrace, gece yarısını geçen öğrenme oturumuna tanınan süredir.
//
// Öğrenme sayfası açıkken gün dönerse "bugün açılan gruplar" kümesi yeni
// günde boştur; istemci bunu ancak bir kayıt reddedilince fark eder. Satır
// yeni gün için henüz sıfırlanmamışsa (o gün hiçbir kullanım kaydı yazılmamış)
// ve grup dün açılmışsa, gün başından bu süre boyunca öğrenme açık sayılır.
// Kötüye kullanım alanı dar: yalnızca dün gerçekten açılmış gruplar, yalnızca
// gece yarısından sonraki ilk saat.
const GateRolloverGrace = time.Hour

// NormalizeGateMode, yapılandırmadaki kipi döndürür; boş ya da tanınmayan
// değer varsayılan kiptir (config.Load geçersiz değeri zaten reddeder; elle
// kurulan Config'lerde alan boş kalabilir).
func NormalizeGateMode(m config.GateEnforcement) config.GateEnforcement {
	switch m {
	case config.GateOff, config.GateLog, config.GateEnforce:
		return m
	}
	return config.DefaultGateEnforcement
}

// GateAction, bir kapı kararının istekte ne yapılacağıdır.
type GateAction int

const (
	// GateAllow: istek olduğu gibi işlenir.
	GateAllow GateAction = iota
	// GateAllowAndReport: istek işlenir, ihlal raporlanır (log kipi).
	GateAllowAndReport
	// GateReject: istek 403 ile reddedilir (enforce kipi).
	GateReject
)

// GateActionFor, kipe ve kararın sonucuna göre yapılacak işi döndürür.
func GateActionFor(mode config.GateEnforcement, allowed bool) GateAction {
	if allowed {
		return GateAllow
	}
	switch NormalizeGateMode(mode) {
	case config.GateOff:
		return GateAllow
	case config.GateEnforce:
		return GateReject
	default:
		return GateAllowAndReport
	}
}

// DailyGateState, kullanıcı satırındaki günlük sayaçların now'ın kullanım
// günündeki değeridir. Satır bugün henüz sıfırlanmamışsa sayaçlar sıfır
// sayılır: resetUsageIfStale'in yazacağı değerlerle aynı (gün dönümü kuralı
// NeedsResetAt'tir).
type DailyGateState struct {
	// Bugün açılan gruplar ("SEVIYE:GRUP").
	UnlockedGroups []string
	// Bugün açılan grup sayısı (ücretsiz + reklamla). Gövdesiz (eski
	// istemci) kayıtlar yalnızca bu sayıyı artırır, kümeye anahtar yazmaz.
	GroupsOpened int
	// Bugünkü tekrar cevabı ve geçilen tekrar kapısı sayıları.
	ReviewAnswers  int
	ReviewAdPasses int
	// Dün açılan gruplar; yalnızca satır bugün henüz sıfırlanmamışsa, en son
	// dün yazılmışsa ve now gün başından GateRolloverGrace içindeyse dolu.
	CarriedOverGroups []string
}

// GateStateAt, u'nun now'daki günlük kapı durumunu döndürür.
func GateStateAt(u *models.User, now time.Time) DailyGateState {
	if !NeedsResetAt(u.LastResetDate, now) {
		return DailyGateState{
			UnlockedGroups: SplitUnlockedGroups(u.UnlockedGroupsToday),
			GroupsOpened:   u.GroupsStartedTodayCount + u.GroupsUnlockedByAdToday,
			ReviewAnswers:  u.ReviewAnswersToday,
			ReviewAdPasses: u.ReviewAdPassCountToday,
		}
	}
	s := DailyGateState{UnlockedGroups: []string{}}
	dayStart := UsageDayStart(now)
	yesterdayStart := dayStart.AddDate(0, 0, -1).UnixMilli()
	if u.LastResetDate >= yesterdayStart && now.Sub(dayStart) < GateRolloverGrace {
		s.CarriedOverGroups = SplitUnlockedGroups(u.UnlockedGroupsToday)
	}
	return s
}

func containsKey(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

// LearnAllowedToday, key ("SEVIYE:GRUP") grubunda bugün YENİ kelime
// öğrenilebilir mi söyler: grup bugün açılmışsa (ücretsiz hak ya da reklam)
// ya da gece yarısı toleransındaysa (bkz. GateRolloverGrace).
//
// Kelimenin zaten ilerlemesi varsa (tekrar gelen learn isteği yazım yapmaz)
// ve TEST_MODE_UNLOCK_ALL kararları çağırana aittir.
func LearnAllowedToday(s DailyGateState, key string) bool {
	return containsKey(s.UnlockedGroups, key) || containsKey(s.CarriedOverGroups, key)
}

// KeylessUnlockSlots, bugün gövdesiz (hangi grup olduğu bilinmeyen) yazılmış
// grup açılışı sayısıdır. Gövdeli kayıt aynı grubu bir kez sayar ve anahtarını
// kümeye yazar; kalan fark eski istemcinin açtığı gruplardır.
func KeylessUnlockSlots(s DailyGateState) int {
	if n := s.GroupsOpened - len(s.UnlockedGroups); n > 0 {
		return n
	}
	return 0
}

// KeylessSlotCovers, gövdesiz açılışların key grubunu karşılayıp
// karşılamadığını söyler. learnedToday, bugün yeni kelime öğrenilen grupların
// anahtarlarıdır; kümede (unlocked) olanlar kendi kaydıyla açılmıştır, kalanlar
// birer gövdesiz açılış harcamış sayılır. Grup zaten onlardan biriyse ya da
// boş açılış kaldıysa izin verilir.
func KeylessSlotCovers(slots int, learnedToday, unlocked []string, key string) bool {
	used := map[string]bool{}
	for _, k := range learnedToday {
		if !containsKey(unlocked, k) {
			used[k] = true
		}
	}
	return used[key] || len(used) < slots
}

// ReviewCheckpointForAnswer, günün n. tekrar cevabından önce geçilmiş olması
// gereken kontrol noktasıdır: 1-30 → 0 (kapı yok), 31-60 → 30, 61-90 → 60,
// 91+ → 90. İstemcideki ReviewGatingService.getCheckpointAtAnswerCount ile
// aynıdır.
func ReviewCheckpointForAnswer(n int) int {
	if n <= ReviewAdCheckpointInterval {
		return 0
	}
	cp := ((n - 1) / ReviewAdCheckpointInterval) * ReviewAdCheckpointInterval
	if maxCp := MaxReviewAdCheckpoints * ReviewAdCheckpointInterval; cp > maxCp {
		return maxCp
	}
	return cp
}

// ReviewAnswerAllowed, bugün answersToday tekrar cevabı verilmiş ve passes
// kapı geçilmişken bir sonraki tekrar cevabının sayılabileceğini söyler.
//
// Her geçiş bir kontrol noktasını açar (30, 60, 90); üçü de geçildiyse gün
// serbesttir. Fazla geçiş (gövdesiz eski kayıtlar sayacı 3'ün üstüne
// çıkarabilir) 90'ı aşan bir şey açmaz. No-fill toleransıyla geçilen kapı da
// istemcide review-ad-pass ile kaydedilir; ayrıca sayılmaz.
func ReviewAnswerAllowed(answersToday, passes int) bool {
	if passes > MaxReviewAdCheckpoints {
		passes = MaxReviewAdCheckpoints
	}
	return ReviewCheckpointForAnswer(answersToday+1) <= passes*ReviewAdCheckpointInterval
}
