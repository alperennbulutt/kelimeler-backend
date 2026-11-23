package services

import (
	"testing"
	"time"

	"kelimeler-backend/internal/models"
)

func TestReviewDaysForBox(t *testing.T) {
	cases := map[int]int{
		1: 0, // kutu 1: henüz öğrenilmedi, tekrar yok
		2: 3,
		3: 7,
		4: 15,
		5: 30,
		6: 0, // kutu 6: tamamen öğrenildi, tekrar yok
		7: 0, // tanımsız kutu güvenli varsayılana düşer
	}
	for box, want := range cases {
		if got := ReviewDaysForBox(box); got != want {
			t.Errorf("ReviewDaysForBox(%d) = %d, beklenen %d", box, got, want)
		}
	}
}

func TestLearnNewWord(t *testing.T) {
	wp := &models.WordProgress{Box: 1, WrongCount: 2, Learned: true}
	before := time.Now()
	LearnNewWord(wp, time.Now())

	if wp.Box != 2 {
		t.Errorf("Box = %d, beklenen 2", wp.Box)
	}
	if wp.WrongCount != 0 {
		t.Errorf("WrongCount = %d, beklenen 0 (sıfırlanmalı)", wp.WrongCount)
	}
	if wp.Learned {
		t.Error("Learned = true, yeni öğrenilen kelime henüz tamamlanmış sayılmaz")
	}
	if wp.LastReviewDate == nil || wp.LastReviewDate.Before(before) {
		t.Error("LastReviewDate güncellenmedi")
	}
	if wp.NextReviewDate == nil {
		t.Fatal("NextReviewDate nil, kutu 2 için 3 gün sonrası beklenir")
	}
	if gotDays := int(wp.NextReviewDate.Sub(*wp.LastReviewDate).Hours() / 24); gotDays != 3 {
		t.Errorf("NextReviewDate farkı %d gün, beklenen 3", gotDays)
	}
}

func TestOnRightSwipe_KutuyuYukseltir(t *testing.T) {
	cases := []struct {
		fromBox      int
		wantBox      int
		wantLearned  bool
		wantNextNil  bool
		wantNextDays int
	}{
		{fromBox: 2, wantBox: 3, wantNextDays: 7},
		{fromBox: 3, wantBox: 4, wantNextDays: 15},
		{fromBox: 4, wantBox: 5, wantNextDays: 30},
		// Kutu 5'ten sonrası kutu 6: öğrenildi, bir daha tekrar edilmez.
		{fromBox: 5, wantBox: 6, wantLearned: true, wantNextNil: true},
		// Zaten kutu 6'daysa orada kalır.
		{fromBox: 6, wantBox: 6, wantLearned: true, wantNextNil: true},
	}

	for _, c := range cases {
		wp := &models.WordProgress{Box: c.fromBox, WrongCount: 2}
		OnRightSwipe(wp, time.Now())

		if wp.Box != c.wantBox {
			t.Errorf("kutu %d → %d, beklenen %d", c.fromBox, wp.Box, c.wantBox)
		}
		if wp.Learned != c.wantLearned {
			t.Errorf("kutu %d → Learned = %v, beklenen %v", c.fromBox, wp.Learned, c.wantLearned)
		}
		if wp.WrongCount != 0 {
			t.Errorf("kutu %d → WrongCount = %d, doğru cevap sıfırlamalı", c.fromBox, wp.WrongCount)
		}
		if c.wantNextNil {
			if wp.NextReviewDate != nil {
				t.Errorf("kutu %d → NextReviewDate set edilmiş, kutu 6 tekrar edilmemeli", c.fromBox)
			}
			continue
		}
		if wp.NextReviewDate == nil {
			t.Fatalf("kutu %d → NextReviewDate nil", c.fromBox)
		}
		gotDays := int(wp.NextReviewDate.Sub(*wp.LastReviewDate).Hours() / 24)
		if gotDays != c.wantNextDays {
			t.Errorf("kutu %d → sonraki tekrar %d gün, beklenen %d", c.fromBox, gotDays, c.wantNextDays)
		}
	}
}

