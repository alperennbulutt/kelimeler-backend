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
	LearnNewWord(wp)

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
		OnRightSwipe(wp)

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
			onLeftSwipe(wp, now)

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
	onLeftSwipe(wp, now)
	if wp.NextReviewDate == nil || !wp.NextReviewDate.Equal(want) {
		t.Errorf("NextReviewDate = %v, beklenen %v", wp.NextReviewDate, want)
	}
}

func TestOnLeftSwipe_Kutu1KutusundaKalir(t *testing.T) {
	// Öğrenme ekranındaki "Bilmiyorum": kelime henüz öğrenilmedi, kutu 2'ye
	// çıkmaz ve kutu 1'de hemen yeniden gösterilebilir.
	now := time.Now()
	wp := &models.WordProgress{Box: 1}
	onLeftSwipe(wp, now)

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
	OnLeftSwipe(wp)
	OnLeftSwipe(wp)
	OnRightSwipe(wp)
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
