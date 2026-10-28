package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Bu dosya reklam kapısını (sözleşme K4, GATE_ENFORCEMENT) gerçek
// PostgreSQL'e karşı sınar: karar, yazımla aynı transaction'da kilitli
// kullanıcı satırından verilir. Kurulum için usage_integration_test.go
// başındaki nota bakın; TEST_DATABASE_URL yoksa testler atlanır.

// kapiKaydi, testin süresince kapı raporlayıcısını yakalar.
type kapiKaydi struct {
	mu       sync.Mutex
	lines    []string
	captures []string
	tags     []map[string]string
}

// kapiRaporunuYakala, paket düzeyindeki gateReporter'ı test boyunca
// kaydedicisiyle değiştirir.
func kapiRaporunuYakala(t *testing.T) *kapiKaydi {
	t.Helper()

	k := &kapiKaydi{}
	onceki := gateReporter
	gateReporter = services.NewGateReporterWith(
		func(format string, args ...any) {
			k.mu.Lock()
			defer k.mu.Unlock()
			k.lines = append(k.lines, fmt.Sprintf(format, args...))
		},
		func(msg string, tags map[string]string) {
			k.mu.Lock()
			defer k.mu.Unlock()
			k.captures = append(k.captures, msg)
			k.tags = append(k.tags, tags)
		},
	)
	t.Cleanup(func() { gateReporter = onceki })
	return k
}

// ihlaller, kaydedilen ihlal satırlarıdır (her biri bir istek).
func (k *kapiKaydi) ihlaller() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.lines...)
}

func (k *kapiKaydi) sentry() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.captures...)
}

// gateApp, kapıyla ilgili bütün uçları userID enjekte ederek kurar; yollar
// routes.go'dakilerle aynıdır.
func gateApp(h *Handlers, userID string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	inject := func(c *fiber.Ctx) error {
		c.Locals("userID", userID)
		return c.Next()
	}
	app.Post("/progress/learn", inject, h.LearnWord)
	app.Post("/progress/swipe", inject, h.Swipe)
	app.Get("/progress/review/box/:box", inject, h.ReviewWordsForBox)
	app.Get("/users/me/usage", inject, h.GetDailyUsage)
	app.Post("/users/me/usage/group-started", inject, h.UnlockGroup("groups_started_today_count"))
	app.Post("/users/me/usage/group-unlocked-by-ad", inject, h.UnlockGroup("groups_unlocked_by_ad_today"))
	app.Post("/users/me/usage/review-ad-pass", inject, h.RecordReviewAdPass)
	app.Post("/users/me/usage/no-fill-pass", inject, h.GrantNoFillPass)
	app.Post("/users/me/advance-group", inject, h.AdvanceGroup)
	app.Get("/map/levels/:level/groups", inject, h.LevelGroups)
	return app
}

// newGateTest, verilen kipte bir kullanıcı ve uygulama kurar.
func newGateTest(t *testing.T, mode config.GateEnforcement) (*gorm.DB, *models.User, *fiber.App) {
	t.Helper()

	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test", GateEnforcement: mode}, nil, nil)
	return gdb, u, gateApp(h, u.ID)
}

// setUsage, kullanıcının günlük sayaçlarını bugüne ait olarak yazar.
func setUsage(t *testing.T, gdb *gorm.DB, userID string, values map[string]any) {
	t.Helper()

	if _, ok := values["last_reset_date"]; !ok {
		values["last_reset_date"] = time.Now().UnixMilli()
	}
	if err := gdb.Model(&models.User{}).Where("id = ?", userID).Updates(values).Error; err != nil {
		t.Fatalf("kullanım yazılamadı: %v", err)
	}
}

// dueWords, kullanıcıya verilen kutuda n tane vadesi gelmiş tekrar kelimesi
// açar ve kimliklerini döner.
func dueWords(t *testing.T, gdb *gorm.DB, userID, prefix string, box, n int) []string {
	t.Helper()

	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s_%d", prefix, i)
		setProgress(t, gdb, userID, models.DictionaryWord{ID: ids[i], Level: "A1", Grp: 1}, box, nil)
	}
	return ids
}

func gateCode(body map[string]any) any { return body["code"] }

func progressRowCount(t *testing.T, gdb *gorm.DB, userID, wordID string) int64 {
	t.Helper()

	var n int64
	if err := gdb.Model(&models.WordProgress{}).Where("user_id = ? AND word_id = ?", userID, wordID).Count(&n).Error; err != nil {
		t.Fatalf("ilerleme sayılamadı: %v", err)
	}
	return n
}

// --- POST /progress/learn ---

func TestLearnGate_KipVeGrupDurumu(t *testing.T) {
	cases := []struct {
		name       string
		mode       config.GateEnforcement
		unlocked   bool
		wantStatus int
		wantCode   string
		wantWrite  bool
		wantReport int
		wantSentry int
	}{
		{"off acik", config.GateOff, true, 200, "", true, 0, 0},
		{"off kapali", config.GateOff, false, 200, "", true, 0, 0},
		{"log acik", config.GateLog, true, 200, "", true, 0, 0},
		{"log kapali", config.GateLog, false, 200, "", true, 1, 1},
		{"enforce acik", config.GateEnforce, true, 200, "", true, 0, 0},
		{"enforce kapali", config.GateEnforce, false, 403, "gate_required", false, 1, 0},
		{"varsayilan (bos) kip log gibi", "", false, 200, "", true, 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kayit := kapiRaporunuYakala(t)
			gdb, u, app := newGateTest(t, c.mode)
			w := testDictWord(t, gdb, "itest_gate_learn_1", "A1", 1, 1)
			if c.unlocked {
				if status, body := postJSON(t, app, "/users/me/usage/group-started", fiber.Map{"level": "A1", "group": 1}); status != 200 {
					t.Fatalf("group-started status = %d (%v)", status, body)
				}
			}

			status, body := send(t, app, learnRequest(fiber.Map{"wordId": w.ID}))
			if status != c.wantStatus {
				t.Fatalf("status = %d, beklenen %d (body: %v)", status, c.wantStatus, body)
			}
			if c.wantCode != "" && gateCode(body) != c.wantCode {
				t.Errorf("code = %v, beklenen %s", gateCode(body), c.wantCode)
			}
			if got := progressRowCount(t, gdb, u.ID, w.ID) == 1; got != c.wantWrite {
				t.Errorf("kelime yazıldı mı = %v, beklenen %v", got, c.wantWrite)
			}
			if got := len(kayit.ihlaller()); got != c.wantReport {
				t.Errorf("ihlal satırı = %d, beklenen %d (%v)", got, c.wantReport, kayit.ihlaller())
			}
			if got := len(kayit.sentry()); got != c.wantSentry {
				t.Errorf("Sentry olayı = %d, beklenen %d", got, c.wantSentry)
			}
			if c.wantReport > 0 && !strings.Contains(kayit.ihlaller()[0], "kind=learn reason=group_not_unlocked") {
				t.Errorf("ihlal satırı = %q", kayit.ihlaller()[0])
			}
		})
	}
}

