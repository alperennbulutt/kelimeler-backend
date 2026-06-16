package services

import "time"

// FormatDate, stats_repository._formatDate ile aynı format: "YYYY-MM-DD".
func FormatDate(t time.Time) string {
	return t.Format("2006-01-02")
}

// CalculateStreak: bugün giriş yoksa 0; varsa geriye doğru kesintisiz gün sayısı.
// stats_repository._calculateCurrentStreak portu.
func CalculateStreak(loggedDates map[string]bool, now time.Time) int {
	if !loggedDates[FormatDate(now)] {
		return 0
	}
	streak := 0
	for i := 0; i <= 365; i++ {
		if loggedDates[FormatDate(now.AddDate(0, 0, -i))] {
			streak++
		} else {
			break
		}
	}
	return streak
}
