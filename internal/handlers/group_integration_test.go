package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/dto"
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

// completeGroup, grubun bütün kelimelerini kutu 2'ye yazar: grup haritada
// tamamlanmış görünür.
func completeGroup(t *testing.T, gdb *gorm.DB, userID string, words []models.DictionaryWord) {
	t.Helper()
	for _, w := range words {
		setProgress(t, gdb, userID, w, 2, nil)
	}
}

func TestAdvanceGroup_GovdesizTamamlanmisGrupBirIleri(t *testing.T) {
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
			levels := seedGroupLevels(t, gdb)
			setPointer(t, gdb, u.ID, c.level, c.group)
			if c.group > 0 {
				completeGroup(t, gdb, u.ID, levels[c.level][c.group-1])
			}

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
	// Gövdeli advance gövdedeki grubun sunucuda görülmüş olmasını ister;
	// görülmüş ama tamamlanmamış grup (bir kelime kutu 1'de) yeterlidir.
	gdb, u, app := newGroupTest(t, nil)
	a1 := seedGroupLevels(t, gdb)["A1"]
	setPointer(t, gdb, u.ID, "A1", 2)
	setProgress(t, gdb, u.ID, a1[1][0], 2, nil)
	setProgress(t, gdb, u.ID, a1[1][1], 1, nil)

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
	// "Tümü tamamlandı"ya (C2, grup 0) gövdeyle, seviye geçişi gibi yalnızca
	// C2 tamamlanınca geçilir (bkz.
	// TestAdvanceGroup_GovdeliSeviyeGecisiSeviyeTamamlaninca).
	gdb, u, app := newGroupTest(t, nil)
	c2 := seedGroupLevels(t, gdb)["C2"]
	setPointer(t, gdb, u.ID, "C2", 2)
	completeGroup(t, gdb, u.ID, c2[0])
	completeGroup(t, gdb, u.ID, c2[1])

	for i := 0; i < 2; i++ {
		if status, body := send(t, app, advanceRequest(fiber.Map{"level": "C2", "group": 2})); status != fiber.StatusOK {
			t.Fatalf("status = %d (body: %v)", status, body)
		}
	}
	assertPointer(t, gdb, u.ID, "C2", 0)
}

// seeGroupDirect, grubun ilk kelimesini kutu 2'ye, kalanlarını kutu 1'e
// yazar: grup görülür ama tamamlanmaz.
func seeGroupDirect(t *testing.T, gdb *gorm.DB, userID string, words []models.DictionaryWord) {
	t.Helper()
	for i, w := range words {
		box := 1
		if i == 0 {
			box = 2
		}
		setProgress(t, gdb, userID, w, box, nil)
	}
}

// assertAdvanceResult, assertAdvance'e ek olarak status'u, blockedReason'ı
// ve veritabanındaki işaretçiyi denetler.
func assertAdvanceResult(t *testing.T, gdb *gorm.DB, userID string, status int, body map[string]any, wantLevel string, wantGroup int, wantChanged bool, wantReason string) {
	t.Helper()
	if status != fiber.StatusOK {
		t.Fatalf("advance status = %d (body: %v)", status, body)
	}
	assertAdvance(t, body, wantLevel, wantGroup, wantChanged)
	if body["blockedReason"] != wantReason {
		t.Errorf("blockedReason = %v, beklenen %q", body["blockedReason"], wantReason)
	}
	assertPointer(t, gdb, userID, wantLevel, wantGroup)
}

