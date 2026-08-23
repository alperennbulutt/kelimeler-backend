package services

import "time"

// NeedsReset, lastResetDate (unix millis) bugünden farklı bir güne aitse true döner
// (DailyUsageData.needsReset portu — gün/ay/yıl karşılaştırması).
func NeedsReset(lastResetMillis int64) bool {
	if lastResetMillis == 0 {
		return true
	}
	last := time.UnixMilli(lastResetMillis)
	now := time.Now()
	y1, m1, d1 := now.Date()
	y2, m2, d2 := last.Date()
	return y1 != y2 || m1 != m2 || d1 != d2
}

// MaxNoFillPassesPerDay, reklam hiç yüklenemediğinde (no-fill) bir kullanıcıya
// gün içinde kaç kez reklamsız geçiş verileceğini sınırlar.
//
// Amaç: AdMob'un gerçekten envanter döndürmediği anlarda kullanıcıyı bloke
// etmemek, ama uçak modu / reklam engelleyici ile sınırsız ücretsiz kullanımı
// da mümkün kılmamak. Sunucu tarafında zorlanır; istemciden değiştirilemez.
const MaxNoFillPassesPerDay = 2