func TestOnLeftSwipe_BirAltKutuyaInipYarinSorulur(t *testing.T) {
	loc := UsageLocation()
	// Kullanım gününün 23:30'u: ertesi gün başı yalnızca 30 dakika sonradır.
	// Kural "24 saat sonra" değil "yarın gün başı" olduğu için fark önemlidir.
	now := time.Date(2026, 3, 10, 23, 30, 0, 0, loc)
	tomorrow := time.Date(2026, 3, 11, 0, 0, 0, 0, loc)

	cases := []struct {
		name      string
		fromBox   int
		fromWrong int
		learned   bool
		wantBox   int
		wantWrong int
	}{
		{name: "kutu 5 kutu 4'e iner", fromBox: 5, wantBox: 4, wantWrong: 1},
		{name: "kutu 4 kutu 3'e iner", fromBox: 4, fromWrong: 1, wantBox: 3, wantWrong: 2},
		{name: "kutu 3 kutu 2'ye iner", fromBox: 3, wantBox: 2, wantWrong: 1},
		// Kutu 2 tabandır: daha aşağı inmez ama yine yarın sorulur.
		{name: "kutu 2 tabanda kalir", fromBox: 2, fromWrong: 2, wantBox: 2, wantWrong: 3},
		// Öğrenilmiş kelime yanlış cevaplanırsa artık öğrenilmiş sayılmaz.
		{name: "kutu 6 kutu 5'e iner", fromBox: 6, learned: true, wantBox: 5, wantWrong: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wp := &models.WordProgress{Box: c.fromBox, WrongCount: c.fromWrong, Learned: c.learned}
			OnLeftSwipe(wp, now)

			if wp.Box != c.wantBox {
				t.Errorf("kutu %d → %d, beklenen %d", c.fromBox, wp.Box, c.wantBox)
			}
			if wp.WrongCount != c.wantWrong {
				t.Errorf("WrongCount = %d, beklenen %d", wp.WrongCount, c.wantWrong)
			}
			if wp.Learned {
				t.Error("Learned = true, yanlış cevaplanan kelime öğrenilmiş sayılmaz")
			}
			if wp.LastReviewDate == nil || !wp.LastReviewDate.Equal(now) {
				t.Errorf("LastReviewDate = %v, beklenen %v", wp.LastReviewDate, now)
			}
			if wp.NextReviewDate == nil || !wp.NextReviewDate.Equal(tomorrow) {
				t.Errorf("NextReviewDate = %v, beklenen ertesi gün başı %v", wp.NextReviewDate, tomorrow)
			}
		})
	}
}

func TestOnLeftSwipe_GunBasiKullanimSaatDilimineGore(t *testing.T) {
	// UTC'de hâlâ 10 Mart olan an, İstanbul'da 11 Mart'tır: "yarın" 12 Mart
	// İstanbul gece yarısıdır, 11 Mart UTC gece yarısı değil.
	loc := UsageLocation()
	now := time.Date(2026, 3, 10, 22, 30, 0, 0, time.UTC)
	y, m, d := now.In(loc).Date()
	want := time.Date(y, m, d+1, 0, 0, 0, 0, loc)

	wp := &models.WordProgress{Box: 3}
	OnLeftSwipe(wp, now)
	if wp.NextReviewDate == nil || !wp.NextReviewDate.Equal(want) {
		t.Errorf("NextReviewDate = %v, beklenen %v", wp.NextReviewDate, want)
	}
}

func TestOnLeftSwipe_Kutu1KutusundaKalir(t *testing.T) {
	// Öğrenme ekranındaki "Bilmiyorum": kelime henüz öğrenilmedi, kutu 2'ye
	// çıkmaz ve kutu 1'de hemen yeniden gösterilebilir.
	now := time.Now()
	wp := &models.WordProgress{Box: 1}
	OnLeftSwipe(wp, now)

	if wp.Box != 1 {
		t.Errorf("kutu = %d, beklenen 1", wp.Box)
	}
	if wp.WrongCount != 1 {
		t.Errorf("WrongCount = %d, beklenen 1", wp.WrongCount)
	}
	if wp.NextReviewDate == nil || !wp.NextReviewDate.Equal(now) {
		t.Errorf("NextReviewDate = %v, beklenen %v", wp.NextReviewDate, now)
	}
}

func TestOnRightSwipe_YanlistanSonraSayaciSifirlar(t *testing.T) {
	wp := &models.WordProgress{Box: 4}
	now := time.Now()
	OnLeftSwipe(wp, now)
	OnLeftSwipe(wp, now)
	OnRightSwipe(wp, now)
	if wp.Box != 3 || wp.WrongCount != 0 {
		t.Errorf("kutu=%d wrong=%d, beklenen kutu=3 wrong=0 (4→3→2→3)", wp.Box, wp.WrongCount)
	}
}

