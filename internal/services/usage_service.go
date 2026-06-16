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