func TestLearnGate_ReklamlaAcilanGrupKabulEdilir(t *testing.T) {
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newGateTest(t, config.GateEnforce)
	w := testDictWord(t, gdb, "itest_gate_learn_ad", "A1", 1, 1)
	setUsage(t, gdb, u.ID, map[string]any{"groups_started_today_count": 1, "unlocked_groups_today": "A2:1"})

	if status, _ := send(t, app, learnRequest(fiber.Map{"wordId": w.ID})); status != fiber.StatusForbidden {
		t.Fatalf("reklamsız status = %d, beklenen 403", status)
	}
	if status, body := postJSON(t, app, "/users/me/usage/group-unlocked-by-ad", fiber.Map{"level": "A1", "group": 1}); status != 200 {
		t.Fatalf("group-unlocked-by-ad status = %d (%v)", status, body)
	}
	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": w.ID})); status != fiber.StatusOK {
		t.Fatalf("reklamdan sonra status = %d (%v)", status, body)
	}
	if got := loadProgress(t, gdb, u.ID, w.ID).Box; got != 2 {
		t.Errorf("box = %d, beklenen 2", got)
	}
	if got := len(kayit.ihlaller()); got != 1 {
		t.Errorf("ihlal = %d, beklenen 1 (yalnızca reklamsız istek)", got)
	}
}

func TestLearnGate_IlerlemisKelimeDenetlenmez(t *testing.T) {
	// Yinelenen learn (yanıt kayboldu, ikinci cihaz) hiçbir şey yazmaz; grup
	// bugün açılmamış olsa da reddedilmez ve ihlal sayılmaz.
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newGateTest(t, config.GateEnforce)
	w := testDictWord(t, gdb, "itest_gate_learn_done", "A1", 1, 1)
	next := time.Now().AddDate(0, 0, 3)
	setProgress(t, gdb, u.ID, w, 3, &next)

	status, body := send(t, app, learnRequest(fiber.Map{"wordId": w.ID}))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, beklenen 200 (body: %v)", status, body)
	}
	if intField(t, body, "box") != 3 {
		t.Errorf("box = %v, beklenen 3", body["box"])
	}
	if got := kayit.ihlaller(); len(got) != 0 {
		t.Errorf("ihlal yazıldı: %v", got)
	}
}

func TestLearnGate_Kutu1EskiKayitDenetlenir(t *testing.T) {
	// Kutu 1'deki eski kayıt learn ile kutu 2'ye alınır: bu yeni bir
	// öğrenmedir ve kapıdan geçer.
	gdb, u, app := newGateTest(t, config.GateEnforce)
	kapiRaporunuYakala(t)
	w := testDictWord(t, gdb, "itest_gate_learn_box1", "A1", 1, 1)
	setProgress(t, gdb, u.ID, w, 1, nil)

	status, body := send(t, app, learnRequest(fiber.Map{"wordId": w.ID}))
	if status != fiber.StatusForbidden || gateCode(body) != "gate_required" {
		t.Fatalf("status = %d code = %v, beklenen 403 gate_required", status, gateCode(body))
	}
	if got := loadProgress(t, gdb, u.ID, w.ID).Box; got != 1 {
		t.Errorf("reddedilen istek kutuyu değiştirdi: %d", got)
	}
}

func TestLearnGate_TestModuKapiyiKapatir(t *testing.T) {
	kayit := kapiRaporunuYakala(t)
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test", GateEnforcement: config.GateEnforce, TestModeUnlockAll: true}, nil, nil)
	app := gateApp(h, u.ID)
	w := testDictWord(t, gdb, "itest_gate_learn_testmode", "A1", 1, 1)

	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": w.ID})); status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	if got := kayit.ihlaller(); len(got) != 0 {
		t.Errorf("ihlal yazıldı: %v", got)
	}
}

func TestLearnGate_GovdesizKayitlarGrupSayisiKadarHakVerir(t *testing.T) {
	// Eski istemci group-started / group-unlocked-by-ad'i gövdesiz çağırır:
	// hangi grubu açtığı bilinmez, kaç grup açtığı bilinir. Her açılış bir
	// grubu karşılar.
	kapiRaporunuYakala(t)
	gdb, u, app := newGateTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 2, 1)

	if status, _ := post(t, app, "/users/me/usage/group-started"); status != 200 {
		t.Fatalf("gövdesiz group-started status = %d", status)
	}
	for _, w := range a1[0] {
		if status, body := send(t, app, learnRequest(fiber.Map{"wordId": w.ID})); status != fiber.StatusOK {
			t.Fatalf("1. grup %s status = %d (%v)", w.ID, status, body)
		}
	}
	// İkinci grup ikinci bir açılış ister.
	status, body := send(t, app, learnRequest(fiber.Map{"wordId": a1[1][0].ID}))
	if status != fiber.StatusForbidden || gateCode(body) != "gate_required" {
		t.Fatalf("2. grup status = %d code = %v, beklenen 403 gate_required", status, gateCode(body))
	}
	if status, _ := post(t, app, "/users/me/usage/group-unlocked-by-ad"); status != 200 {
		t.Fatalf("gövdesiz group-unlocked-by-ad status = %d", status)
	}
	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": a1[1][0].ID})); status != fiber.StatusOK {
		t.Fatalf("reklamdan sonra 2. grup status = %d (%v)", status, body)
	}
	if got := progressRowCount(t, gdb, u.ID, a1[1][0].ID); got != 1 {
		t.Errorf("2. grubun kelimesi yazılmadı")
	}
}