func TestAcceptsSwipe(t *testing.T) {
	end := time.Date(2026, 3, 11, 0, 0, 0, 0, UsageLocation())
	at := func(d time.Duration) *time.Time { v := end.Add(d); return &v }

	cases := []struct {
		name string
		box  int
		next *time.Time
		want bool
	}{
		{"kutu 1 her zaman", 1, at(72 * time.Hour), true},
		{"kutu 2 tarihsiz", 2, nil, true},
		{"kutu 2 gecmis", 2, at(-72 * time.Hour), true},
		{"kutu 3 bugun 23:59", 3, at(-time.Minute), true},
		{"kutu 4 yarin 00:00", 4, at(0), false},
		{"kutu 5 bir ay sonra", 5, at(30 * 24 * time.Hour), false},
		{"kutu 6 ogrenilmis", 6, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wp := &models.WordProgress{Box: c.box, NextReviewDate: c.next}
			if got := AcceptsSwipe(wp, end); got != c.want {
				t.Errorf("AcceptsSwipe = %v, beklenen %v", got, c.want)
			}
		})
	}
}

// --- Açık zamanla (çevrimdışı cevap) ---

func TestLearnNewWord_VerilenAnaGore(t *testing.T) {
	loc := UsageLocation()
	// Çevrimdışı öğrenilmiş kelime: sunucuya iki gün sonra ulaşır; tarihler
	// sunucunun saatine değil cevabın anına göre yazılır.
	at := time.Date(2026, 11, 21, 23, 50, 0, 0, loc)
	wp := &models.WordProgress{Box: 1, WrongCount: 3}
	LearnNewWord(wp, at)

	if wp.Box != 2 || wp.WrongCount != 0 || wp.Learned {
		t.Fatalf("kutu=%d wrong=%d learned=%v, beklenen 2/0/false", wp.Box, wp.WrongCount, wp.Learned)
	}
	if wp.LastReviewDate == nil || !wp.LastReviewDate.Equal(at) {
		t.Errorf("LastReviewDate = %v, beklenen %v", wp.LastReviewDate, at)
	}
	if want := at.AddDate(0, 0, 3); wp.NextReviewDate == nil || !wp.NextReviewDate.Equal(want) {
		t.Errorf("NextReviewDate = %v, beklenen %v", wp.NextReviewDate, want)
	}
}

func TestOnRightSwipe_VerilenAnaGore(t *testing.T) {
	loc := UsageLocation()
	at := time.Date(2026, 11, 20, 8, 15, 0, 0, loc)
	cases := []struct {
		from, to int
		days     int // 0: kutu 6, tarih yok
	}{
		{1, 2, 3}, {2, 3, 7}, {3, 4, 15}, {4, 5, 30}, {5, 6, 0},
	}
	for _, c := range cases {
		wp := &models.WordProgress{Box: c.from, WrongCount: 1}
		OnRightSwipe(wp, at)
		if wp.Box != c.to {
			t.Errorf("kutu %d → %d, beklenen %d", c.from, wp.Box, c.to)
		}
		if wp.LastReviewDate == nil || !wp.LastReviewDate.Equal(at) {
			t.Errorf("kutu %d: LastReviewDate = %v, beklenen %v", c.from, wp.LastReviewDate, at)
		}
		if c.days == 0 {
			if wp.NextReviewDate != nil || !wp.Learned {
				t.Errorf("kutu %d: next=%v learned=%v, beklenen nil/true", c.from, wp.NextReviewDate, wp.Learned)
			}
			continue
		}
		if want := at.AddDate(0, 0, c.days); wp.NextReviewDate == nil || !wp.NextReviewDate.Equal(want) {
			t.Errorf("kutu %d: NextReviewDate = %v, beklenen %v", c.from, wp.NextReviewDate, want)
		}
	}
}

