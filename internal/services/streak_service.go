package services

import "time"

// FormatDate, stats_repository._formatDate ile aynı format: "YYYY-MM-DD".
// t'nin kendi konumundaki günü verir; "bugün" için UsageDate kullanılmalı.
func FormatDate(t time.Time) string {
	return t.Format("2006-01-02")
}

// UsageDate, t'nin kullanım konumundaki (UsageLocation) gününü "YYYY-MM-DD"
// olarak döndürür. Giriş günü, seri ve takvim bu günü kullanır; böylece günlük
// sayaçlarla aynı gece yarısında döner.
//
// login_date bir "date" kolonudur ve bağlantı PreferSimpleProtocol ile açılır:
// time.Time göndermek oturumun saat dilimine göre günü kaydırabilir. Bu yüzden
// gün veritabanına her zaman bu metin (CAST(? AS date)) olarak yazılır ve
// to_char ile metin olarak okunur.
func UsageDate(t time.Time) string {
	return FormatDate(t.In(UsageLocation()))
}

// UsageDateDaysAgo, now'ın kullanım gününden n gün önceki günü "YYYY-MM-DD"
// olarak döndürür. Takvim günü üzerinden sayar (UTC öğlen), yaz saati
// geçişlerinde gün atlamaz.
func UsageDateDaysAgo(now time.Time, n int) string {
	y, m, d := now.In(UsageLocation()).Date()
	return FormatDate(time.Date(y, m, d-n, 12, 0, 0, 0, time.UTC))
}

// LoginRecord, daily_logins satırının gün hesabına giren alanlarıdır.
type LoginRecord struct {
	LoginDate string     // "YYYY-MM-DD"
	CreatedAt *time.Time // ilk giriş anı; eski kayıtlarda gün ondan türetilir
}

// LoginDays, giriş kayıtlarından kullanım günü kümesini çıkarır.
//
// Yeni kayıtlarda login_date zaten created_at'in kullanım günüdür. Eski kayıtlar
// ise sürecin (UTC) gününe yazılmıştı: TR 00:00–03:00 arasındaki giriş önceki
// güne düşüyordu. Bu kayıtlarda created_at'in kullanım günü de girişli sayılır;
// aksi halde geçişte gece giren kullanıcının serisi bir kez kırılırdı. Eski
// kayıt zaten girişli saydığı günü kaybetmez, en fazla ertesi gün eklenir.
func LoginDays(rows []LoginRecord) map[string]bool {
	set := make(map[string]bool, len(rows))
	for _, r := range rows {
		set[r.LoginDate] = true
		if r.CreatedAt != nil && !r.CreatedAt.IsZero() {
			set[UsageDate(*r.CreatedAt)] = true
		}
	}
	return set
}

// CalculateStreak: bugün giriş yoksa 0; varsa geriye doğru kesintisiz gün sayısı.
// stats_repository._calculateCurrentStreak portu. "Bugün", now'ın kullanım
// konumundaki günüdür.
func CalculateStreak(loggedDates map[string]bool, now time.Time) int {
	if !loggedDates[UsageDateDaysAgo(now, 0)] {
		return 0
	}
	streak := 0
	for i := 0; i <= 365; i++ {
		if loggedDates[UsageDateDaysAgo(now, i)] {
			streak++
		} else {
			break
		}
	}
	return streak
}