func TestAdvanceGroup_GovdeliSeviyeGecisiSeviyeTamamlaninca(t *testing.T) {
	// Ürün kararı: gövdeli advance seviyenin son grubundan sonraki seviyeye
	// (C2'de "tümü tamamlandı"ya) yalnızca seviye tamamlanınca geçer.
	// Bütün grupları görülmüş ama tamamlanmamış seviyede işaretçi son grupta
	// kalır, blockedReason "previous_level" olur (haritada sonraki seviye de
	// aynı nedenle kilitli). Seviye tamamlanınca aynı istek geçirir.
	cases := []struct {
		name        string
		level       string
		wantLevel   string
		wantGroup   int
		wantChanged bool
	}{
		{"A1 sonu A2'ye", "A1", "A2", 1, true},
		{"C2 sonu tumu tamamlandi", "C2", "C2", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gdb, u, app := newGroupTest(t, nil)
			groups := seedGroupLevels(t, gdb)[c.level]
			last := len(groups)
			for _, g := range groups {
				seeGroupDirect(t, gdb, u.ID, g)
			}
			setPointer(t, gdb, u.ID, c.level, last)
			body := fiber.Map{"level": c.level, "group": last}

			status, resp := send(t, app, advanceRequest(body))
			assertAdvanceResult(t, gdb, u.ID, status, resp, c.level, last, false, "previous_level")

			if err := gdb.Model(&models.WordProgress{}).Where("user_id = ? AND level = ?", u.ID, c.level).
				Update("box", 2).Error; err != nil {
				t.Fatalf("kayıtlar güncellenemedi: %v", err)
			}
			status, resp = send(t, app, advanceRequest(body))
			assertAdvanceResult(t, gdb, u.ID, status, resp, c.wantLevel, c.wantGroup, c.wantChanged, "")

			// Yeniden deneme: değişmez.
			status, resp = send(t, app, advanceRequest(body))
			assertAdvanceResult(t, gdb, u.ID, status, resp, c.wantLevel, c.wantGroup, false, "")
		})
	}
}

func TestAdvanceGroup_GovdeliSeviyeGecisiRozetleAcik(t *testing.T) {
	// Seviye rozeti, haritanın previous_level kilidinde olduğu gibi seviyeyi
	// tamamlanmış sayar (ör. rozetten sonra sözlüğe kelime eklendi). Son
	// grup görülmüş ama tamamlanmamış: açık grup sayısı 1, sınır dolmaz.
	gdb, u, app := newGroupTest(t, nil)
	a1 := seedGroupLevels(t, gdb)["A1"]
	completeGroup(t, gdb, u.ID, a1[0])
	completeGroup(t, gdb, u.ID, a1[1])
	seeGroupDirect(t, gdb, u.ID, a1[2])
	if err := gdb.Create(&models.EarnedBadge{UserID: u.ID, BadgeID: "level_A1", EarnedAt: time.Now()}).Error; err != nil {
		t.Fatalf("rozet yazılamadı: %v", err)
	}
	setPointer(t, gdb, u.ID, "A1", 3)

	status, body := send(t, app, advanceRequest(fiber.Map{"level": "A1", "group": 3}))
	assertAdvanceResult(t, gdb, u.ID, status, body, "A2", 1, true, "")
}

func TestAdvanceGroup_GovdeliGorulmemisGrupIlerletmez(t *testing.T) {
	// İstemciye körü körüne güvenilmez: gövdedeki grup sunucuda görülmemişse
	// (her kelimesinin kaydı yok) işaretçi yerinde kalır, blockedReason
	// "previous_not_seen" olur. Seviyenin son grubunda da önce bu sorulur.
	t.Run("seviye ici", func(t *testing.T) {
		gdb, u, app := newGroupTest(t, nil)
		a1 := seedGroupLevels(t, gdb)["A1"]
		setPointer(t, gdb, u.ID, "A1", 1)
		body := fiber.Map{"level": "A1", "group": 1}

		status, resp := send(t, app, advanceRequest(body))
		assertAdvanceResult(t, gdb, u.ID, status, resp, "A1", 1, false, "previous_not_seen")

		setProgress(t, gdb, u.ID, a1[0][0], 2, nil)
		status, resp = send(t, app, advanceRequest(body))
		assertAdvanceResult(t, gdb, u.ID, status, resp, "A1", 1, false, "previous_not_seen")

		// Son kelime "Bilmiyorum" ile kutu 1'e yazılınca grup görülür.
		setProgress(t, gdb, u.ID, a1[0][1], 1, nil)
		status, resp = send(t, app, advanceRequest(body))
		assertAdvanceResult(t, gdb, u.ID, status, resp, "A1", 2, false, "")
	})
	t.Run("seviye sonu", func(t *testing.T) {
		gdb, u, app := newGroupTest(t, nil)
		a1 := seedGroupLevels(t, gdb)["A1"]
		completeGroup(t, gdb, u.ID, a1[0])
		completeGroup(t, gdb, u.ID, a1[1])
		setProgress(t, gdb, u.ID, a1[2][0], 2, nil)
		setPointer(t, gdb, u.ID, "A1", 3)

		status, resp := send(t, app, advanceRequest(fiber.Map{"level": "A1", "group": 3}))
		assertAdvanceResult(t, gdb, u.ID, status, resp, "A1", 3, false, "previous_not_seen")
	})
}

