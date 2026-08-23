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

func TestOnLeftSwipe_UcuncuHatadaDuser(t *testing.T) {
	wp := &models.WordProgress{Box: 4, WrongCount: 0}

	// İlk iki hata yalnızca sayacı artırır.
	OnLeftSwipe(wp)
	if wp.Box != 4 || wp.WrongCount != 1 {
		t.Fatalf("1. hata: kutu=%d wrong=%d, beklenen kutu=4 wrong=1", wp.Box, wp.WrongCount)
	}
	OnLeftSwipe(wp)
	if wp.Box != 4 || wp.WrongCount != 2 {
		t.Fatalf("2. hata: kutu=%d wrong=%d, beklenen kutu=4 wrong=2", wp.Box, wp.WrongCount)
	}

	// Üçüncü hata bir alt kutuya düşürür ve sayacı sıfırlar.
	OnLeftSwipe(wp)
	if wp.Box != 3 {
		t.Errorf("3. hata: kutu = %d, beklenen 3", wp.Box)
	}
	if wp.WrongCount != 0 {
		t.Errorf("3. hata: WrongCount = %d, düşünce sıfırlanmalı", wp.WrongCount)
	}
}

func TestOnLeftSwipe_Kutu2TabandirDusmez(t *testing.T) {
	// Kutu 2 tabandır: 3 hata olsa da daha aşağı inmez, sayaç artmaya devam eder.
	wp := &models.WordProgress{Box: 2, WrongCount: 2}
	OnLeftSwipe(wp)

	if wp.Box != 2 {
		t.Errorf("kutu = %d, beklenen 2 (taban)", wp.Box)
	}
	if wp.WrongCount != 3 {
		t.Errorf("WrongCount = %d, beklenen 3 (taban kutuda sıfırlanmaz)", wp.WrongCount)
	}
	if wp.NextReviewDate == nil {
		t.Error("NextReviewDate nil, taban kutuda da tekrar planlanmalı")
	}
}
