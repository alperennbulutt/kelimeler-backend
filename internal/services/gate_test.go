package services

import (
	"reflect"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/models"
)

func TestGateActionFor_KipVeKarar(t *testing.T) {
	cases := []struct {
		mode    config.GateEnforcement
		allowed bool
		want    GateAction
	}{
		{config.GateOff, true, GateAllow},
		{config.GateOff, false, GateAllow},
		{config.GateLog, true, GateAllow},
		{config.GateLog, false, GateAllowAndReport},
		{config.GateEnforce, true, GateAllow},
		{config.GateEnforce, false, GateReject},
		// Elle kurulan Config'te alan boş kalabilir: varsayılan kip (log).
		{"", true, GateAllow},
		{"", false, GateAllowAndReport},
		{"bilinmeyen", false, GateAllowAndReport},
	}
	for _, c := range cases {
		if got := GateActionFor(c.mode, c.allowed); got != c.want {
			t.Errorf("GateActionFor(%q, %v) = %v, beklenen %v", c.mode, c.allowed, got, c.want)
		}
	}
}

func TestNormalizeGateMode(t *testing.T) {
	for in, want := range map[config.GateEnforcement]config.GateEnforcement{
		config.GateOff:     config.GateOff,
		config.GateLog:     config.GateLog,
		config.GateEnforce: config.GateEnforce,
		"":                 config.DefaultGateEnforcement,
		"ENFORCE":          config.DefaultGateEnforcement,
	} {
		if got := NormalizeGateMode(in); got != want {
			t.Errorf("NormalizeGateMode(%q) = %q, beklenen %q", in, got, want)
		}
	}
}

// gateUser, bugünün içinde (ya da verilen anda) yazılmış sayaçlarla bir
// kullanıcı satırıdır.
func gateUser(lastReset time.Time, unlocked string, started, byAd, answers, passes int) *models.User {
	return &models.User{
		UnlockedGroupsToday:     unlocked,
		GroupsStartedTodayCount: started,
		GroupsUnlockedByAdToday: byAd,
		ReviewAnswersToday:      answers,
		ReviewAdPassCountToday:  passes,
		LastResetDate:           lastReset.UnixMilli(),
	}
}

func TestGateStateAt_BugunkuSatirOldugaGibi(t *testing.T) {
	kullanimKonumu(t, "Europe/Istanbul")
	now := utc(t, "2026-10-23T12:00:00Z")

	s := GateStateAt(gateUser(now.Add(-time.Hour), "A1:1,A1:2", 1, 1, 42, 1), now)
	want := DailyGateState{
		UnlockedGroups: []string{"A1:1", "A1:2"},
		GroupsOpened:   2,
		ReviewAnswers:  42,
		ReviewAdPasses: 1,
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("GateStateAt = %+v, beklenen %+v", s, want)
	}
}

func TestGateStateAt_BayatSatirSifirSayilir(t *testing.T) {
	kullanimKonumu(t, "Europe/Istanbul")
	// Öğlen; dünkü kayıt gece yarısı toleransının dışında.
	now := utc(t, "2026-10-23T12:00:00Z")

	s := GateStateAt(gateUser(now.AddDate(0, 0, -1), "A1:1", 1, 2, 95, 3), now)
	if len(s.UnlockedGroups) != 0 || s.GroupsOpened != 0 || s.ReviewAnswers != 0 || s.ReviewAdPasses != 0 {
		t.Errorf("bayat satır sıfır sayılmadı: %+v", s)
	}
	if len(s.CarriedOverGroups) != 0 {
		t.Errorf("tolerans dışında dünkü gruplar taşındı: %v", s.CarriedOverGroups)
	}
	if s.UnlockedGroups == nil {
		t.Error("UnlockedGroups nil olmamalı")
	}
}

func TestLearnAllowedToday(t *testing.T) {
	s := DailyGateState{UnlockedGroups: []string{"A1:1", "A2:3"}, CarriedOverGroups: []string{"B1:4"}}
	for key, want := range map[string]bool{
		"A1:1":  true,
		"A2:3":  true,
		"B1:4":  true, // gece yarısı toleransı
		"A1:2":  false,
		"A1:10": false,
		"":      false,
	} {
		if got := LearnAllowedToday(s, key); got != want {
			t.Errorf("LearnAllowedToday(%q) = %v, beklenen %v", key, got, want)
		}
	}
}

