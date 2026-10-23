package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Bu dosya grup akışını (advance-group, current-group, harita grupları)
// gerçek PostgreSQL'e karşı sınar. Kurulum için usage_integration_test.go
// başındaki nota bakın; TEST_DATABASE_URL yoksa testler atlanır.
//
// Testler sözlüğe A1/A2/C2 seviyeleri için kendi kelimelerini yazar; bu
// seviyelerde başka kelime bulunan (seed yüklenmiş) veritabanında atlanırlar.

func groupApp(h *Handlers, userID string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	inject := func(c *fiber.Ctx) error {
		c.Locals("userID", userID)
		return c.Next()
	}
	app.Post("/users/me/advance-group", inject, h.AdvanceGroup)
	app.Put("/users/me/current-group", inject, h.SetCurrentGroup)
	app.Get("/map/levels/:level/groups", inject, h.LevelGroups)
	app.Get("/map/levels/:level/max-group", inject, h.MaxGroupForLevel)
	app.Get("/home/current-group", inject, h.CurrentGroup)
	return app
}

func newGroupTest(t *testing.T, cfg *config.Config) (*gorm.DB, *models.User, *fiber.App) {
	t.Helper()

	gdb := testDB(t)
	u := testUser(t, gdb)
	if cfg == nil {
		cfg = &config.Config{AppEnv: "test"}
	}
	return gdb, u, groupApp(New(gdb, cfg, nil, nil), u.ID)
}

// seedGroupLevels, A1 (3 grup), A2 (2 grup) ve C2 (2 grup) seviyelerini
// ikişer kelimelik gruplarla doldurur.
func seedGroupLevels(t *testing.T, gdb *gorm.DB) map[string][][]models.DictionaryWord {
	t.Helper()
	return map[string][][]models.DictionaryWord{
		"A1": testLevel(t, gdb, "A1", 2, 2, 2),
		"A2": testLevel(t, gdb, "A2", 2, 2),
		"C2": testLevel(t, gdb, "C2", 2, 2),
	}
}

func setPointer(t *testing.T, gdb *gorm.DB, userID, level string, group int) {
	t.Helper()

	if err := gdb.Model(&models.User{}).Where("id = ?", userID).
		Updates(map[string]any{"current_level": level, "current_group": group}).Error; err != nil {
		t.Fatalf("işaretçi yazılamadı: %v", err)
	}
}

func assertPointer(t *testing.T, gdb *gorm.DB, userID, wantLevel string, wantGroup int) {
	t.Helper()

	u := loadUser(t, gdb, userID)
	if u.CurrentLevel != wantLevel || u.CurrentGroup != wantGroup {
		t.Errorf("işaretçi = %s:%d, beklenen %s:%d", u.CurrentLevel, u.CurrentGroup, wantLevel, wantGroup)
	}
}

func advanceRequest(body any) *http.Request {
	if body == nil {
		return httptest.NewRequest(http.MethodPost, "/users/me/advance-group", nil)
	}
	return jsonRequest("/users/me/advance-group", body)
}

func currentGroupRequest(body any) *http.Request {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/users/me/current-group", bytes.NewReader(raw))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	return req
}

func userField(t *testing.T, body map[string]any) map[string]any {
	t.Helper()

	u, ok := body["user"].(map[string]any)
	if !ok {
		t.Fatalf("yanıtta user yok: %v", body)
	}
	return u
}

// --- POST /users/me/advance-group ---

func TestAdvanceGroup_GovdesizBirIleri(t *testing.T) {
	cases := []struct {
		name        string
		level       string
		group       int
		wantLevel   string
		wantGroup   int
		wantChanged bool
	}{
		{"seviye icinde", "A1", 1, "A1", 2, false},
		{"seviye sonu sonraki seviyeye", "A1", 3, "A2", 1, true},
		{"C2 son grubu bitti", "C2", 2, "C2", 0, false},
		// C2 bittiyse ("tümü tamamlandı") başa sarılmaz.
		{"C2 bitmisken degismez", "C2", 0, "C2", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gdb, u, app := newGroupTest(t, nil)
			seedGroupLevels(t, gdb)
			setPointer(t, gdb, u.ID, c.level, c.group)

			status, body := send(t, app, advanceRequest(nil))
			if status != fiber.StatusOK {
				t.Fatalf("status = %d (body: %v)", status, body)
			}
			assertPointer(t, gdb, u.ID, c.wantLevel, c.wantGroup)
			if body["levelChanged"] != c.wantChanged || body["newLevel"] != c.wantLevel || intField(t, body, "newGroup") != c.wantGroup {
				t.Errorf("yanıt = %v, beklenen levelChanged=%v newLevel=%s newGroup=%d",
					body, c.wantChanged, c.wantLevel, c.wantGroup)
			}
			uf := userField(t, body)
			if uf["currentLevel"] != c.wantLevel || int(uf["currentGroup"].(float64)) != c.wantGroup {
				t.Errorf("yanıttaki kullanıcı = %v:%v, beklenen %s:%d", uf["currentLevel"], uf["currentGroup"], c.wantLevel, c.wantGroup)
			}
		})
	}
}

