package handlers

import (
	"sort"
	"time"

	"kelimeler-backend/internal/services"
)

// reviewDayBounds, now'ın kullanım günüdür (services.UsageLocation): bugünün
// başı ve bitişi (ertesi gün 00:00, hariç üst sınır). Tekrar uçlarının ve
// swipe vadesinin "bugün"ü budur.
func reviewDayBounds(now time.Time) (start, end time.Time) {
	start = services.UsageDayStart(now)
	return start, start.AddDate(0, 0, 1)
}

// usageDaysBetween, start'ın kullanım günü ile t'nin kullanım günü arasındaki
// takvim günü farkıdır. Saat farkını 24'e bölmek yaz saati geçişinde bir gün
// kaydırır; burada yalnızca tarihler karşılaştırılır.
func usageDaysBetween(start, t time.Time) int {
	loc := services.UsageLocation()
	sy, sm, sd := start.In(loc).Date()
	ty, tm, td := t.In(loc).Date()
	a := time.Date(sy, sm, sd, 0, 0, 0, 0, time.UTC)
	b := time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC)
	return int(b.Sub(a).Hours() / 24)
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