func TestKeylessUnlockSlots(t *testing.T) {
	cases := []struct {
		opened   int
		unlocked []string
		want     int
	}{
		{0, nil, 0},
		{1, []string{"A1:1"}, 0},
		{2, []string{"A1:1"}, 1},
		{3, nil, 3},
		// Sayaç sıfırlanmış ama küme dolu kalamaz; yine de negatif dönmez.
		{0, []string{"A1:1"}, 0},
	}
	for _, c := range cases {
		s := DailyGateState{GroupsOpened: c.opened, UnlockedGroups: c.unlocked}
		if got := KeylessUnlockSlots(s); got != c.want {
			t.Errorf("KeylessUnlockSlots(opened=%d, %v) = %d, beklenen %d", c.opened, c.unlocked, got, c.want)
		}
	}
}

func TestKeylessSlotCovers(t *testing.T) {
	cases := []struct {
		name     string
		slots    int
		learned  []string
		unlocked []string
		key      string
		want     bool
	}{
		{"bos hak ilk grup", 1, nil, nil, "A1:1", true},
		{"hak ayni gruba harcanmis", 1, []string{"A1:1"}, nil, "A1:1", true},
		{"hak baska gruba harcanmis", 1, []string{"A1:1"}, nil, "A1:2", false},
		{"iki hak ikinci grup", 2, []string{"A1:1"}, nil, "A1:2", true},
		{"anahtarli grup hak harcamaz", 1, []string{"A1:1"}, []string{"A1:1"}, "A1:2", true},
		{"hak yok", 0, nil, nil, "A1:1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := KeylessSlotCovers(c.slots, c.learned, c.unlocked, c.key); got != c.want {
				t.Errorf("KeylessSlotCovers = %v, beklenen %v", got, c.want)
			}
		})
	}
}

func TestReviewAnswerAllowed_Temel(t *testing.T) {
	cases := []struct {
		answers, passes int
		want            bool
	}{
		{0, 0, true},
		{30, 0, false},
		{30, 1, true},
		{60, 1, false},
		{90, 3, true},
		{500, 3, true},
		// Gövdesiz eski kayıtlar sayacı 3'ün üstüne çıkarabilir; 90'ın
		// ötesini yine açmaz ama kapatmaz da.
		{200, 7, true},
	}
	for _, c := range cases {
		if got := ReviewAnswerAllowed(c.answers, c.passes); got != c.want {
			t.Errorf("ReviewAnswerAllowed(%d, %d) = %v, beklenen %v", c.answers, c.passes, got, c.want)
		}
	}
}

func TestReviewCheckpointForAnswer_Sinirlar(t *testing.T) {
	cases := map[int]int{
		1: 0, 29: 0, 30: 0,
		31: 30, 59: 30, 60: 30,
		61: 60, 89: 60, 90: 60,
		91: 90, 92: 90, 500: 90,
	}
	for n, want := range cases {
		if got := ReviewCheckpointForAnswer(n); got != want {
			t.Errorf("ReviewCheckpointForAnswer(%d) = %d, beklenen %d", n, got, want)
		}
	}
}

// TestReviewGate_KontrolNoktasiSinirlariHerKipte, sayılan cevaptan ÖNCEKİ
// günlük cevap sayısıyla (answers) kararı ve her kipteki sonucu sabitler:
// answers=29 → 30. cevap (ücretsiz), answers=30 → 31. cevap (30 kapısı).
func TestReviewGate_KontrolNoktasiSinirlariHerKipte(t *testing.T) {
	cases := []struct {
		answers, passes int
		allowed         bool
	}{
		{29, 0, true}, {30, 0, false}, {31, 0, false},
		{29, 1, true}, {30, 1, true}, {31, 1, true},
		{59, 1, true}, {60, 1, false}, {61, 1, false},
		{59, 2, true}, {60, 2, true}, {61, 2, true},
		{89, 2, true}, {90, 2, false}, {91, 2, false},
		{89, 3, true}, {90, 3, true}, {91, 3, true},
		// Kapı atlanarak geçilemez: 60 kapısında bir geçiş yetmez.
		{60, 0, false}, {90, 1, false},
		// Sırayla geçilmeden 90 bildirildiyse sayaç 3'tür (RecordReviewAdPass).
		{75, 3, true},
	}
	want := map[config.GateEnforcement]map[bool]GateAction{
		config.GateOff:     {true: GateAllow, false: GateAllow},
		config.GateLog:     {true: GateAllow, false: GateAllowAndReport},
		config.GateEnforce: {true: GateAllow, false: GateReject},
	}
	for _, c := range cases {
		got := ReviewAnswerAllowed(c.answers, c.passes)
		if got != c.allowed {
			t.Errorf("ReviewAnswerAllowed(%d, %d) = %v, beklenen %v", c.answers, c.passes, got, c.allowed)
		}
		for mode, actions := range want {
			if a := GateActionFor(mode, got); a != actions[c.allowed] {
				t.Errorf("kip %s, cevap=%d geçiş=%d: eylem %v, beklenen %v", mode, c.answers, c.passes, a, actions[c.allowed])
			}
		}
	}
}