func TestLearnGate_GovdesizHakEszamanliIkiGrubaYetmez(t *testing.T) {
	// Tek gövdesiz açılış tek grubu karşılar. Kullanıcı satırı learn'de
	// paylaşımlı kilitlendiği için iki farklı gruba aynı anda gelen ilk
	// kelimeler "bugün öğrenilen gruplar"ı birlikte boş görüp hakkı ikisi
	// birden kullanmamalı.
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newGateTest(t, config.GateEnforce)
	g1 := testDictWord(t, gdb, "itest_gate_keyless_race_g1", "A1", 1, 1)
	g2old := testDictWord(t, gdb, "itest_gate_keyless_race_g2a", "A1", 2, 1)
	g2 := testDictWord(t, gdb, "itest_gate_keyless_race_g2b", "A1", 2, 2)
	// 2. grup daha önce başlanmış (haritada açık), ama bugün değil.
	next := time.Now().AddDate(0, 0, 5)
	setProgress(t, gdb, u.ID, g2old, 3, &next)
	if err := gdb.Model(&models.WordProgress{}).Where("user_id = ? AND word_id = ?", u.ID, g2old.ID).
		Update("created_at", time.Now().AddDate(0, 0, -3)).Error; err != nil {
		t.Fatalf("created_at yazılamadı: %v", err)
	}

	if status, _ := post(t, app, "/users/me/usage/group-started"); status != 200 {
		t.Fatalf("gövdesiz group-started status = %d", status)
	}

	words := []models.DictionaryWord{g1, g2}
	statuses := kilitAltindaEszamanli(t, gdb, app, u.ID, len(words), func(i int) *http.Request {
		return learnRequest(fiber.Map{"wordId": words[i].ID})
	})
	ok := 0
	for _, s := range statuses {
		switch s {
		case fiber.StatusOK:
			ok++
		case fiber.StatusForbidden:
		default:
			t.Fatalf("beklenmeyen status %d (tümü: %v)", s, statuses)
		}
	}
	if ok != 1 {
		t.Fatalf("kabul edilen = %d, beklenen 1 (tek gövdesiz hak; tümü: %v)", ok, statuses)
	}
	if got := progressRowCount(t, gdb, u.ID, g1.ID) + progressRowCount(t, gdb, u.ID, g2.ID); got != 1 {
		t.Errorf("yazılan yeni kelime = %d, beklenen 1", got)
	}
	if got := len(kayit.ihlaller()); got != 1 {
		t.Errorf("ihlal satırı = %d, beklenen 1 (%v)", got, kayit.ihlaller())
	}
}

// --- POST /progress/swipe ---

func TestReviewGate_KipVeKontrolNoktasi(t *testing.T) {
	cases := []struct {
		name        string
		mode        config.GateEnforcement
		passes      int
		wantStatus  int
		wantAnswers int
		wantBox     int
		wantReport  int
	}{
		{"off", config.GateOff, 0, 200, 31, 3, 0},
		{"log gecissiz", config.GateLog, 0, 200, 31, 3, 1},
		{"log gecisli", config.GateLog, 1, 200, 31, 3, 0},
		{"enforce gecissiz", config.GateEnforce, 0, 403, 30, 2, 1},
		{"enforce gecisli", config.GateEnforce, 1, 200, 31, 3, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kayit := kapiRaporunuYakala(t)
			gdb, u, app := newGateTest(t, c.mode)
			ids := dueWords(t, gdb, u.ID, "itest_gate_rev", 2, 1)
			setUsage(t, gdb, u.ID, map[string]any{"review_answers_today": 30, "review_ad_pass_count_today": c.passes})

			status, body := send(t, app, progressSwipe(ids[0], "right"))
			if status != c.wantStatus {
				t.Fatalf("status = %d, beklenen %d (body: %v)", status, c.wantStatus, body)
			}
			if c.wantStatus == fiber.StatusForbidden && gateCode(body) != "review_gate_required" {
				t.Errorf("code = %v, beklenen review_gate_required", gateCode(body))
			}
			if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != c.wantAnswers {
				t.Errorf("review_answers_today = %d, beklenen %d", got, c.wantAnswers)
			}
			if got := loadProgress(t, gdb, u.ID, ids[0]).Box; got != c.wantBox {
				t.Errorf("box = %d, beklenen %d", got, c.wantBox)
			}
			if got := len(kayit.ihlaller()); got != c.wantReport {
				t.Errorf("ihlal = %d, beklenen %d (%v)", got, c.wantReport, kayit.ihlaller())
			}
			if c.wantReport > 0 && !strings.Contains(kayit.ihlaller()[0], "kind=review reason=review_checkpoint") {
				t.Errorf("ihlal satırı = %q", kayit.ihlaller()[0])
			}
		})
	}
}

func TestReviewGate_OgrenmeSwipeiKapiyaTakilmaz(t *testing.T) {
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newGateTest(t, config.GateEnforce)
	ids := dueWords(t, gdb, u.ID, "itest_gate_box1", 1, 1)
	// Kutu 1'in sağa kaydırılması öğrenmedir: grup bugün açık olmalı (bkz.
	// TestSwipe_Kutu1SagGunlukKapidanGecer).
	setUsage(t, gdb, u.ID, map[string]any{
		"review_answers_today":       30,
		"groups_started_today_count": 1,
		"unlocked_groups_today":      "A1:1",
	})

	if status, body := send(t, app, progressSwipe(ids[0], "right")); status != fiber.StatusOK {
		t.Fatalf("kutu 1 swipe status = %d (body: %v)", status, body)
	}
	if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 30 {
		t.Errorf("review_answers_today = %d, beklenen 30 (öğrenme sayılmaz)", got)
	}
	if got := kayit.ihlaller(); len(got) != 0 {
		t.Errorf("ihlal yazıldı: %v", got)
	}
}

func TestReviewGate_NoOpSwipeIhlalSayilmaz(t *testing.T) {
	// Vadesi gelmemiş ya da kutu 6'daki kelimeye gelen cevap hiçbir şeyi
	// değiştirmez (yinelenen istek); kapı dolu olsa da 200 ve ihlal yok.
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newGateTest(t, config.GateEnforce)
	next := time.Now().AddDate(0, 0, 5)
	setProgress(t, gdb, u.ID, models.DictionaryWord{ID: "itest_gate_notdue", Level: "A1", Grp: 1}, 3, &next)
	setProgress(t, gdb, u.ID, models.DictionaryWord{ID: "itest_gate_box6", Level: "A1", Grp: 1}, 6, nil)
	setUsage(t, gdb, u.ID, map[string]any{"review_answers_today": 30})

	for _, id := range []string{"itest_gate_notdue", "itest_gate_box6"} {
		if status, body := send(t, app, progressSwipe(id, "right")); status != fiber.StatusOK {
			t.Fatalf("%s status = %d (body: %v)", id, status, body)
		}
	}
	if got := kayit.ihlaller(); len(got) != 0 {
		t.Errorf("ihlal yazıldı: %v", got)
	}
}

