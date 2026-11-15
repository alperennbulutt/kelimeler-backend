package services

import (
	"errors"
	"testing"
	"time"
)

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
		ActiveByLevel:    map[string]int{"A1": 595},
		LevelTotals:      map[string]int{"A1": 595, "A2": 1200},
	}
	all, _ := ComputeBadges(in, nil)

	kazanilmali := []string{
		"streak_3", "streak_7", // 7 >= 3 ve 7
		"words_100", "words_250", // 250 >= 100 ve 250
		"box_mastery_1", "box_mastery_10", // 10 >= 1 ve 10
		"level_A1", // 595 >= 595 (sözlükteki tam sayı)
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

func TestComputeBadges_SeviyeHedefiSozluktekiSayi(t *testing.T) {
	sozluk := map[string]int{"A1": 595, "A2": 1200, "B1": 2000, "B2": 2240, "C1": 2000, "C2": 1940}

	cases := []struct {
		name   string
		level  string
		active int
		totals map[string]int
		want   bool
	}{
		// Eski sabit 2200'dü: B2 rozeti 40 kelime eksikken verilip C1'i açıyordu.
		{"B2 eski esikte kazanilmaz", "B2", 2200, sozluk, false},
		{"B2 tum kelimelerle kazanilir", "B2", 2240, sozluk, true},
		// Eski sabit 2000'di: 1940 kelimelik C2 rozeti hiç kazanılamıyordu.
		{"C2 tum kelimelerle kazanilir", "C2", 1940, sozluk, true},
		{"A1 bir eksikle kazanilmaz", "A1", 594, sozluk, false},
		// Sözlük boşsa (0 >= 0) seviye tamamlanmış sayılmamalı.
		{"sozlukte kelime yoksa kazanilmaz", "C1", 0, map[string]int{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			all, _ := ComputeBadges(BadgeInput{
				ActiveByLevel: map[string]int{c.level: c.active},
				LevelTotals:   c.totals,
			}, nil)
			b, ok := badgeByID(all, "level_"+c.level)
			if !ok {
				t.Fatalf("level_%s rozeti üretilmedi", c.level)
			}
			if b.IsEarned != c.want {
				t.Errorf("level_%s IsEarned = %v, beklenen %v", c.level, b.IsEarned, c.want)
			}
		})
	}
}

func TestTotalWordsByLevel_SozlukYoksaEskiSabit(t *testing.T) {
	got := TotalWordsByLevel(map[string]int{"A1": 595, "C2": 1940})
	want := map[string]int{"A1": 595, "A2": 1200, "B1": 2000, "B2": 2200, "C1": 2000, "C2": 1940}
	for lvl, n := range want {
		if got[lvl] != n {
			t.Errorf("TotalWordsByLevel[%s] = %d, beklenen %d", lvl, got[lvl], n)
		}
	}
	if len(got) != len(Levels) {
		t.Errorf("TotalWordsByLevel %d seviye içeriyor, beklenen %d: %v", len(got), len(Levels), got)
	}
}

func TestCachedLevelTotals(t *testing.T) {
	ResetLevelTotalsCache()
	t.Cleanup(ResetLevelTotalsCache)

	yukleme := 0
	sonuc := map[string]int{"A1": 595}
	var hata error
	load := func() (map[string]int, error) {
		yukleme++
		if hata != nil {
			return nil, hata
		}
		return sonuc, nil
	}
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	// Boş sözlük önbelleğe alınmaz: seed bitince ilk istek gerçek sayıyı görür.
	sonuc = map[string]int{}
	if got, err := CachedLevelTotals(t0, load); err != nil || len(got) != 0 {
		t.Fatalf("boş sözlük = %v, %v", got, err)
	}
	// Hata da önbelleğe alınmaz.
	hata = errors.New("db yok")
	if _, err := CachedLevelTotals(t0, load); err == nil {
		t.Fatal("yükleme hatası dönmedi")
	}
	hata, sonuc = nil, map[string]int{"A1": 595}

	got, err := CachedLevelTotals(t0, load)
	if err != nil || got["A1"] != 595 {
		t.Fatalf("CachedLevelTotals = %v, %v", got, err)
	}
	// Dönen kopyayı değiştirmek önbelleği bozmamalı.
	got["A1"] = 1

	sonuc = map[string]int{"A1": 600}
	if got, _ := CachedLevelTotals(t0.Add(LevelTotalsTTL-time.Second), load); got["A1"] != 595 {
		t.Errorf("süre dolmadan önbellek = %v, beklenen A1=595", got)
	}
	if yukleme != 3 {
		t.Errorf("süre dolmadan %d yükleme, beklenen 3", yukleme)
	}
	if got, _ := CachedLevelTotals(t0.Add(LevelTotalsTTL), load); got["A1"] != 600 {
		t.Errorf("süre dolunca önbellek = %v, beklenen A1=600 (yeniden yüklenmeli)", got)
	}
}