func TestOnLeftSwipe_IstanbulGeceYarisininIkiYani(t *testing.T) {
	loc := UsageLocation()
	cases := []struct {
		name string
		at   time.Time
		want time.Time
	}{
		// 23:59:59 İstanbul: hâlâ 22 Kasım; kelime 23 Kasım başında sorulur.
		{"gece yarisindan once", time.Date(2026, 11, 22, 23, 59, 59, 0, loc), time.Date(2026, 11, 23, 0, 0, 0, 0, loc)},
		// 00:00:00 İstanbul: artık 23 Kasım; kelime 24 Kasım başında sorulur.
		{"gece yarisi", time.Date(2026, 11, 23, 0, 0, 0, 0, loc), time.Date(2026, 11, 24, 0, 0, 0, 0, loc)},
		// 21:30 UTC = 00:30 İstanbul (23 Kasım): UTC günü değil İstanbul günü sayılır.
		{"utc hala dun", time.Date(2026, 11, 22, 21, 30, 0, 0, time.UTC), time.Date(2026, 11, 24, 0, 0, 0, 0, loc)},
		// 20:59 UTC = 23:59 İstanbul (22 Kasım).
		{"utc 20:59", time.Date(2026, 11, 22, 20, 59, 0, 0, time.UTC), time.Date(2026, 11, 23, 0, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wp := &models.WordProgress{Box: 4}
			OnLeftSwipe(wp, c.at)
			if wp.Box != 3 {
				t.Errorf("kutu = %d, beklenen 3", wp.Box)
			}
			if wp.NextReviewDate == nil || !wp.NextReviewDate.Equal(c.want) {
				t.Errorf("NextReviewDate = %v, beklenen %v", wp.NextReviewDate, c.want)
			}
		})
	}
}

// Dün verilmiş yanlış cevap (çevrimdışı) bugünün başında vadesi gelmiş olur:
// bugünün tekrar listesinde görünür ve bugün yeniden cevaplanabilir.
func TestOnLeftSwipe_DunkuCevapBugunBasindaVadeli(t *testing.T) {
	loc := UsageLocation()
	yesterday := time.Date(2026, 11, 22, 14, 0, 0, 0, loc)
	todayStart := time.Date(2026, 11, 23, 0, 0, 0, 0, loc)
	endOfToday := todayStart.AddDate(0, 0, 1)

	wp := &models.WordProgress{Box: 3}
	OnLeftSwipe(wp, yesterday)
	if wp.NextReviewDate == nil || !wp.NextReviewDate.Equal(todayStart) {
		t.Fatalf("NextReviewDate = %v, beklenen bugün başı %v", wp.NextReviewDate, todayStart)
	}
	if !AcceptsSwipe(wp, endOfToday) {
		t.Error("dünkü yanlış cevaptan sonra kelime bugün cevaplanamıyor")
	}
	// Dünün bitişine göre ise vadesi henüz gelmemiştir: aynı güne ait ikinci
	// bir çevrimdışı cevap kelimeyi ikinci kez değiştirmez.
	if AcceptsSwipe(wp, todayStart) {
		t.Error("dünkü ikinci cevap kabul edildi; kelime aynı gün iki kez işlenirdi")
	}
}

// İki ayrı çevrimdışı günün cevapları sırayla uygulanır: her biri kendi
// gününün bitişine göre denetlenir.
func TestAcceptsSwipe_IkiCevrimdisiGunSirayla(t *testing.T) {
	loc := UsageLocation()
	day1 := time.Date(2026, 11, 20, 10, 0, 0, 0, loc)
	day2 := time.Date(2026, 11, 21, 10, 0, 0, 0, loc)
	endOf := func(t time.Time) time.Time { return UsageDayStart(t).AddDate(0, 0, 1) }

	due := day1.Add(-time.Hour)
	wp := &models.WordProgress{Box: 3, NextReviewDate: &due}
	if !AcceptsSwipe(wp, endOf(day1)) {
		t.Fatal("1. gün: vadesi gelmiş kelime reddedildi")
	}
	OnLeftSwipe(wp, day1) // kutu 2, 21 Kasım başında
	if !AcceptsSwipe(wp, endOf(day2)) {
		t.Fatal("2. gün: dünkü yanlıştan sonra kelime reddedildi")
	}
	OnRightSwipe(wp, day2) // kutu 3, 7 gün sonra
	if wp.Box != 3 {
		t.Errorf("kutu = %d, beklenen 3", wp.Box)
	}
	if want := day2.AddDate(0, 0, 7); !wp.NextReviewDate.Equal(want) {
		t.Errorf("NextReviewDate = %v, beklenen %v", wp.NextReviewDate, want)
	}
	// Aynı (2.) güne ait üçüncü cevap no-op'tur.
	if AcceptsSwipe(wp, endOf(day2)) {
		t.Error("2. günün ikinci cevabı kabul edildi")
	}
}