func TestReviewGate_DunkuSayacBugunuKapatmaz(t *testing.T) {
	// Satır dün 95 cevapla kalmış: bugün sayaç sıfırdan başlar, kapı yoktur.
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newGateTest(t, config.GateEnforce)
	ids := dueWords(t, gdb, u.ID, "itest_gate_stale", 2, 1)
	setUsage(t, gdb, u.ID, map[string]any{
		"review_answers_today": 95, "review_ad_pass_count_today": 0,
		"last_reset_date": time.Now().AddDate(0, 0, -2).UnixMilli(),
	})

	if status, body := send(t, app, progressSwipe(ids[0], "right")); status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 1 {
		t.Errorf("review_answers_today = %d, beklenen 1", got)
	}
	if got := kayit.ihlaller(); len(got) != 0 {
		t.Errorf("ihlal yazıldı: %v", got)
	}
}

// --- Harita kilidi (POST /progress/learn) ---

// hepsiBugunAcik, verilen grupları bugün açılmış yazar: kapının bu kısmı
// geçilir, yalnızca harita kilidi sınanır.
func hepsiBugunAcik(t *testing.T, gdb *gorm.DB, userID string, keys ...string) {
	t.Helper()
	setUsage(t, gdb, userID, map[string]any{
		"groups_started_today_count":  1,
		"groups_unlocked_by_ad_today": len(keys) - 1,
		"unlocked_groups_today":       strings.Join(keys, ","),
	})
}

func TestMapLockGate_SiraliKilit(t *testing.T) {
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newGateTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 2, 2)
	hepsiBugunAcik(t, gdb, u.ID, "A1:1", "A1:2")

	// 1. grup bitmeden 2. grup haritada kilitli.
	status, body := send(t, app, learnRequest(fiber.Map{"wordId": a1[1][0].ID}))
	if status != fiber.StatusForbidden || gateCode(body) != "gate_required" {
		t.Fatalf("kilitli grup status = %d code = %v, beklenen 403 gate_required", status, gateCode(body))
	}
	if got := progressRowCount(t, gdb, u.ID, a1[1][0].ID); got != 0 {
		t.Error("kilitli gruba kelime yazıldı")
	}
	if l := kayit.ihlaller(); len(l) != 1 || !strings.Contains(l[0], "reason=map_locked") {
		t.Errorf("ihlal satırları = %v, beklenen tek map_locked", l)
	}

	// 1. grup bitince 2. grup açılır.
	for _, w := range a1[0] {
		if status, body := send(t, app, learnRequest(fiber.Map{"wordId": w.ID})); status != fiber.StatusOK {
			t.Fatalf("1. grup %s status = %d (%v)", w.ID, status, body)
		}
	}
	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": a1[1][0].ID})); status != fiber.StatusOK {
		t.Fatalf("1. grup bitince 2. grup status = %d (%v)", status, body)
	}
	// Başlanmış grubun sonraki kelimesi ucuz yoldan geçer.
	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": a1[1][1].ID})); status != fiber.StatusOK {
		t.Fatalf("başlanmış grubun 2. kelimesi status = %d (%v)", status, body)
	}
}

func TestMapLockGate_OncekiSeviyeBitmeden(t *testing.T) {
	kapiRaporunuYakala(t)
	gdb, u, app := newGateTest(t, config.GateEnforce)
	testLevel(t, gdb, "A1", 1)
	a2 := testLevel(t, gdb, "A2", 2)
	hepsiBugunAcik(t, gdb, u.ID, "A2:1")

	// Başlanmamış grup (computeLevelGroups yolu).
	if status, _ := send(t, app, learnRequest(fiber.Map{"wordId": a2[0][0].ID})); status != fiber.StatusForbidden {
		t.Fatalf("A1 bitmeden A2 status = %d, beklenen 403", status)
	}
	// Eski bir kayıtla başlanmış grup (ucuz yol) da önceki seviyeyi bekler.
	setProgress(t, gdb, u.ID, a2[0][1], 3, nil)
	if status, _ := send(t, app, learnRequest(fiber.Map{"wordId": a2[0][0].ID})); status != fiber.StatusForbidden {
		t.Fatalf("başlanmış A2 grubu, A1 bitmeden status = %d, beklenen 403", status)
	}

	// Seviye rozeti önceki seviyeyi tamamlanmış sayar.
	if err := gdb.Create(&models.EarnedBadge{UserID: u.ID, BadgeID: "level_A1", EarnedAt: time.Now()}).Error; err != nil {
		t.Fatalf("rozet yazılamadı: %v", err)
	}
	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": a2[0][0].ID})); status != fiber.StatusOK {
		t.Fatalf("rozetten sonra status = %d (%v)", status, body)
	}
}

func TestMapLockGate_LogKipindeTekSatir(t *testing.T) {
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newGateTest(t, config.GateLog)
	a1 := testLevel(t, gdb, "A1", 1, 1, 1)
	hepsiBugunAcik(t, gdb, u.ID, "A1:2")

	// Bugün açık ama haritada kilitli: kabul edilir, map_locked raporlanır.
	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": a1[1][0].ID})); status != fiber.StatusOK {
		t.Fatalf("status = %d (%v)", status, body)
	}
	// Hem bugün açılmamış hem kilitli: tek satır, group_not_unlocked.
	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": a1[2][0].ID})); status != fiber.StatusOK {
		t.Fatalf("status = %d (%v)", status, body)
	}
	l := kayit.ihlaller()
	if len(l) != 2 || !strings.Contains(l[0], "reason=map_locked") || !strings.Contains(l[1], "reason=group_not_unlocked") {
		t.Errorf("ihlal satırları = %v", l)
	}
}

func TestMapLockGate_TestModuKilidiAcar(t *testing.T) {
	kayit := kapiRaporunuYakala(t)
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test", GateEnforcement: config.GateEnforce, TestModeUnlockAll: true}, nil, nil)
	app := gateApp(h, u.ID)
	a1 := testLevel(t, gdb, "A1", 1, 1)

	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": a1[1][0].ID})); status != fiber.StatusOK {
		t.Fatalf("status = %d (%v)", status, body)
	}
	if l := kayit.ihlaller(); len(l) != 0 {
		t.Errorf("ihlal yazıldı: %v", l)
	}
}