func TestAdvanceGroup_GovdeyleIdempotent(t *testing.T) {
	gdb, u, app := newGroupTest(t, nil)
	seedGroupLevels(t, gdb)
	setPointer(t, gdb, u.ID, "A1", 2)

	// Yanıtı kaybolan isteğin yeniden denenmesi: aynı gövde ikinci kez gelir.
	for i := 0; i < 2; i++ {
		status, body := send(t, app, advanceRequest(fiber.Map{"level": "A1", "group": 2}))
		if status != fiber.StatusOK {
			t.Fatalf("%d. istek status = %d (body: %v)", i+1, status, body)
		}
		if body["newLevel"] != "A1" || intField(t, body, "newGroup") != 3 {
			t.Errorf("%d. istek yanıtı = %v, beklenen A1:3", i+1, body)
		}
	}
	assertPointer(t, gdb, u.ID, "A1", 3)
}

func TestAdvanceGroup_GovdeIsaretciyleUyusmazsaDegismez(t *testing.T) {
	cases := []struct {
		name string
		body fiber.Map
	}{
		{"geride kalmis grup", fiber.Map{"level": "A1", "group": 1}},
		{"ileride grup", fiber.Map{"level": "A2", "group": 1}},
		{"gecersiz seviye", fiber.Map{"level": "ZZ", "group": 3}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gdb, u, app := newGroupTest(t, nil)
			seedGroupLevels(t, gdb)
			setPointer(t, gdb, u.ID, "A1", 3)

			status, body := send(t, app, advanceRequest(c.body))
			if status != fiber.StatusOK {
				t.Fatalf("status = %d (body: %v)", status, body)
			}
			assertPointer(t, gdb, u.ID, "A1", 3)
			if body["levelChanged"] != false || body["newLevel"] != "A1" || intField(t, body, "newGroup") != 3 {
				t.Errorf("yanıt = %v, beklenen değişmemiş A1:3", body)
			}
		})
	}
}

func TestAdvanceGroup_C2SonGrubuGovdeyleTamamlanir(t *testing.T) {
	gdb, u, app := newGroupTest(t, nil)
	seedGroupLevels(t, gdb)
	setPointer(t, gdb, u.ID, "C2", 2)

	for i := 0; i < 2; i++ {
		if status, body := send(t, app, advanceRequest(fiber.Map{"level": "C2", "group": 2})); status != fiber.StatusOK {
			t.Fatalf("status = %d (body: %v)", status, body)
		}
	}
	assertPointer(t, gdb, u.ID, "C2", 0)
}

func TestAdvanceGroup_EszamanliAyniGovdeTekIlerleme(t *testing.T) {
	gdb, u, app := newGroupTest(t, nil)
	seedGroupLevels(t, gdb)
	setPointer(t, gdb, u.ID, "A1", 1)

	statuses := kilitAltindaEszamanli(t, gdb, app, u.ID, 4, func(int) *http.Request {
		return advanceRequest(fiber.Map{"level": "A1", "group": 1})
	})
	for _, s := range statuses {
		if s != fiber.StatusOK {
			t.Fatalf("status = %d (tümü: %v)", s, statuses)
		}
	}
	assertPointer(t, gdb, u.ID, "A1", 2)
}

func TestAdvanceGroup_EszamanliGovdesizIsteklerKaybolmaz(t *testing.T) {
	// Gövdesiz istek eski davranıştır (her biri bir ileri); satır kilidi
	// sayesinde eşzamanlı iki istek aynı değeri okuyup birbirini ezmez.
	gdb, u, app := newGroupTest(t, nil)
	seedGroupLevels(t, gdb)
	setPointer(t, gdb, u.ID, "A1", 1)

	statuses := kilitAltindaEszamanli(t, gdb, app, u.ID, 2, func(int) *http.Request {
		return advanceRequest(nil)
	})
	for _, s := range statuses {
		if s != fiber.StatusOK {
			t.Fatalf("status = %d (tümü: %v)", s, statuses)
		}
	}
	assertPointer(t, gdb, u.ID, "A1", 3)
}

func TestAdvanceGroup_BozukGovde400(t *testing.T) {
	gdb, u, app := newGroupTest(t, nil)
	setPointer(t, gdb, u.ID, "A1", 1)

	req := httptest.NewRequest(http.MethodPost, "/users/me/advance-group", bytes.NewReader([]byte("{bozuk")))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	if status, body := send(t, app, req); status != fiber.StatusBadRequest {
		t.Fatalf("status = %d, beklenen 400 (body: %v)", status, body)
	}
	assertPointer(t, gdb, u.ID, "A1", 1)
}

// --- PUT /users/me/current-group ---

