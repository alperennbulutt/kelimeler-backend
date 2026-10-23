package services

import (
	"fmt"
	"sync"
	"time"
)

// Eşikler — stats_repository._calculateBadges ile birebir aynı.
var (
	StreakDays           = []int{3, 7, 14, 30, 100, 365}
	WordCountThresholds  = []int{100, 250, 500, 1000, 2500, 5000, 10000}
	BoxMasteryThresholds = []int{1, 10, 100, 1000, 10000}
)

// FallbackLevelTotals, istemcinin eski sabit seviye toplamlarıdır. Seviye
// rozeti hedefi DEĞİLDİR (hedef sözlükteki gerçek sayıdır, bkz. LevelTotals);
// yalnızca sözlükte o seviye için kelime yokken totalWordsByLevel'da sıfıra
// bölünmesin diye döner.
var FallbackLevelTotals = map[string]int{"A1": 600, "A2": 1200, "B1": 2000, "B2": 2200, "C1": 2000, "C2": 2000}

var levelTitles = map[string]string{
	"A1": "A1 Ustası", "A2": "A2 Ustası", "B1": "B1 Şampiyonu",
	"B2": "B2 Şampiyonu", "C1": "C1 Bilgesi", "C2": "C2 Efsanesi",
}

// BadgeDef, bir rozetin sabit tanımı.
type BadgeDef struct {
	ID       string
	Title    string
	Subtitle string
	IconKey  string
	Category string
}

// ComputedBadge, hesaplanmış rozet (kazanılma durumuyla).
type ComputedBadge struct {
	BadgeDef
	IsEarned bool
}

// BadgeInput, rozet hesabı için gerekli sayılar.
type BadgeInput struct {
	// ActiveByLevel, sözlükteki seviyeye göre kullanıcının box>=2 kelime sayısı
	// (sözlükle eşleşmeyen ilerleme satırları sayılmaz).
	ActiveByLevel map[string]int
	// LevelTotals, sözlükteki seviye başına kelime sayısı (seviye rozeti hedefi).
	LevelTotals      map[string]int
	TotalActiveCount int // box>=2 toplam
	LearnedCount     int // box==6 toplam
	CurrentStreak    int
}

// ComputeBadges, tüm rozetleri ve bu çağrıda yeni kazanılanları hesaplar.
func ComputeBadges(in BadgeInput, earnedBefore map[string]bool) (all []ComputedBadge, newlyEarned []string) {
	add := func(def BadgeDef, earnedNow bool) {
		isEarned := earnedNow || earnedBefore[def.ID]
		if earnedNow && !earnedBefore[def.ID] {
			newlyEarned = append(newlyEarned, def.ID)
		}
		all = append(all, ComputedBadge{BadgeDef: def, IsEarned: isEarned})
	}

	// 1) Seviye tamamlama: seviyenin sözlükteki bütün kelimeleri box>=2. Bu,
	// haritadaki "seviyenin tüm grupları tamamlandı" ölçütüyle aynıdır;
	// map_handler bu rozeti önceki seviye tamamlandı diye okur. Sözlükte kelimesi
	// olmayan seviye tamamlanmış sayılmaz.
	for _, lvl := range Levels {
		total := in.LevelTotals[lvl]
		add(BadgeDef{
			ID:       "level_" + lvl,
			Title:    levelTitles[lvl],
			Subtitle: lvl + " seviyesi tamamlandı",
			IconKey:  "crown",
			Category: "level",
		}, total > 0 && in.ActiveByLevel[lvl] >= total)
	}

	// 2) Streak
	for _, d := range StreakDays {
		add(BadgeDef{
			ID:       fmt.Sprintf("streak_%d", d),
			Title:    fmt.Sprintf("%d Günlük Streak", d),
			Subtitle: fmt.Sprintf("%d gün üst üste çalışma", d),
			IconKey:  "fire",
			Category: "streak",
		}, in.CurrentStreak >= d)
	}

	// 3) Kelime öğrenme (box>=2 toplam)
	for _, c := range WordCountThresholds {
		add(BadgeDef{
			ID:       fmt.Sprintf("words_%d", c),
			Title:    fmt.Sprintf("%d Kelime", c),
			Subtitle: fmt.Sprintf("%d kelime öğreniliyor", c),
			IconKey:  "medal",
			Category: "wordCount",
		}, in.TotalActiveCount >= c)
	}

	// 4) Kutu başarısı (box==6 toplam)
	for _, c := range BoxMasteryThresholds {
		title, subtitle := boxMasteryText(c)
		add(BadgeDef{
			ID:       fmt.Sprintf("box_mastery_%d", c),
			Title:    title,
			Subtitle: subtitle,
			IconKey:  "box",
			Category: "boxMastery",
		}, in.LearnedCount >= c)
	}

	// 5) Davranış (first_day her zaman kazanılmış)
	add(BadgeDef{
		ID:       "first_day",
		Title:    "İlk Günüm",
		Subtitle: "Uygulamayı kullanmaya başladın",
		IconKey:  "star",
		Category: "behavior",
	}, true)

	return all, newlyEarned
}