func TestMapLockGate_HaritaIleAyniKarar(t *testing.T) {
	// Grupların hepsi bugün açıkken öğrenme, haritanın açık gösterdiği her
	// grupta kabul, kilitli gösterdiğinde ret almalı: dürüst istemci yalnızca
	// haritada açık gruba girer.
	cases := []struct {
		level string
		group int
		word  func(levels map[string][][]models.DictionaryWord) models.DictionaryWord
	}{
		{"A1", 2, func(l map[string][][]models.DictionaryWord) models.DictionaryWord { return l["A1"][1][1] }},
		{"A1", 3, func(l map[string][][]models.DictionaryWord) models.DictionaryWord { return l["A1"][2][1] }},
		{"A2", 1, func(l map[string][][]models.DictionaryWord) models.DictionaryWord { return l["A2"][0][0] }},
		{"A2", 2, func(l map[string][][]models.DictionaryWord) models.DictionaryWord { return l["A2"][1][0] }},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s:%d", c.level, c.group), func(t *testing.T) {
			kapiRaporunuYakala(t)
			gdb, u, app := newGateTest(t, config.GateEnforce)
			levels := mapScenario(t, gdb, u.ID)
			hepsiBugunAcik(t, gdb, u.ID, "A1:1", "A1:2", "A1:3", "A2:1", "A2:2")

			unlocked := false
			for _, g := range levelGroups(t, app, c.level) {
				if g.GroupNumber == c.group {
					unlocked = g.IsUnlocked
				}
			}
			status, body := send(t, app, learnRequest(fiber.Map{"wordId": c.word(levels).ID}))
			if unlocked && status != fiber.StatusOK {
				t.Errorf("haritada açık grup reddedildi: %d (%v)", status, body)
			}
			if !unlocked && status != fiber.StatusForbidden {
				t.Errorf("haritada kilitli grup kabul edildi: %d (%v)", status, body)
			}
		})
	}
}

// --- Eşzamanlılık: kontrol noktasında patlama ---

func TestReviewGate_EszamanliCevaplarKontrolNoktasiniAsamaz(t *testing.T) {
	// Aynı kullanıcının farklı kelimelere aynı anda gelen cevapları kilitli
	// kullanıcı satırında sıraya girer: kontrol noktasına kadar olanlar
	// kabul edilir, gerisi reddedilir; hiçbiri kilit öncesi sayacı görüp
	// kapıyı birlikte aşamaz.
	cases := []struct {
		name            string
		answers, passes int
		n               int
		wantOK          int
	}{
		{"30 kapisi", 28, 0, 8, 2},
		{"60 kapisi", 57, 1, 8, 3},
		{"90 kapisi", 89, 2, 6, 1},
		{"90 sonrasi serbest", 89, 3, 6, 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kayit := kapiRaporunuYakala(t)
			gdb, u, app := newGateTest(t, config.GateEnforce)
			ids := dueWords(t, gdb, u.ID, "itest_gate_burst", 3, c.n)
			setUsage(t, gdb, u.ID, map[string]any{"review_answers_today": c.answers, "review_ad_pass_count_today": c.passes})

			statuses := kilitAltindaEszamanli(t, gdb, app, u.ID, c.n, func(i int) *http.Request {
				return progressSwipe(ids[i], "right")
			})
			ok, rejected := 0, 0
			for _, s := range statuses {
				switch s {
				case fiber.StatusOK:
					ok++
				case fiber.StatusForbidden:
					rejected++
				default:
					t.Fatalf("beklenmeyen status %d (tümü: %v)", s, statuses)
				}
			}
			if ok != c.wantOK || rejected != c.n-c.wantOK {
				t.Fatalf("kabul = %d, ret = %d; beklenen %d / %d (tümü: %v)", ok, rejected, c.wantOK, c.n-c.wantOK, statuses)
			}
			if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != c.answers+c.wantOK {
				t.Errorf("review_answers_today = %d, beklenen %d", got, c.answers+c.wantOK)
			}
			var advanced int64
			gdb.Model(&models.WordProgress{}).Where("user_id = ? AND word_id IN ? AND box = 4", u.ID, ids).Count(&advanced)
			if int(advanced) != c.wantOK {
				t.Errorf("ilerleyen kelime = %d, beklenen %d (reddedilen kelime değişmemeli)", advanced, c.wantOK)
			}
			if got := len(kayit.ihlaller()); got != c.n-c.wantOK {
				t.Errorf("ihlal satırı = %d, beklenen %d", got, c.n-c.wantOK)
			}
		})
	}
}

// --- İhlal satırında istemci sürümü ve gece yarısı nedeni ---

func TestLearnGate_IhlalIstemciSurumunuTasir(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"baslik yok", "", "eski"},
		{"surum", "1.0.3+57", "1.0.3+57"},
		{"gecersiz", "1.0 x=y", "gecersiz"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kayit := kapiRaporunuYakala(t)
			gdb, _, app := newGateTest(t, config.GateLog)
			w := testDictWord(t, gdb, "itest_gate_version", "A1", 1, 1)

			req := learnRequest(fiber.Map{"wordId": w.ID})
			if c.header != "" {
				req.Header.Set("X-App-Version", c.header)
			}
			if status, body := send(t, app, req); status != fiber.StatusOK {
				t.Fatalf("status = %d (%v)", status, body)
			}
			lines := kayit.ihlaller()
			if len(lines) != 1 || !strings.Contains(lines[0], " app_version="+c.want+" ") {
				t.Errorf("ihlal satırı = %v, app_version=%s bekleniyordu", lines, c.want)
			}
			kayit.mu.Lock()
			defer kayit.mu.Unlock()
			if len(kayit.tags) != 1 || kayit.tags[0]["app_version"] != c.want {
				t.Errorf("Sentry etiketleri = %v, app_version=%s bekleniyordu", kayit.tags, c.want)
			}
		})
	}
}

