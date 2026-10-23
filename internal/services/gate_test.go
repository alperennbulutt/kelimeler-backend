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
