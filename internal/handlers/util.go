package handlers

import (
	"sort"
	"time"

	"kelimeler-backend/internal/services"
)

// parseQueryDate, "YYYY-MM-DD" formatındaki query parametresini çözer; yoksa şimdi.
func parseQueryDate(s string) time.Time {
	if s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return t
		}
	}
	return time.Now()
}

// reviewDayBounds, now'ın kullanım günüdür (services.UsageLocation): bugünün
// başı ve bitişi (ertesi gün 00:00, hariç üst sınır). Tekrar uçlarının ve
// swipe vadesinin "bugün"ü budur.
func reviewDayBounds(now time.Time) (start, end time.Time) {
	start = services.UsageDayStart(now)
	return start, start.AddDate(0, 0, 1)
}

// startOfDay, verilen zamanın gün başlangıcını (00:00) döndürür.
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// sortedIntKeys, int-anahtarlı bir map'in anahtarlarını sıralı döndürür.
func sortedIntKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}