func TestSetCurrentGroup_GecersizHedef400(t *testing.T) {
	cases := []struct {
		name string
		body fiber.Map
	}{
		{"bos govde", fiber.Map{}},
		{"grup yok", fiber.Map{"level": "A1"}},
		{"kucuk harf seviye", fiber.Map{"level": "a1", "group": 1}},
		{"bilinmeyen seviye", fiber.Map{"level": "ZZ", "group": 1}},
		{"sifir grup", fiber.Map{"level": "A1", "group": 0}},
		{"negatif grup", fiber.Map{"level": "A2", "group": -5}},
		{"seviyedeki grup sayisini asar", fiber.Map{"level": "A1", "group": 4}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gdb, u, app := newGroupTest(t, nil)
			seedGroupLevels(t, gdb)
			setPointer(t, gdb, u.ID, "A1", 1)

			status, body := send(t, app, currentGroupRequest(c.body))
			if status != fiber.StatusBadRequest || body["code"] != "invalid_group" {
				t.Fatalf("status = %d code = %v, beklenen 400 invalid_group", status, body["code"])
			}
			assertPointer(t, gdb, u.ID, "A1", 1)
		})
	}
}

func TestSetCurrentGroup_YalnizcaIleriYazar(t *testing.T) {
	cases := []struct {
		name      string
		fromLevel string
		fromGroup int
		body      fiber.Map
		wantLevel string
		wantGroup int
	}{
		{"ayni seviyede ileri", "A1", 1, fiber.Map{"level": "A1", "group": 3}, "A1", 3},
		{"sonraki seviyeye", "A1", 3, fiber.Map{"level": "A2", "group": 1}, "A2", 1},
		{"ayni grup", "A1", 2, fiber.Map{"level": "A1", "group": 2}, "A1", 2},
		{"ayni seviyede geri", "A1", 3, fiber.Map{"level": "A1", "group": 1}, "A1", 3},
		{"onceki seviyeye", "A2", 1, fiber.Map{"level": "A1", "group": 3}, "A2", 1},
		{"C2 bitmisken", "C2", 0, fiber.Map{"level": "C2", "group": 1}, "C2", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gdb, u, app := newGroupTest(t, nil)
			seedGroupLevels(t, gdb)
			setPointer(t, gdb, u.ID, c.fromLevel, c.fromGroup)

			status, body := send(t, app, currentGroupRequest(c.body))
			if status != fiber.StatusOK {
				t.Fatalf("status = %d (body: %v)", status, body)
			}
			assertPointer(t, gdb, u.ID, c.wantLevel, c.wantGroup)
			uf := userField(t, body)
			if uf["currentLevel"] != c.wantLevel || int(uf["currentGroup"].(float64)) != c.wantGroup {
				t.Errorf("yanıttaki kullanıcı = %v:%v, beklenen %s:%d", uf["currentLevel"], uf["currentGroup"], c.wantLevel, c.wantGroup)
			}
		})
	}
}

func TestSetCurrentGroup_BaslikVeAciklamaYazilir(t *testing.T) {
	gdb, u, app := newGroupTest(t, nil)
	seedGroupLevels(t, gdb)

	status, body := send(t, app, currentGroupRequest(fiber.Map{
		"level": "A1", "group": 2, "title": "Grup 2", "description": "Günlük", "totalWords": 40,
	}))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	after := loadUser(t, gdb, u.ID)
	if after.CurrentGroupTitle == nil || *after.CurrentGroupTitle != "Grup 2" ||
		after.CurrentGroupDescription == nil || *after.CurrentGroupDescription != "Günlük" ||
		after.CurrentGroupTotalWords != 40 {
		t.Errorf("başlık/açıklama/kelime sayısı yazılmadı: %+v", after)
	}

	// Geri yazım başlığı da değiştirmez.
	send(t, app, currentGroupRequest(fiber.Map{"level": "A1", "group": 1, "title": "Eski"}))
	if after := loadUser(t, gdb, u.ID); *after.CurrentGroupTitle != "Grup 2" {
		t.Errorf("geri yazım başlığı değiştirdi: %q", *after.CurrentGroupTitle)
	}
}

func TestSetCurrentGroup_UzunAlanlar400(t *testing.T) {
	gdb, u, app := newGroupTest(t, nil)
	seedGroupLevels(t, gdb)
	long := string(bytes.Repeat([]byte("a"), 1000))

	for _, body := range []fiber.Map{
		{"level": "A1", "group": 2, "title": long},
		{"level": "A1", "group": 2, "description": long},
		{"level": "A1", "group": 2, "totalWords": -1},
	} {
		if status, resp := send(t, app, currentGroupRequest(body)); status != fiber.StatusBadRequest {
			t.Errorf("status = %d, beklenen 400 (body: %v)", status, resp)
		}
	}
	assertPointer(t, gdb, u.ID, "A1", 1)
}