func TestAdvanceGroup_TestModuGovdeliKosulAramaz(t *testing.T) {
	// TEST_MODE_UNLOCK_ALL'da haritada kilit yoktur: gövdeli advance grubun
	// görülmesini ve seviyenin tamamlanmasını aramaz.
	gdb, u, app := newGroupTest(t, &config.Config{AppEnv: "test", TestModeUnlockAll: true})
	seedGroupLevels(t, gdb)
	setPointer(t, gdb, u.ID, "A1", 3)

	status, body := send(t, app, advanceRequest(fiber.Map{"level": "A1", "group": 3}))
	assertAdvanceResult(t, gdb, u.ID, status, body, "A2", 1, true, "")
}

func TestAdvanceGroup_EszamanliAyniGovdeTekIlerleme(t *testing.T) {
	gdb, u, app := newGroupTest(t, nil)
	a1 := seedGroupLevels(t, gdb)["A1"]
	setPointer(t, gdb, u.ID, "A1", 1)
	completeGroup(t, gdb, u.ID, a1[0])

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
	// Gövdesiz istek işaretçideki grup tamamlanmışsa bir ileri gider; satır
	// kilidi sayesinde eşzamanlı iki istek aynı değeri okuyup birbirini ezmez.
	gdb, u, app := newGroupTest(t, nil)
	a1 := seedGroupLevels(t, gdb)["A1"]
	setPointer(t, gdb, u.ID, "A1", 1)
	completeGroup(t, gdb, u.ID, a1[0])
	completeGroup(t, gdb, u.ID, a1[1])

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

// --- GET /map/levels/:level/groups ---

func levelGroups(t *testing.T, app *fiber.App, level string) []dto.LevelGroupDTO {
	t.Helper()

	status, raw := getJSON(t, app, "/map/levels/"+level+"/groups")
	if status != fiber.StatusOK {
		t.Fatalf("GET groups(%s) status = %d (body: %s)", level, status, raw)
	}
	var groups []dto.LevelGroupDTO
	if err := json.Unmarshal(raw, &groups); err != nil || groups == nil {
		t.Fatalf("gruplar çözülemedi: %v (body: %s)", err, raw)
	}
	return groups
}

// grp, beklenen grup durumudur; seen, kaydı olan kelime sayısıdır (isSeen
// bundan çıkar), reason kilit nedenidir.
func grp(n, total, remaining int, completed, unlocked, started bool, seen int, reason string) dto.LevelGroupDTO {
	return dto.LevelGroupDTO{
		GroupNumber: n, TotalWords: total, RemainingInBox1: remaining,
		IsCompleted: completed, IsUnlocked: unlocked, HasStarted: started,
		IsSeen: total > 0 && seen == total, SeenWords: seen, LockReason: reason,
	}
}

// mapScenario, harita testlerinin ortak verisidir:
//
//	A1 grup 1: iki kelime de kutu 2+ (tamamlandı)
//	A1 grup 2: bir kelime kutu 3, biri kutu 1 (başladı, 1 kelime kaldı)
//	A1 grup 3: başlanmadı; tek kaydı başka seviyeye yazılmış (sayılmaz)
func mapScenario(t *testing.T, gdb *gorm.DB, userID string) map[string][][]models.DictionaryWord {
	t.Helper()

	levels := seedGroupLevels(t, gdb)
	a1 := levels["A1"]
	setProgress(t, gdb, userID, a1[0][0], 2, nil)
	setProgress(t, gdb, userID, a1[0][1], 6, nil)
	setProgress(t, gdb, userID, a1[1][0], 3, nil)
	setProgress(t, gdb, userID, a1[1][1], 1, nil)
	mismatched := a1[2][0]
	mismatched.Level = "B1"
	setProgress(t, gdb, userID, mismatched, 2, nil)
	return levels
}

func TestLevelGroups_SiraliKilitVeIlerleme(t *testing.T) {
	gdb, u, app := newGroupTest(t, nil)
	mapScenario(t, gdb, u.ID)

	want := []dto.LevelGroupDTO{
		grp(1, 2, 0, true, true, true, 2, ""),
		grp(2, 2, 1, false, true, true, 2, ""),
		// Önceki grup bitmedi ama bütün kelimeleri görüldü (biri kutu 1'de):
		// başlanmamış olsa da açık.
		grp(3, 2, 2, false, true, false, 0, ""),
	}
	if got := levelGroups(t, app, "A1"); !reflect.DeepEqual(got, want) {
		t.Errorf("A1 grupları = %+v\nbeklenen %+v", got, want)
	}

	// A1 bitmediği (ve rozeti olmadığı) için A2'nin tamamı kilitli.
	wantA2 := []dto.LevelGroupDTO{
		grp(1, 2, 2, false, false, false, 0, "previous_level"),
		grp(2, 2, 2, false, false, false, 0, "previous_level"),
	}
	if got := levelGroups(t, app, "A2"); !reflect.DeepEqual(got, wantA2) {
		t.Errorf("A2 grupları = %+v\nbeklenen %+v", got, wantA2)
	}

	// C2'nin önceki seviyesinde (C1) hiç kelime yok: tamamlanmış sayılmaz.
	if got := levelGroups(t, app, "C2"); got[0].IsUnlocked {
		t.Errorf("C2 grup 1 açık, önceki seviye tamamlanmadı: %+v", got)
	}

	if got := levelGroups(t, app, "ZZ"); len(got) != 0 {
		t.Errorf("bilinmeyen seviye = %+v, beklenen boş liste", got)
	}
}

func TestLevelGroups_OncekiSeviyeRozetleAcilir(t *testing.T) {
	gdb, u, app := newGroupTest(t, nil)
	mapScenario(t, gdb, u.ID)
	if err := gdb.Create(&models.EarnedBadge{UserID: u.ID, BadgeID: "level_A1", EarnedAt: time.Now()}).Error; err != nil {
		t.Fatalf("rozet yazılamadı: %v", err)
	}

	want := []dto.LevelGroupDTO{
		grp(1, 2, 2, false, true, false, 0, ""),
		grp(2, 2, 2, false, false, false, 0, "previous_not_seen"),
	}
	if got := levelGroups(t, app, "A2"); !reflect.DeepEqual(got, want) {
		t.Errorf("A2 grupları = %+v\nbeklenen %+v", got, want)
	}
}

func TestLevelGroups_OncekiSeviyeTamamlaninca(t *testing.T) {
	gdb, u, app := newGroupTest(t, nil)
	levels := mapScenario(t, gdb, u.ID)
	a1 := levels["A1"]
	// A1'in kalan kelimeleri de öğrenilince (rozet olmadan) A2 açılır.
	gdb.Model(&models.WordProgress{}).Where("user_id = ? AND word_id = ?", u.ID, a1[1][1].ID).Update("box", 2)
	gdb.Model(&models.WordProgress{}).Where("user_id = ? AND word_id = ?", u.ID, a1[2][0].ID).Update("level", "A1")
	setProgress(t, gdb, u.ID, a1[2][1], 4, nil)

	wantA1 := []dto.LevelGroupDTO{
		grp(1, 2, 0, true, true, true, 2, ""),
		grp(2, 2, 0, true, true, true, 2, ""),
		grp(3, 2, 0, true, true, true, 2, ""),
	}
	if got := levelGroups(t, app, "A1"); !reflect.DeepEqual(got, wantA1) {
		t.Errorf("A1 grupları = %+v\nbeklenen %+v", got, wantA1)
	}
	wantA2 := []dto.LevelGroupDTO{
		grp(1, 2, 2, false, true, false, 0, ""),
		grp(2, 2, 2, false, false, false, 0, "previous_not_seen"),
	}
	if got := levelGroups(t, app, "A2"); !reflect.DeepEqual(got, wantA2) {
		t.Errorf("A2 grupları = %+v\nbeklenen %+v", got, wantA2)
	}
}

func TestLevelGroups_TestModuHepsiAcik(t *testing.T) {
	gdb, u, app := newGroupTest(t, &config.Config{AppEnv: "test", TestModeUnlockAll: true})
	mapScenario(t, gdb, u.ID)

	for _, level := range []string{"A1", "A2"} {
		for _, g := range levelGroups(t, app, level) {
			if !g.IsUnlocked {
				t.Errorf("%s grup %d kilitli, test modunda hepsi açık olmalı", level, g.GroupNumber)
			}
		}
	}
}

func TestMaxGroupForLevel(t *testing.T) {
	gdb, _, app := newGroupTest(t, nil)
	seedGroupLevels(t, gdb)

	for level, want := range map[string]int{"A1": 3, "A2": 2, "ZZ": 0} {
		status, raw := getJSON(t, app, "/map/levels/"+level+"/max-group")
		var body struct {
			MaxGroup int `json:"maxGroup"`
		}
		if status != fiber.StatusOK || json.Unmarshal(raw, &body) != nil || body.MaxGroup != want {
			t.Errorf("max-group(%s) = %d %s, beklenen 200 ve %d", level, status, raw, want)
		}
	}
}

// --- Uçtan uca grup akışı ---

func TestGrupAkisi_OgrenTamamlaIlerle(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := groupApp(h, u.ID)
	papp := progressApp(h, u.ID)
	a1 := seedGroupLevels(t, gdb)["A1"]

	// Grup 1'in kelimeleri öğrenilir (istemci gövdeye kendi değerlerini koyar).
	for _, w := range a1[0] {
		status, body := send(t, papp, learnRequest(fiber.Map{
			"wordId": w.ID, "level": w.Level, "group": w.Grp, "english": w.English, "turkish": w.Turkish,
		}))
		if status != fiber.StatusOK {
			t.Fatalf("learn(%s) status = %d (body: %v)", w.ID, status, body)
		}
	}

	groups := levelGroups(t, app, "A1")
	if !groups[0].IsCompleted || !groups[1].IsUnlocked || groups[2].IsUnlocked {
		t.Fatalf("öğrenme sonrası harita = %+v, beklenen grup 1 tamam, grup 2 açık, grup 3 kilitli", groups)
	}

	// Grubun kayıtları sunucuda; GET /progress/group ve /progress/word onları döner.
	status, raw := getJSON(t, papp, "/progress/group?level=A1&group=1")
	var list []map[string]any
	if status != fiber.StatusOK || json.Unmarshal(raw, &list) != nil || len(list) != 2 {
		t.Errorf("progress/group = %d %s, beklenen 2 kayıt", status, raw)
	}
	if status, raw := getJSON(t, papp, "/progress/word/"+a1[0][0].ID); status != fiber.StatusOK {
		t.Errorf("progress/word status = %d (body: %s)", status, raw)
	}
	if status, raw := getJSON(t, papp, "/progress/word/itest_olmayan"); status != fiber.StatusNotFound {
		t.Errorf("olmayan kelime status = %d, beklenen 404 (body: %s)", status, raw)
	}

	// Grup tamamlandı: işaretçi gövdeyle ilerler, yeniden deneme bir daha ilerletmez.
	for i := 0; i < 2; i++ {
		if status, body := send(t, app, advanceRequest(fiber.Map{"level": "A1", "group": 1})); status != fiber.StatusOK {
			t.Fatalf("advance status = %d (body: %v)", status, body)
		}
	}
	assertPointer(t, gdb, u.ID, "A1", 2)

	status, raw = getJSON(t, app, "/home/current-group")
	var cg dto.CurrentGroupDTO
	if status != fiber.StatusOK || json.Unmarshal(raw, &cg) != nil || cg.Level != "A1" || cg.Group != 2 {
		t.Errorf("home/current-group = %d %s, beklenen A1 grup 2", status, raw)
	}
}

func TestAdvanceGroup_EskiIstemciGovdeleriBirIleri(t *testing.T) {
	// Yayındaki istemci gövde göndermez; Content-Type başlığı yine de gelebilir.
	// İşaretçideki grup tamamlanmışsa hepsi gövdesiz istek sayılır.
	for _, raw := range []string{"", "null", "{}"} {
		t.Run("govde "+raw, func(t *testing.T) {
			gdb, u, app := newGroupTest(t, nil)
			a1 := seedGroupLevels(t, gdb)["A1"]
			setPointer(t, gdb, u.ID, "A1", 1)
			completeGroup(t, gdb, u.ID, a1[0])

			req := httptest.NewRequest(http.MethodPost, "/users/me/advance-group", bytes.NewReader([]byte(raw)))
			req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
			if status, body := send(t, app, req); status != fiber.StatusOK {
				t.Fatalf("status = %d (body: %v)", status, body)
			}
			assertPointer(t, gdb, u.ID, "A1", 2)
		})
	}
}

// --- QA: eşzamanlı current-group ---

func TestSetCurrentGroup_EszamanliIleriVeGeriSonuctaIleride(t *testing.T) {
	// Haritadan iki gruba art arda dokunulup istekler aynı anda gelir: sıra ne
	// olursa olsun işaretçi ilerideki grupta, başlığı da onunki kalır.
	gdb, u, app := newGroupTest(t, nil)
	seedGroupLevels(t, gdb)
	setPointer(t, gdb, u.ID, "A1", 1)

	bodies := []fiber.Map{
		{"level": "A1", "group": 3, "title": "uc"},
		{"level": "A1", "group": 2, "title": "iki"},
	}
	statuses := kilitAltindaEszamanli(t, gdb, app, u.ID, len(bodies), func(i int) *http.Request {
		return currentGroupRequest(bodies[i])
	})
	for _, s := range statuses {
		if s != fiber.StatusOK {
			t.Fatalf("status = %d (tümü: %v)", s, statuses)
		}
	}
	assertPointer(t, gdb, u.ID, "A1", 3)
	if got := loadUser(t, gdb, u.ID).CurrentGroupTitle; got == nil || *got != "uc" {
		t.Errorf("başlık = %v, beklenen \"uc\" (geri yazım başlığı değiştirmemeli)", got)
	}
}

func TestAdvanceGroup_GovdeliIstekSonGruptaSeviyeAtlarYanitAlanlari(t *testing.T) {
	// Seviye tamamlanmışken son grubu için gövdeli advance bir sonraki
	// seviyeye geçer; yanıt şekli (user, levelChanged, newLevel, newGroup)
	// değişmemiştir. Eskiden son grubun gövdesi seviye tamamlanmamışken de
	// seviye atlatıyordu; ürün kararıyla seviye geçişi artık yalnızca seviye
	// tamamlanınca olur, kurulum bu yüzden A1'in bütün gruplarını tamamlar.
	gdb, u, app := newGroupTest(t, nil)
	for _, g := range seedGroupLevels(t, gdb)["A1"] {
		completeGroup(t, gdb, u.ID, g)
	}
	setPointer(t, gdb, u.ID, "A1", 3)

	status, body := send(t, app, advanceRequest(fiber.Map{"level": "A1", "group": 3}))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	if body["levelChanged"] != true || body["newLevel"] != "A2" || body["newGroup"] != float64(1) {
		t.Errorf("yanıt = %v, beklenen levelChanged=true newLevel=A2 newGroup=1", body)
	}
	if usr := userField(t, body); usr["currentLevel"] != "A2" || usr["currentGroup"] != float64(1) {
		t.Errorf("user = %v, beklenen A2:1", usr)
	}
	assertPointer(t, gdb, u.ID, "A2", 1)

	// Aynı gövdeyle yeniden deneme: değişmez, levelChanged=false.
	status, body = send(t, app, advanceRequest(fiber.Map{"level": "A1", "group": 3}))
	if status != fiber.StatusOK || body["levelChanged"] != false || body["newLevel"] != "A2" {
		t.Errorf("yeniden deneme: status=%d body=%v", status, body)
	}
	assertPointer(t, gdb, u.ID, "A2", 1)
}
