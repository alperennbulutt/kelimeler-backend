package services

import "testing"

func badgeByID(all []ComputedBadge, id string) (ComputedBadge, bool) {
	for _, b := range all {
		if b.ID == id {
			return b, true
		}
	}
	return ComputedBadge{}, false
}

func TestComputeBadges_FirstDayHepKazanilmis(t *testing.T) {
	all, _ := ComputeBadges(BadgeInput{}, nil)

	b, ok := badgeByID(all, "first_day")
	if !ok {
		t.Fatal("first_day rozeti üretilmedi")
	}
	if !b.IsEarned {
		t.Error("first_day kazanılmamış görünüyor, her zaman kazanılmış olmalı")
	}
}

func TestComputeBadges_EsiklerdeKazanilir(t *testing.T) {
	in := BadgeInput{
		CurrentStreak:    7,
		TotalActiveCount: 250,
		LearnedCount:     10,
		ActiveByLevel:    map[string]int{"A1": 600},
	}
	all, _ := ComputeBadges(in, nil)

	kazanilmali := []string{
		"streak_3", "streak_7", // 7 >= 3 ve 7
		"words_100", "words_250", // 250 >= 100 ve 250
		"box_mastery_1", "box_mastery_10", // 10 >= 1 ve 10
		"level_A1", // 600 >= 600 (tam eşik)
	}
	for _, id := range kazanilmali {
		b, ok := badgeByID(all, id)
		if !ok {
			t.Errorf("%s rozeti üretilmedi", id)
			continue
		}
		if !b.IsEarned {
			t.Errorf("%s kazanılmalıydı", id)
		}
	}

	kazanilmamali := []string{
		"streak_14",       // 7 < 14
		"words_500",       // 250 < 500
		"box_mastery_100", // 10 < 100
		"level_A2",        // ActiveByLevel'da yok
	}
	for _, id := range kazanilmamali {
		b, ok := badgeByID(all, id)
		if !ok {
			t.Errorf("%s rozeti üretilmedi", id)
			continue
		}
		if b.IsEarned {
			t.Errorf("%s kazanılmamalıydı", id)
		}
	}
}

func TestComputeBadges_YeniKazanilanlarSadeceBirKezBildirilir(t *testing.T) {
	in := BadgeInput{CurrentStreak: 3}

	// İlk hesapta streak_3 yeni kazanılmış olarak bildirilmeli.
	_, yeni := ComputeBadges(in, nil)
	if !contains(yeni, "streak_3") {
		t.Fatalf("streak_3 yeni kazanılanlarda yok: %v", yeni)
	}

	// Daha önce kazanılmışsa tekrar "yeni" sayılmamalı (mükerrer bildirim olmasın).
	_, yeni2 := ComputeBadges(in, map[string]bool{"streak_3": true})
	if contains(yeni2, "streak_3") {
		t.Errorf("streak_3 tekrar yeni olarak bildirildi: %v", yeni2)
	}
}

func TestComputeBadges_OncedenKazanilanKorunur(t *testing.T) {
	// Streak sıfırlansa bile geçmişte kazanılan rozet kazanılmış kalmalı.
	all, _ := ComputeBadges(BadgeInput{CurrentStreak: 0}, map[string]bool{"streak_30": true})

	b, ok := badgeByID(all, "streak_30")
	if !ok {
		t.Fatal("streak_30 rozeti üretilmedi")
	}
	if !b.IsEarned {
		t.Error("streak_30 kazanılmış kalmalıydı, streak düşse bile geri alınmaz")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