func TestLearnGate_HerKipteKarar(t *testing.T) {
	s := DailyGateState{UnlockedGroups: []string{"A1:1"}}
	cases := []struct {
		mode config.GateEnforcement
		key  string
		want GateAction
	}{
		{config.GateOff, "A1:1", GateAllow},
		{config.GateOff, "A1:2", GateAllow},
		{config.GateLog, "A1:1", GateAllow},
		{config.GateLog, "A1:2", GateAllowAndReport},
		{config.GateEnforce, "A1:1", GateAllow},
		{config.GateEnforce, "A1:2", GateReject},
	}
	for _, c := range cases {
		if got := GateActionFor(c.mode, LearnAllowedToday(s, c.key)); got != c.want {
			t.Errorf("kip %s, %s: %v, beklenen %v", c.mode, c.key, got, c.want)
		}
	}
}

func TestGateStateAt_IstanbulGeceYarisi(t *testing.T) {
	kullanimKonumu(t, "Europe/Istanbul")
	// 23 Ekim 23:59:59 TSİ'de yazılmış satır: 29 cevap, A1:3 açık.
	lastReset := utc(t, "2026-10-23T20:59:59Z")
	u := gateUser(lastReset, "A1:3", 1, 0, 95, 0)

	cases := []struct {
		name        string
		now         string
		wantAnswers int
		wantToday   bool // A1:3 bugün açık
		wantCarried bool // A1:3 gece yarısı toleransında
	}{
		{"ayni gun son an", "2026-10-23T20:59:59.999Z", 95, true, false},
		{"gece yarisi", "2026-10-23T21:00:00Z", 0, false, true},
		{"toleransin son ani", "2026-10-23T21:59:59Z", 0, false, true},
		{"tolerans bitti", "2026-10-23T22:00:00Z", 0, false, false},
		// 03:00 TSİ = 00:00 UTC: UTC günü döner ama kullanım günü çoktan dönmüştür.
		{"utc gun donumu", "2026-10-24T00:00:00Z", 0, false, false},
		{"iki gun sonra gece yarisi", "2026-10-24T21:10:00Z", 0, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := GateStateAt(u, utc(t, c.now))
			if s.ReviewAnswers != c.wantAnswers {
				t.Errorf("ReviewAnswers = %d, beklenen %d", s.ReviewAnswers, c.wantAnswers)
			}
			if got := containsKey(s.UnlockedGroups, "A1:3"); got != c.wantToday {
				t.Errorf("bugün açık = %v, beklenen %v", got, c.wantToday)
			}
			if got := containsKey(s.CarriedOverGroups, "A1:3"); got != c.wantCarried {
				t.Errorf("toleransta = %v, beklenen %v", got, c.wantCarried)
			}
			if got := LearnAllowedToday(s, "A1:3"); got != (c.wantToday || c.wantCarried) {
				t.Errorf("LearnAllowedToday = %v", got)
			}
			// Yeni günde tekrar kapısı sıfırdan başlar.
			if !NeedsResetAt(u.LastResetDate, utc(t, c.now)) {
				return
			}
			if !ReviewAnswerAllowed(s.ReviewAnswers, s.ReviewAdPasses) {
				t.Error("yeni günün ilk tekrar cevabı reddedildi")
			}
			if KeylessUnlockSlots(s) != 0 {
				t.Error("dünkü açılışlar bugüne hak olarak taşındı")
			}
		})
	}
}

func TestGateStateAt_YeniKullanici(t *testing.T) {
	// Hiç sıfırlanmamış satır (last_reset_date = 0): her şey sıfır, tolerans yok.
	s := GateStateAt(&models.User{UnlockedGroupsToday: "A1:1"}, time.Now())
	if len(s.UnlockedGroups) != 0 || len(s.CarriedOverGroups) != 0 || s.ReviewAnswers != 0 {
		t.Errorf("yeni kullanıcı durumu = %+v", s)
	}
}
