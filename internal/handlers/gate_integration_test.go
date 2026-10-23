package handlers

import (
	"fmt"
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
		func(msg string) {
			k.mu.Lock()
			defer k.mu.Unlock()
			k.captures = append(k.captures, msg)
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
	setUsage(t, gdb, u.ID, map[string]any{"review_answers_today": 30})

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
