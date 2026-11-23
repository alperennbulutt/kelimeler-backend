package handlers

import (
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Bu dosya grup açılışlarının günlük geçmişini (daily_group_unlocks) gerçek
// PostgreSQL'e karşı sınar. Kurulum için usage_integration_test.go başındaki
// nota bakın; TEST_DATABASE_URL yoksa testler atlanır.

// gunlukAcilis, daily_group_unlocks'taki bir satırdır.
type gunlukAcilis struct {
	Day   string
	Level string
	Grp   int
	Via   string
}

// gunlukAcilislar, kullanıcının açılış geçmişini gün ve grup sırasıyla döner.
func gunlukAcilislar(t *testing.T, gdb *gorm.DB, userID string) []gunlukAcilis {
	t.Helper()

	var rows []gunlukAcilis
	if err := gdb.Raw(`SELECT to_char(usage_date, 'YYYY-MM-DD') AS day, level, grp, via
		FROM daily_group_unlocks WHERE user_id = ? ORDER BY usage_date, level, grp`, userID).
		Scan(&rows).Error; err != nil {
		t.Fatalf("açılış geçmişi okunamadı: %v", err)
	}
	return rows
}

func TestUnlockGroup_GunlukGecmiseYazilir(t *testing.T) {
	gdb, u, app := newGateTest(t, config.GateEnforce)
	today := services.UsageDate(time.Now())

	for _, req := range []struct {
		path  string
		group int
	}{
		{"/users/me/usage/group-started", 1},
		{"/users/me/usage/group-started", 1}, // yanıtı kaybolan deneme
		{"/users/me/usage/group-unlocked-by-ad", 2},
		{"/users/me/usage/group-unlocked-by-ad", 2},
	} {
		if status, body := postJSON(t, app, req.path, fiber.Map{"level": "A1", "group": req.group}); status != fiber.StatusOK {
			t.Fatalf("%s A1:%d status = %d (%v)", req.path, req.group, status, body)
		}
	}
	got := gunlukAcilislar(t, gdb, u.ID)
	want := []gunlukAcilis{{today, "A1", 1, "free"}, {today, "A1", 2, "ad"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("geçmiş = %+v, beklenen %+v", got, want)
	}
}

func TestUnlockGroup_ReddedilenAcilisGecmiseYazilmaz(t *testing.T) {
	kapiRaporunuYakala(t)
	gdb, u, app := newGateTest(t, config.GateEnforce)
	setUsage(t, gdb, u.ID, map[string]any{"groups_started_today_count": 1, "unlocked_groups_today": "A1:1"})

	status, body := postJSON(t, app, "/users/me/usage/group-started", fiber.Map{"level": "A1", "group": 2})
	if status != fiber.StatusForbidden {
		t.Fatalf("status = %d, beklenen 403 (%v)", status, body)
	}
	for _, r := range gunlukAcilislar(t, gdb, u.ID) {
		if r.Grp == 2 {
			t.Errorf("reddedilen açılış geçmişe yazıldı: %+v", r)
		}
	}
}

// Bu tablodan önce bugün açılmış grup (kümede var, satırı yok): yeniden
// girişte satır tamamlanır, sayaç değişmez.
func TestUnlockGroup_KumedekiGrubunEksikSatiriTamamlanir(t *testing.T) {
	gdb, u, app := newGateTest(t, config.GateEnforce)
	setUsage(t, gdb, u.ID, map[string]any{"groups_started_today_count": 1, "unlocked_groups_today": "A1:4"})

	if status, body := postJSON(t, app, "/users/me/usage/group-started", fiber.Map{"level": "A1", "group": 4}); status != fiber.StatusOK {
		t.Fatalf("status = %d (%v)", status, body)
	}
	if after := loadUser(t, gdb, u.ID); after.GroupsStartedTodayCount != 1 {
		t.Errorf("sayaç = %d, beklenen 1", after.GroupsStartedTodayCount)
	}
	got := gunlukAcilislar(t, gdb, u.ID)
	if len(got) != 1 || got[0].Grp != 4 || got[0].Day != services.UsageDate(time.Now()) {
		t.Errorf("geçmiş = %+v, beklenen bugün A1:4", got)
	}
}