func TestLearnGate_ToleransiGecenGeceOturumuAyriNeden(t *testing.T) {
	// Satır en son iki gün önce yazılmış ve grup o gün açılmış: öğrenme sayfası
	// gece boyunca açık kalmış dürüst istemci. Ret aynıdır, neden ayrıdır.
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newGateTest(t, config.GateEnforce)
	w1 := testDictWord(t, gdb, "itest_gate_rollover_1", "A1", 1, 1)
	w2 := testDictWord(t, gdb, "itest_gate_rollover_2", "A1", 2, 1)
	setUsage(t, gdb, u.ID, map[string]any{
		"groups_started_today_count": 1, "unlocked_groups_today": "A1:1",
		"last_reset_date": services.UsageDayStart(time.Now()).AddDate(0, 0, -2).Add(23 * time.Hour).UnixMilli(),
	})
	setProgress(t, gdb, u.ID, testDictWord(t, gdb, "itest_gate_rollover_0", "A1", 1, 2), 2, nil)

	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": w1.ID})); status != fiber.StatusForbidden || gateCode(body) != "gate_required" {
		t.Fatalf("status = %d (%v), beklenen 403 gate_required", status, body)
	}
	// O gün açılmamış bir grup yine group_not_unlocked'tır.
	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": w2.ID})); status != fiber.StatusForbidden {
		t.Fatalf("status = %d (%v), beklenen 403", status, body)
	}
	lines := kayit.ihlaller()
	if len(lines) != 2 || !strings.Contains(lines[0], "reason=rollover_expired") || !strings.Contains(lines[1], "reason=group_not_unlocked") {
		t.Errorf("ihlal satırları = %v, beklenen rollover_expired sonra group_not_unlocked", lines)
	}
}

// --- POST /users/me/usage/group-started: günde bir ücretsiz grup ---

func TestFreeGroupGate_IkinciUcretsizGrupKipeGore(t *testing.T) {
	cases := []struct {
		name       string
		mode       config.GateEnforcement
		wantStatus int
		wantWrite  bool
		wantReport int
		wantSentry int
	}{
		{"off", config.GateOff, 200, true, 0, 0},
		{"log", config.GateLog, 200, true, 1, 1},
		{"enforce", config.GateEnforce, 403, false, 1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kayit := kapiRaporunuYakala(t)
			gdb, u, app := newGateTest(t, c.mode)
			setUsage(t, gdb, u.ID, map[string]any{"groups_started_today_count": 1, "unlocked_groups_today": "A1:1"})

			status, body := postJSON(t, app, "/users/me/usage/group-started", fiber.Map{"level": "A1", "group": 2})
			if status != c.wantStatus {
				t.Fatalf("status = %d, beklenen %d (body: %v)", status, c.wantStatus, body)
			}
			if status == fiber.StatusForbidden && gateCode(body) != "gate_required" {
				t.Errorf("code = %v, beklenen gate_required", gateCode(body))
			}
			after := loadUser(t, gdb, u.ID)
			wantCount, wantSet := 1, "A1:1"
			if c.wantWrite {
				wantCount, wantSet = 2, "A1:1,A1:2"
			}
			if after.GroupsStartedTodayCount != wantCount || after.UnlockedGroupsToday != wantSet {
				t.Errorf("sayaç/küme = %d %q, beklenen %d %q", after.GroupsStartedTodayCount, after.UnlockedGroupsToday, wantCount, wantSet)
			}
			if got := kayit.ihlaller(); len(got) != c.wantReport {
				t.Errorf("ihlal satırı = %d, beklenen %d (%v)", len(got), c.wantReport, got)
			} else if c.wantReport > 0 && !strings.Contains(got[0], "kind=learn reason=free_group_reused") ||
				c.wantReport > 0 && !strings.Contains(got[0], " group=A1:2 ") || c.wantReport > 0 && strings.Contains(got[0], "word=") {
				t.Errorf("ihlal satırı = %q", got[0])
			}
			if got := len(kayit.sentry()); got != c.wantSentry {
				t.Errorf("Sentry olayı = %d, beklenen %d", got, c.wantSentry)
			}
		})
	}
}

func TestFreeGroupGate_AcikGrubaYenidenGirisVeReklamIhlalDegil(t *testing.T) {
	// Bugün açılmış gruba yeniden group-started (yanıtı kaybolan istek) ve
	// reklamla açılış, ücretsiz hak kullanılmışken de ihlal değildir.
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newGateTest(t, config.GateEnforce)
	setUsage(t, gdb, u.ID, map[string]any{"groups_started_today_count": 1, "unlocked_groups_today": "A1:1"})

	for _, req := range []struct {
		path  string
		group int
	}{
		{"/users/me/usage/group-started", 1},
		{"/users/me/usage/group-unlocked-by-ad", 2},
		{"/users/me/usage/group-started", 2},
	} {
		if status, body := postJSON(t, app, req.path, fiber.Map{"level": "A1", "group": req.group}); status != fiber.StatusOK {
			t.Fatalf("%s A1:%d status = %d (%v)", req.path, req.group, status, body)
		}
	}
	after := loadUser(t, gdb, u.ID)
	if after.GroupsStartedTodayCount != 1 || after.GroupsUnlockedByAdToday != 1 || after.UnlockedGroupsToday != "A1:1,A1:2" {
		t.Errorf("kullanım = %d/%d %q, beklenen 1/1 \"A1:1,A1:2\"", after.GroupsStartedTodayCount, after.GroupsUnlockedByAdToday, after.UnlockedGroupsToday)
	}
	if l := kayit.ihlaller(); len(l) != 0 {
		t.Errorf("ihlal üretildi: %v", l)
	}
}

func TestFreeGroupGate_YeniGundeUcretsizHakYenilenir(t *testing.T) {
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newGateTest(t, config.GateEnforce)
	dunkuKullanim(t, gdb, u.ID)

	if status, body := postJSON(t, app, "/users/me/usage/group-started", fiber.Map{"level": "A1", "group": 3}); status != fiber.StatusOK {
		t.Fatalf("status = %d (%v)", status, body)
	}
	if after := loadUser(t, gdb, u.ID); after.GroupsStartedTodayCount != 1 || after.UnlockedGroupsToday != "A1:3" {
		t.Errorf("kullanım = %d %q, beklenen 1 \"A1:3\"", after.GroupsStartedTodayCount, after.UnlockedGroupsToday)
	}
	if l := kayit.ihlaller(); len(l) != 0 {
		t.Errorf("ihlal üretildi: %v", l)
	}
}