func boxMasteryText(count int) (string, string) {
	switch count {
	case 1:
		return "İlk Adım", "İlk kelimenizi tamamen öğrendiniz"
	case 10:
		return "Hızlı Başlangıç", "10 kelime tamamen öğrenildi"
	case 100:
		return "Yüzlük Kulüp", "100 kelime tamamen öğrenildi"
	case 1000:
		return "Bin Kelime Ustası", "1000 kelime tamamen öğrenildi"
	case 10000:
		return "On Bin Efsanesi", "10000 kelime tamamen öğrenildi"
	default:
		return fmt.Sprintf("%d Kelime Ustası", count), fmt.Sprintf("%d kelime tamamen öğrenildi", count)
	}
}

// TotalWordsByLevel, istemcinin ilerleme paydası (totalWordsByLevel): sözlükteki
// gerçek sayı, sözlükte kelimesi olmayan seviyede eski sabit.
func TotalWordsByLevel(levelTotals map[string]int) map[string]int {
	out := make(map[string]int, len(Levels))
	for _, lvl := range Levels {
		if n := levelTotals[lvl]; n > 0 {
			out[lvl] = n
		} else {
			out[lvl] = FallbackLevelTotals[lvl]
		}
	}
	return out
}

// LevelTotalsTTL, seviye toplamlarının önbellekte tutulma süresidir. Sözlük
// yalnızca seed ya da import ile değişir; import ayrı bir süreç olduğu için
// sunucu yeni sayıları en geç bu süre sonunda görür.
const LevelTotalsTTL = 10 * time.Minute

var levelTotalsCache struct {
	sync.Mutex
	totals   map[string]int
	loadedAt time.Time
}

// CachedLevelTotals, seviye başına sözlük kelime sayısını önbellekten döner;
// önbellek boşsa ya da süresi dolmuşsa load ile yeniler. Hata ve boş sonuç
// önbelleğe alınmaz (sözlük henüz yüklenmemiş olabilir). Dönen map kopyadır.
func CachedLevelTotals(now time.Time, load func() (map[string]int, error)) (map[string]int, error) {
	c := &levelTotalsCache
	c.Lock()
	defer c.Unlock()

	if c.totals == nil || now.Sub(c.loadedAt) >= LevelTotalsTTL || now.Before(c.loadedAt) {
		totals, err := load()
		if err != nil {
			return nil, err
		}
		if len(totals) == 0 {
			return map[string]int{}, nil
		}
		c.totals, c.loadedAt = totals, now
	}

	out := make(map[string]int, len(c.totals))
	for k, v := range c.totals {
		out[k] = v
	}
	return out, nil
}

// ResetLevelTotalsCache, seviye toplamları önbelleğini boşaltır (sözlük aynı
// süreçte değiştiğinde ve testlerde).
func ResetLevelTotalsCache() {
	levelTotalsCache.Lock()
	levelTotalsCache.totals = nil
	levelTotalsCache.Unlock()
}
