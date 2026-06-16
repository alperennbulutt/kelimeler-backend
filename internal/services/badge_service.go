package services

import "fmt"

// Eşikler — stats_repository._calculateBadges ile birebir aynı.
var (
	LevelTargets         = map[string]int{"A1": 600, "A2": 1200, "B1": 2000, "B2": 2200, "C1": 2000, "C2": 2000}
	StreakDays           = []int{3, 7, 14, 30, 100, 365}
	WordCountThresholds  = []int{100, 250, 500, 1000, 2500, 5000, 10000}
	BoxMasteryThresholds = []int{1, 10, 100, 1000, 10000}
)

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
	ActiveByLevel    map[string]int // box>=2, seviye kırılımı
	TotalActiveCount int            // box>=2 toplam
	LearnedCount     int            // box==6 toplam
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

	// 1) Seviye tamamlama (activeByLevel >= target)
	for _, lvl := range Levels {
		add(BadgeDef{
			ID:       "level_" + lvl,
			Title:    levelTitles[lvl],
			Subtitle: lvl + " seviyesi tamamlandı",
			IconKey:  "crown",
			Category: "level",
		}, in.ActiveByLevel[lvl] >= LevelTargets[lvl])
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