func TestFreeGroupGate_EszamanliIkiFarkliGrupTekUcretsiz(t *testing.T) {
	cases := []struct {
		mode       config.GateEnforcement
		wantOK     int
		wantCount  int
		wantReport int
	}{
		{config.GateEnforce, 1, 1, 1},
		{config.GateLog, 2, 2, 1},
	}
	for _, c := range cases {
		t.Run(string(c.mode), func(t *testing.T) {
			kayit := kapiRaporunuYakala(t)
			gdb, u, app := newGateTest(t, c.mode)
			setUsage(t, gdb, u.ID, map[string]any{"groups_started_today_count": 0, "unlocked_groups_today": ""})

			statuses := kilitAltindaEszamanli(t, gdb, app, u.ID, 2, func(i int) *http.Request {
				return jsonRequest("/users/me/usage/group-started", fiber.Map{"level": "A1", "group": i + 1})
			})
			ok := 0
			for _, s := range statuses {
				switch s {
				case fiber.StatusOK:
					ok++
				case fiber.StatusForbidden:
				default:
					t.Fatalf("status = %d (tümü: %v)", s, statuses)
				}
			}
			if ok != c.wantOK {
				t.Errorf("kabul = %d, beklenen %d (tümü: %v)", ok, c.wantOK, statuses)
			}
			after := loadUser(t, gdb, u.ID)
			if after.GroupsStartedTodayCount != c.wantCount || len(services.SplitUnlockedGroups(after.UnlockedGroupsToday)) != c.wantCount {
				t.Errorf("sayaç/küme = %d %q, beklenen %d grup", after.GroupsStartedTodayCount, after.UnlockedGroupsToday, c.wantCount)
			}
			if got := len(kayit.ihlaller()); got != c.wantReport {
				t.Errorf("ihlal satırı = %d, beklenen %d", got, c.wantReport)
			}
		})
	}
}

// --- Uçtan uca: dürüst istemci ---

// durustIstemci, yayındaki Flutter istemcisinin kapı kararlarını aynen
// uygular (learning_swipe_page.dart _openGroupForToday, review_swipe_page.dart
// _syncGateState/_gateDue/_runCheckpointGate, rewarded_gate.dart no-fill
// yolu). Hiçbir kararı sunucunun kuralına bakarak vermez; yalnızca
// /users/me/usage yanıtını okur.
type durustIstemci struct {
	t   *testing.T
	app *fiber.App
	// 403 sayısı: dürüst istemci hiç ret almamalı.
	rejections int
}

func (k *durustIstemci) do(req *http.Request) (int, map[string]any) {
	k.t.Helper()
	status, body := send(k.t, k.app, req)
	if status == fiber.StatusForbidden {
		k.rejections++
	}
	return status, body
}

func (k *durustIstemci) post(path string, payload any) map[string]any {
	k.t.Helper()
	status, body := k.do(jsonRequest(path, payload))
	if status != fiber.StatusOK {
		k.t.Fatalf("POST %s status = %d (%v)", path, status, body)
	}
	return body
}

func (k *durustIstemci) usage() map[string]any {
	k.t.Helper()
	status, body := k.do(httptest.NewRequest(http.MethodGet, "/users/me/usage", nil))
	if status != fiber.StatusOK {
		k.t.Fatalf("GET usage status = %d (%v)", status, body)
	}
	return body
}

// openGroup, sayfa açılışındaki kapıdır: grup bugün açıksa kayıt yok; değilse
// günün ilk grubu ücretsiz (group-started), sonrakiler reklamla
// (group-unlocked-by-ad; reklam izlendi sayılır).
func (k *durustIstemci) openGroup(level string, group int) {
	k.t.Helper()
	u := k.usage()
	if containsString(unlockedGroups(k.t, u), fmt.Sprintf("%s:%d", level, group)) {
		return
	}
	body := fiber.Map{"level": level, "group": group}
	if intField(k.t, u, "groupsStartedTodayCount") == 0 {
		k.post("/users/me/usage/group-started", body)
		return
	}
	k.post("/users/me/usage/group-unlocked-by-ad", body)
}

// learnGroup, haritada açık grubun kutu 1 ya da yeni kelimelerini "Biliyorum"
// ile öğrenir: yeni kelime learn, kutu 1'deki eski kayıt swipe ile gider.
func (k *durustIstemci) learnGroup(gdb *gorm.DB, userID string, words []models.DictionaryWord) {
	k.t.Helper()
	for _, w := range words {
		var existing []models.WordProgress
		gdb.Where("user_id = ? AND word_id = ?", userID, w.ID).Find(&existing)
		if len(existing) > 0 && existing[0].Box >= 2 {
			continue
		}
		if len(existing) > 0 {
			k.post("/progress/swipe", fiber.Map{"wordId": w.ID, "direction": "right"})
			continue
		}
		k.post("/progress/learn", fiber.Map{"wordId": w.ID, "level": w.Level, "group": w.Grp, "english": w.English, "turkish": w.Turkish})
	}
	k.post("/users/me/advance-group", fiber.Map{"level": words[0].Level, "group": words[0].Grp})
}

// reviewSession, tekrar seansıdır: sayaçlar sunucudan okunur; sıradaki cevap
// geçilmemiş bir kapının arkasındaysa reklam (ya da no-fill toleransı)
// geçilir ve review-ad-pass yazılır. Kayıtlar istemcide olduğu gibi
// beklenmeden paralel gider, ama kapıya dayanan cevap gönderilmeden önce
// bekleyenler biter (_gateMaybeDue). noFillAt kapısında reklam yüklenemez.
func (k *durustIstemci) reviewSession(ids []string, noFillAt int) {
	k.t.Helper()
	u := k.usage()
	answered := intField(k.t, u, "reviewAnswersToday")
	passed := min(intField(k.t, u, "reviewAdPassCountToday"), 3) * 30
	checkpointAt := func(n int) int {
		switch {
		case n <= 30:
			return 0
		case n <= 60:
			return 30
		case n <= 90:
			return 60
		}
		return 90
	}

	var pending []chan int
	flush := func() {
		for _, ch := range pending {
			s := <-ch
			if s == fiber.StatusForbidden {
				k.rejections++
			}
			if s != fiber.StatusOK {
				k.t.Fatalf("tekrar cevabı status = %d", s)
			}
			answered++
		}
		pending = nil
	}
	for i, id := range ids {
		if checkpointAt(answered+len(pending)+1) > passed {
			flush()
			if cp := checkpointAt(answered + 1); cp > passed {
				if cp == noFillAt {
					k.post("/users/me/usage/no-fill-pass", nil)
				}
				k.post("/users/me/usage/review-ad-pass", fiber.Map{"checkpoint": cp})
				passed = cp
			}
		}
		dir := "right"
		if i%3 == 0 {
			dir = "left"
		}
		req := progressSwipe(id, dir)
		ch := make(chan int, 1)
		go func() {
			res, err := k.app.Test(req, -1)
			if err != nil {
				ch <- 0
				return
			}
			res.Body.Close()
			ch <- res.StatusCode
		}()
		pending = append(pending, ch)
	}
	flush()
}

func TestKapi_DurustIstemciHicIhlalUretmez(t *testing.T) {
	for _, mode := range []config.GateEnforcement{config.GateLog, config.GateEnforce} {
		t.Run(string(mode), func(t *testing.T) {
			kayit := kapiRaporunuYakala(t)
			gdb, u, app := newGateTest(t, mode)
			a1 := testLevel(t, gdb, "A1", 3, 3, 3)
			// Firestore'dan gelmiş kutu 1 kaydı: istemci onu swipe ile öğrenir.
			setProgress(t, gdb, u.ID, a1[0][2], 1, nil)
			k := &durustIstemci{t: t, app: app}

			// Günün ücretsiz grubu.
			k.openGroup("A1", 1)
			k.learnGroup(gdb, u.ID, a1[0])
			// Aynı gruba gün içinde yeniden giriş: yeni hak harcanmaz.
			k.openGroup("A1", 1)
			// İkinci grup reklamla; yanıtı kaybolan learn yeniden denenir.
			k.openGroup("A1", 2)
			k.post("/progress/learn", fiber.Map{"wordId": a1[1][0].ID})
			k.post("/progress/learn", fiber.Map{"wordId": a1[1][0].ID})
			k.learnGroup(gdb, u.ID, a1[1])
			// Üçüncü grup da reklamla.
			k.openGroup("A1", 3)
			k.learnGroup(gdb, u.ID, a1[2])

			// Tekrar: 70 vadesi gelmiş kelime, 30 ve 60 kapıları; 60'ta reklam
			// yüklenemez ve no-fill toleransı kullanılır.
			ids := make([]string, 0, 70)
			for box := 2; box <= 5; box++ {
				ids = append(ids, dueWords(t, gdb, u.ID, fmt.Sprintf("itest_honest_b%d", box), box, 18)...)
			}
			ids = ids[:70]
			k.reviewSession(ids[:45], 60)
			// Yinelenen cevap (yanıt kayboldu): no-op, sayılmaz, kapıya sorulmaz.
			k.post("/progress/swipe", fiber.Map{"wordId": ids[1], "direction": "right"})
			// Ekrandan çıkıp başka kutuya girilir; sayaçlar sunucudan okunur.
			k.reviewSession(ids[45:], 60)

			usage := k.usage()
			if got := intField(t, usage, "reviewAnswersToday"); got != 70 {
				t.Errorf("reviewAnswersToday = %d, beklenen 70", got)
			}
			if got := intField(t, usage, "reviewAdPassCountToday"); got != 2 {
				t.Errorf("reviewAdPassCountToday = %d, beklenen 2", got)
			}
			if got := unlockedGroups(t, usage); len(got) != 3 {
				t.Errorf("unlockedGroupsToday = %v, beklenen 3 grup", got)
			}
			if k.rejections != 0 {
				t.Errorf("dürüst istemci %d kez 403 aldı", k.rejections)
			}
			if l := kayit.ihlaller(); len(l) != 0 {
				t.Errorf("dürüst istemci ihlal üretti:\n%s", strings.Join(l, "\n"))
			}
			if s := kayit.sentry(); len(s) != 0 {
				t.Errorf("Sentry'ye gitti: %v", s)
			}
		})
	}
}

func TestKapi_DurustIstemci_SeviyeGecisiYeniGunVeDoksanKapisi(t *testing.T) {
	// Dürüst istemcinin ilk uçtan uca testinde olmayan yollar: önceki seviye
	// bitince yeni seviyenin ilk grubu (harita kilidi seviye taramasından
	// açılır), ertesi gün dünkü gruba yeniden giriş (küme boşalmıştır; günün
	// ücretsiz hakkı yazılır) ve 90 kapısı (no-fill toleransıyla), 90'dan
	// sonraki serbest cevaplar.
	for _, mode := range []config.GateEnforcement{config.GateLog, config.GateEnforce} {
		t.Run(string(mode), func(t *testing.T) {
			kayit := kapiRaporunuYakala(t)
			gdb, u, app := newGateTest(t, mode)
			a1 := testLevel(t, gdb, "A1", 2, 2)
			a2 := testLevel(t, gdb, "A2", 3)
			k := &durustIstemci{t: t, app: app}

			k.openGroup("A1", 1)
			k.learnGroup(gdb, u.ID, a1[0])
			k.openGroup("A1", 2)
			k.learnGroup(gdb, u.ID, a1[1])

			// A1 bitti: A2'nin ilk grubu haritada açık, reklamla açılır.
			k.openGroup("A2", 1)
			k.post("/progress/learn", fiber.Map{"wordId": a2[0][0].ID})

			// Ertesi gün: satır dünden kalmış (dünkü küme hâlâ yazılı).
			yesterday := services.UsageDayStart(time.Now()).AddDate(0, 0, -1).Add(20 * time.Hour)
			if err := gdb.Model(&models.User{}).Where("id = ?", u.ID).
				Update("last_reset_date", yesterday.UnixMilli()).Error; err != nil {
				t.Fatalf("last_reset_date yazılamadı: %v", err)
			}
			k.openGroup("A2", 1)
			usage := k.usage()
			if got := intField(t, usage, "groupsStartedTodayCount"); got != 1 {
				t.Errorf("yeni günde groupsStartedTodayCount = %d, beklenen 1 (ücretsiz hak)", got)
			}
			if got := unlockedGroups(t, usage); len(got) != 1 || got[0] != "A2:1" {
				t.Errorf("yeni günde unlockedGroupsToday = %v, beklenen [A2:1]", got)
			}
			k.learnGroup(gdb, u.ID, a2[0])

			// 100 tekrar cevabı: 30 ve 60 reklamla, 90 no-fill toleransıyla.
			ids := dueWords(t, gdb, u.ID, "itest_honest90", 2, 100)
			k.reviewSession(ids[:50], 0)
			k.reviewSession(ids[50:], 90)

			usage = k.usage()
			if got := intField(t, usage, "reviewAnswersToday"); got != 100 {
				t.Errorf("reviewAnswersToday = %d, beklenen 100", got)
			}
			if got := intField(t, usage, "reviewAdPassCountToday"); got != 3 {
				t.Errorf("reviewAdPassCountToday = %d, beklenen 3", got)
			}
			if k.rejections != 0 {
				t.Errorf("dürüst istemci %d kez 403 aldı", k.rejections)
			}
			if l := kayit.ihlaller(); len(l) != 0 {
				t.Errorf("dürüst istemci ihlal üretti:\n%s", strings.Join(l, "\n"))
			}
			if s := kayit.sentry(); len(s) != 0 {
				t.Errorf("Sentry'ye gitti: %v", s)
			}
		})
	}
}
