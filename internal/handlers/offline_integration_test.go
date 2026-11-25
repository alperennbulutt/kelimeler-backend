package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Bu dosya çevrimdışı modun sunucu tarafını (opId, answeredAt, POST
// /progress/sync) gerçek PostgreSQL'e karşı sınar. Kurulum için
// usage_integration_test.go başındaki nota bakın; TEST_DATABASE_URL yoksa
// testler atlanır.

// offlineApp, çevrimdışı modun kullandığı uçları userID enjekte ederek kurar;
// yollar routes.go'dakilerle aynıdır.
func offlineApp(h *Handlers, userID string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	inject := func(c *fiber.Ctx) error {
		c.Locals("userID", userID)
		return c.Next()
	}
	app.Post("/progress/learn", inject, h.LearnWord)
	app.Post("/progress/unknown", inject, h.MarkUnknown)
	app.Post("/progress/swipe", inject, h.Swipe)
	app.Post("/progress/sync", inject, h.SyncProgress)
	app.Get("/progress/word/:wordId", inject, h.GetUserWord)
	app.Get("/progress/review/box/:box", inject, h.ReviewWordsForBox)
	app.Get("/users/me/usage", inject, h.GetDailyUsage)
	app.Post("/users/me/usage/group-started", inject, h.UnlockGroup(freeGroupColumn))
	return app
}

func newOfflineTest(t *testing.T, mode config.GateEnforcement) (*gorm.DB, *models.User, *fiber.App) {
	t.Helper()

	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test", GateEnforcement: mode}, nil, nil)
	return gdb, u, offlineApp(h, u.ID)
}

// rfc, zamanı istemcinin göndereceği biçimde (RFC3339, UTC) yazar.
func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// syncIstegi, POST /progress/sync gönderir ve sonuçları döner.
func syncIstegi(t *testing.T, app *fiber.App, ops ...fiber.Map) (int, map[string]any, []map[string]any) {
	t.Helper()

	if ops == nil {
		ops = []fiber.Map{}
	}
	status, body := postJSON(t, app, "/progress/sync", fiber.Map{"ops": ops})
	var results []map[string]any
	if raw, ok := body["results"].([]any); ok {
		for _, r := range raw {
			results = append(results, r.(map[string]any))
		}
	}
	return status, body, results
}

// syncOK, sync'in 200 döndüğünü ve her işlem için bir sonuç olduğunu doğrular.
func syncOK(t *testing.T, app *fiber.App, ops ...fiber.Map) []map[string]any {
	t.Helper()

	status, body, results := syncIstegi(t, app, ops...)
	if status != fiber.StatusOK {
		t.Fatalf("sync status = %d, beklenen 200 (body: %v)", status, body)
	}
	if len(results) != len(ops) {
		t.Fatalf("sonuç sayısı = %d, beklenen %d (body: %v)", len(results), len(ops), body)
	}
	for i, r := range results {
		if r["opId"] != ops[i]["opId"] {
			t.Errorf("%d. sonucun opId'si = %v, beklenen %v", i, r["opId"], ops[i]["opId"])
		}
	}
	return results
}

// sonucBekle, bir sonucun durumunu ve kodunu doğrular.
func sonucBekle(t *testing.T, r map[string]any, status, code string) {
	t.Helper()
	got, _ := r["code"].(string)
	if r["status"] != status || got != code {
		t.Errorf("sonuç = %v/%q, beklenen %s/%q (%v)", r["status"], got, status, code, r)
	}
}

func swipeOp(wordID, direction string, at time.Time) fiber.Map {
	return fiber.Map{"opId": uuid.NewString(), "kind": "swipe", "wordId": wordID, "direction": direction, "answeredAt": rfc(at)}
}

func learnOp(wordID string, at time.Time) fiber.Map {
	return fiber.Map{"opId": uuid.NewString(), "kind": "learn", "wordId": wordID, "answeredAt": rfc(at)}
}

// yakin, iki anın verilen tolerans içinde olduğunu doğrular.
func yakin(t *testing.T, ad string, got *time.Time, want time.Time, tol time.Duration) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = nil, beklenen ~%v", ad, want)
		return
	}
	if d := got.Sub(want); d > tol || d < -tol {
		t.Errorf("%s = %v, beklenen %v (±%v)", ad, *got, want, tol)
	}
}

// dun, şimdiden tam 24 saat öncesidir: İstanbul'da yaz saati olmadığı için
// her zaman bir önceki kullanım günüdür.
func dun() time.Time { return time.Now().Add(-24 * time.Hour) }

// --- Tek tek uçlarda opId ---

func TestOpID_SwipeAyniKimlikIkinciKezUygulanmaz(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	ids := dueWords(t, gdb, u.ID, "off_tekrar", 2, 1)
	opID := uuid.NewString()

	status, ilk := postJSON(t, app, "/progress/swipe", fiber.Map{"wordId": ids[0], "direction": "right", "opId": opID})
	if status != fiber.StatusOK || ilk["box"] != float64(3) {
		t.Fatalf("ilk swipe: status = %d body = %v, beklenen 200 kutu 3", status, ilk)
	}
	// Kelime yeniden vadeli olsa bile aynı işlem ikinci kez uygulanmaz.
	gecmis := time.Now().Add(-time.Hour)
	if err := gdb.Model(&models.WordProgress{}).Where("user_id = ? AND word_id = ?", u.ID, ids[0]).
		Update("next_review_date", gecmis).Error; err != nil {
		t.Fatal(err)
	}
	status, ikinci := postJSON(t, app, "/progress/swipe", fiber.Map{"wordId": ids[0], "direction": "right", "opId": opID})
	if status != fiber.StatusOK || !reflect.DeepEqual(ilk, ikinci) {
		t.Errorf("tekrar: status = %d body = %v, beklenen ilk yanıt %v", status, ikinci, ilk)
	}
	if wp := loadProgress(t, gdb, u.ID, ids[0]); wp.Box != 3 {
		t.Errorf("kutu = %d, beklenen 3 (ikinci kez uygulandı)", wp.Box)
	}
	if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 1 {
		t.Errorf("review_answers_today = %d, beklenen 1", got)
	}
	// Aynı kimlik sync'ten gelirse de uygulanmaz, ilk sonuç döner.
	r := syncOK(t, app, fiber.Map{"opId": opID, "kind": "swipe", "wordId": ids[0], "direction": "right"})
	sonucBekle(t, r[0], "applied", "")
	if p, _ := r[0]["progress"].(map[string]any); p["box"] != float64(3) {
		t.Errorf("sync'teki saklı sonuç = %v, beklenen kutu 3", r[0])
	}
	if wp := loadProgress(t, gdb, u.ID, ids[0]); wp.Box != 3 {
		t.Errorf("kutu = %d, beklenen 3", wp.Box)
	}
}

func TestOpID_LearnVeUnknownTekrarlanabilir(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	w1 := testDictWord(t, gdb, "itest_off_learn_1", "A1", 1, 901)
	w2 := testDictWord(t, gdb, "itest_off_unknown_1", "A1", 1, 902)
	setUsage(t, gdb, u.ID, map[string]any{"groups_started_today_count": 1, "unlocked_groups_today": "A1:1"})

	for _, c := range []struct{ path, wordID string }{
		{"/progress/learn", w1.ID},
		{"/progress/unknown", w2.ID},
	} {
		opID := uuid.NewString()
		status, ilk := postJSON(t, app, c.path, fiber.Map{"wordId": c.wordID, "opId": opID})
		if status != fiber.StatusOK {
			t.Fatalf("%s: status = %d (%v)", c.path, status, ilk)
		}
		status, ikinci := postJSON(t, app, c.path, fiber.Map{"wordId": c.wordID, "opId": opID})
		if status != fiber.StatusOK || !reflect.DeepEqual(ilk, ikinci) {
			t.Errorf("%s tekrar: status = %d body = %v, beklenen %v", c.path, status, ikinci, ilk)
		}
	}
	var n int64
	gdb.Model(&models.AppliedOp{}).Where("user_id = ?", u.ID).Count(&n)
	if n != 2 {
		t.Errorf("applied_ops satırı = %d, beklenen 2", n)
	}
}

func TestOpID_GecersizVeBaskaTurdeKullanilmis(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	ids := dueWords(t, gdb, u.ID, "off_tur", 2, 1)

	status, body := postJSON(t, app, "/progress/swipe", fiber.Map{"wordId": ids[0], "direction": "left", "opId": "uuid-degil"})
	if status != fiber.StatusBadRequest {
		t.Errorf("geçersiz opId: status = %d, beklenen 400 (%v)", status, body)
	}
	if wp := loadProgress(t, gdb, u.ID, ids[0]); wp.WrongCount != 0 {
		t.Error("geçersiz opId'li istek uygulandı")
	}

	opID := uuid.NewString()
	if status, body := postJSON(t, app, "/progress/swipe", fiber.Map{"wordId": ids[0], "direction": "left", "opId": opID}); status != fiber.StatusOK {
		t.Fatalf("swipe: status = %d (%v)", status, body)
	}
	status, body = postJSON(t, app, "/progress/learn", fiber.Map{"wordId": ids[0], "opId": opID})
	if status != fiber.StatusConflict || body["code"] != "op_id_conflict" {
		t.Errorf("başka türde aynı opId: status = %d body = %v, beklenen 409 op_id_conflict", status, body)
	}
}

// Aynı opId'li eşzamanlı istekler: biri uygular, diğerleri onun sonucunu
// döner; kelime bir kez ilerler, cevap bir kez sayılır.
func TestOpID_EszamanliAyniKimlikBirKezUygulanir(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	ids := dueWords(t, gdb, u.ID, "off_eszamanli", 2, 1)
	opID := uuid.NewString()

	const n = 5
	statuses := satirKilidiAltindaEszamanli(t, gdb, app,
		"SELECT 1 FROM word_progresses WHERE user_id = '"+u.ID+"' AND word_id = ? FOR UPDATE", ids[0],
		n, func(int) *http.Request {
			return jsonRequest("/progress/swipe", fiber.Map{"wordId": ids[0], "direction": "right", "opId": opID})
		})
	for _, s := range statuses {
		if s != fiber.StatusOK {
			t.Errorf("status = %v, beklenen hepsi 200", statuses)
			break
		}
	}
	if wp := loadProgress(t, gdb, u.ID, ids[0]); wp.Box != 3 {
		t.Errorf("kutu = %d, beklenen 3", wp.Box)
	}
	if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 1 {
		t.Errorf("review_answers_today = %d, beklenen 1", got)
	}
}

// --- answeredAt ---

func TestAnsweredAt_KirpilirVeYoksaSimdi(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateOff)
	ids := dueWords(t, gdb, u.ID, "off_kirp", 2, 3)

	once := time.Now()
	cases := []struct {
		wordID string
		at     any
		want   time.Time
	}{
		// 30 gün önce → 7 gün önceye kırpılır.
		{ids[0], rfc(once.Add(-30 * 24 * time.Hour)), once.Add(-7 * 24 * time.Hour)},
		// Bir saat sonra → en çok 5 dk sonra (gece yarısını geçiyorsa şimdi).
		{ids[1], rfc(once.Add(time.Hour)), once.Add(5 * time.Minute)},
		// Verilmezse sunucunun saati.
		{ids[2], nil, once},
	}
	if services.UsageDate(once.Add(5*time.Minute)) != services.UsageDate(once) {
		cases[1].want = once
	}
	for _, c := range cases {
		payload := fiber.Map{"wordId": c.wordID, "direction": "right"}
		if c.at != nil {
			payload["answeredAt"] = c.at
		}
		if status, body := postJSON(t, app, "/progress/swipe", payload); status != fiber.StatusOK {
			t.Fatalf("%s: status = %d (%v)", c.wordID, status, body)
		}
		wp := loadProgress(t, gdb, u.ID, c.wordID)
		yakin(t, c.wordID+" lastReviewDate", wp.LastReviewDate, c.want, 5*time.Second)
	}
}

func TestAnsweredAt_GecmisGunCevabiBugunkuSayaclaraDokunmaz(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	ids := dueWords(t, gdb, u.ID, "off_gecmis", 3, 2)
	setUsage(t, gdb, u.ID, map[string]any{"review_answers_today": 30})
	once := loadUser(t, gdb, u.ID)

	// Bugünün cevabı 30 kapısına takılır...
	status, body := postJSON(t, app, "/progress/swipe", fiber.Map{"wordId": ids[0], "direction": "right"})
	if status != fiber.StatusForbidden || body["code"] != "review_gate_required" {
		t.Fatalf("bugünkü cevap: status = %d body = %v, beklenen 403 review_gate_required", status, body)
	}
	// ...dünün cevabı kapıya sorulmaz ve bugünkü sayaca yazılmaz.
	at := dun()
	status, body = postJSON(t, app, "/progress/swipe", fiber.Map{
		"wordId": ids[1], "direction": "right", "opId": uuid.NewString(), "answeredAt": rfc(at),
	})
	if status != fiber.StatusOK || body["box"] != float64(4) {
		t.Fatalf("dünkü cevap: status = %d body = %v, beklenen 200 kutu 4", status, body)
	}
	wp := loadProgress(t, gdb, u.ID, ids[1])
	yakin(t, "lastReviewDate", wp.LastReviewDate, at, time.Second)
	yakin(t, "nextReviewDate", wp.NextReviewDate, at.AddDate(0, 0, 15), time.Second)
	sonra := loadUser(t, gdb, u.ID)
	if sonra.ReviewAnswersToday != 30 || sonra.LastResetDate != once.LastResetDate {
		t.Errorf("bugünkü sayaç = %d (last_reset %d), beklenen 30 (%d)", sonra.ReviewAnswersToday, sonra.LastResetDate, once.LastResetDate)
	}
	// Cevap kendi gününe tekrar cevabı olarak kaydedildi.
	var gun string
	gdb.Raw(`SELECT to_char(review_day, 'YYYY-MM-DD') FROM applied_ops WHERE user_id = ?`, u.ID).Scan(&gun)
	if gun != services.UsageDate(at) {
		t.Errorf("review_day = %q, beklenen %q", gun, services.UsageDate(at))
	}
}

func TestAnsweredAt_DunkuYanlisCevapBugunBasindaVadeli(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	ids := dueWords(t, gdb, u.ID, "off_dun_yanlis", 4, 1)

	status, body := postJSON(t, app, "/progress/swipe", fiber.Map{
		"wordId": ids[0], "direction": "left", "opId": uuid.NewString(), "answeredAt": rfc(dun()),
	})
	if status != fiber.StatusOK || body["box"] != float64(3) {
		t.Fatalf("status = %d body = %v, beklenen 200 kutu 3", status, body)
	}
	bugun := services.UsageDayStart(time.Now())
	wp := loadProgress(t, gdb, u.ID, ids[0])
	if wp.NextReviewDate == nil || !wp.NextReviewDate.Equal(bugun) {
		t.Errorf("nextReviewDate = %v, beklenen bugünün başı %v", wp.NextReviewDate, bugun)
	}
	// Bugünün tekrar listesinde görünür.
	status, raw := getJSON(t, app, "/progress/review/box/3")
	if status != fiber.StatusOK || !strings.Contains(string(raw), ids[0]) {
		t.Errorf("tekrar listesi = %d %s, kelime bekleniyordu", status, raw)
	}
}

// --- POST /progress/sync ---

func TestSync_IkiCevrimdisiGununCevaplariSiraylaUygulanir(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	vade := time.Now().Add(-72 * time.Hour)
	setProgress(t, gdb, u.ID, models.DictionaryWord{ID: "off_iki_gun", Level: "A1", Grp: 1}, 3, &vade)

	gun1 := time.Now().Add(-48 * time.Hour)
	gun2 := time.Now().Add(-24 * time.Hour)
	results := syncOK(t, app,
		swipeOp("off_iki_gun", "left", gun1),                 // kutu 2, gün 2'nin başında vadeli
		swipeOp("off_iki_gun", "right", gun2),                // kutu 3, gün 2 + 7 gün
		swipeOp("off_iki_gun", "right", gun2.Add(time.Hour)), // aynı gün ikinci cevap: no-op
	)
	sonucBekle(t, results[0], "applied", "")
	sonucBekle(t, results[1], "applied", "")
	sonucBekle(t, results[2], "noop", "")
	if p, _ := results[0]["progress"].(map[string]any); p["box"] != float64(2) {
		t.Errorf("1. sonucun kaydı = %v, beklenen kutu 2", p)
	}

	wp := loadProgress(t, gdb, u.ID, "off_iki_gun")
	if wp.Box != 3 {
		t.Errorf("kutu = %d, beklenen 3", wp.Box)
	}
	yakin(t, "lastReviewDate", wp.LastReviewDate, gun2, time.Second)
	yakin(t, "nextReviewDate", wp.NextReviewDate, gun2.AddDate(0, 0, 7), time.Second)
	if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 0 {
		t.Errorf("review_answers_today = %d, beklenen 0 (geçmiş günlerin cevapları)", got)
	}
}

func TestSync_AyniIslemYenidenGonderilinceUygulanmaz(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	ids := dueWords(t, gdb, u.ID, "off_yeniden", 2, 1)
	op := swipeOp(ids[0], "right", time.Now())

	// Aynı parça iki kez (yanıt kayboldu) ve aynı işlem parçada iki kez.
	ilk := syncOK(t, app, op)
	ikinci := syncOK(t, app, op, op)
	sonucBekle(t, ilk[0], "applied", "")
	for i, r := range ikinci {
		if !reflect.DeepEqual(r, ilk[0]) {
			t.Errorf("%d. tekrar sonucu = %v, beklenen ilk sonuç %v", i, r, ilk[0])
		}
	}
	if wp := loadProgress(t, gdb, u.ID, ids[0]); wp.Box != 3 {
		t.Errorf("kutu = %d, beklenen 3", wp.Box)
	}
	if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 1 {
		t.Errorf("review_answers_today = %d, beklenen 1", got)
	}
}

func TestSync_BugunkuCevaptaKapiBirAralikAsilabilir(t *testing.T) {
	for _, c := range []struct {
		mode       config.GateEnforcement
		wantLast   string
		wantCount  int
		wantReport bool
	}{
		{config.GateEnforce, "rejected", 60, true},
		{config.GateLog, "applied", 61, true},
		{config.GateOff, "applied", 61, false},
	} {
		t.Run(string(c.mode), func(t *testing.T) {
			kayit := kapiRaporunuYakala(t)
			gdb, u, app := newOfflineTest(t, c.mode)
			ids := dueWords(t, gdb, u.ID, "off_tolerans", 2, 31)
			setUsage(t, gdb, u.ID, map[string]any{"review_answers_today": 30})

			ops := make([]fiber.Map, len(ids))
			for i, id := range ids {
				ops[i] = swipeOp(id, "right", time.Now())
			}
			results := syncOK(t, app, ops...)
			for i, r := range results[:30] {
				if r["status"] != "applied" {
					t.Fatalf("%d. cevap (günün %d.) = %v, beklenen applied", i, 31+i, r)
				}
			}
			code := ""
			if c.wantLast == "rejected" {
				code = "review_gate_required"
			}
			sonucBekle(t, results[30], c.wantLast, code)
			if c.wantLast == "rejected" {
				if wp := loadProgress(t, gdb, u.ID, ids[30]); wp.Box != 2 {
					t.Errorf("reddedilen cevabın kelimesi kutu %d, beklenen 2", wp.Box)
				}
				if p, _ := results[30]["progress"].(map[string]any); p["box"] != float64(2) {
					t.Errorf("reddedilen sonucun kaydı = %v, beklenen kutu 2", results[30]["progress"])
				}
			}
			if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != c.wantCount {
				t.Errorf("review_answers_today = %d, beklenen %d", got, c.wantCount)
			}
			if got := len(kayit.ihlaller()) > 0; got != c.wantReport {
				t.Errorf("ihlal raporu = %v, beklenen %v (%v)", got, c.wantReport, kayit.ihlaller())
			}
		})
	}
}

func TestSync_GecmisGunOgrenmesiGununAcilisinaGore(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	groups := testLevel(t, gdb, "A1", 4, 2)
	at := dun()
	gun := services.UsageDate(at)

	// Grup o gün açılmamış: öğrenme ve "Bilmiyorum" reddedilir.
	r := syncOK(t, app,
		learnOp(groups[0][0].ID, at),
		fiber.Map{"opId": uuid.NewString(), "kind": "unknown", "wordId": groups[0][1].ID, "answeredAt": rfc(at)},
	)
	sonucBekle(t, r[0], "rejected", "gate_required")
	sonucBekle(t, r[1], "rejected", "gate_required")
	if _, ok := r[0]["progress"]; ok {
		t.Errorf("kaydı olmayan kelimenin reddinde progress var: %v", r[0])
	}
	if n := progressRowCount(t, gdb, u.ID, groups[0][0].ID); n != 0 {
		t.Fatalf("reddedilen öğrenme yazıldı")
	}

	// O günün ücretsiz grubu çevrimdışı açıldı: aynı günün öğrenmeleri geçer,
	// kutu 1'deki kelimenin o günkü doğru cevabı da.
	r = syncOK(t, app,
		fiber.Map{"opId": uuid.NewString(), "kind": "group_started", "level": "A1", "group": 1, "usageDate": gun, "answeredAt": rfc(at)},
		learnOp(groups[0][0].ID, at),
		fiber.Map{"opId": uuid.NewString(), "kind": "unknown", "wordId": groups[0][1].ID, "answeredAt": rfc(at)},
		swipeOp(groups[0][1].ID, "right", at),
	)
	for _, s := range r {
		sonucBekle(t, s, "applied", "")
	}
	wp := loadProgress(t, gdb, u.ID, groups[0][0].ID)
	if wp.Box != 2 {
		t.Errorf("öğrenilen kelime kutu %d, beklenen 2", wp.Box)
	}
	yakin(t, "lastReviewDate", wp.LastReviewDate, at, time.Second)
	if wp := loadProgress(t, gdb, u.ID, groups[0][1].ID); wp.Box != 2 {
		t.Errorf("kutu 1'den doğru cevaplanan kelime kutu %d, beklenen 2", wp.Box)
	}
	if got := gunlukAcilislar(t, gdb, u.ID); len(got) != 1 || got[0] != (gunlukAcilis{gun, "A1", 1, "offline_free"}) {
		t.Errorf("açılış geçmişi = %+v", got)
	}
	// Bugünün sayaçları değişmedi.
	if after := loadUser(t, gdb, u.ID); after.GroupsStartedTodayCount != 0 || after.UnlockedGroupsToday != "" {
		t.Errorf("bugünkü kullanım = %d %q, beklenen 0 \"\"", after.GroupsStartedTodayCount, after.UnlockedGroupsToday)
	}

	// Ertesi güne (bugüne) ait öğrenme bugünkü kapıya göre denetlenir: grup
	// bugün açılmadı.
	r = syncOK(t, app, learnOp(groups[0][2].ID, time.Now()))
	sonucBekle(t, r[0], "rejected", "gate_required")
}

func TestSync_GroupStarted(t *testing.T) {
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	bugun := services.UsageDate(time.Now())
	dunGun := services.UsageDate(dun())
	if _, err := recordGroupUnlock(gdb, u.ID, services.DefaultCourse, dunGun, "A1", 1, models.UnlockViaFree, time.Now()); err != nil {
		t.Fatal(err)
	}

	gs := func(group int, day string) fiber.Map {
		return fiber.Map{"opId": uuid.NewString(), "kind": "group_started", "level": "A1", "group": group, "usageDate": day}
	}
	r := syncOK(t, app,
		gs(3, bugun),  // bugün: group-started ile aynı
		gs(3, bugun),  // aynı grup yeniden: noop
		gs(2, dunGun), // dün: ücretsiz hak A1:1'e kullanılmıştı → çakışma
		gs(2, dunGun), // aynı satır: noop
		gs(1, services.UsageDateDaysAgo(time.Now(), 8)),
		gs(1, "2026-13-01"),
	)
	sonucBekle(t, r[0], "applied", "")
	sonucBekle(t, r[1], "noop", "")
	sonucBekle(t, r[2], "applied", "free_group_conflict")
	sonucBekle(t, r[3], "noop", "")
	sonucBekle(t, r[4], "rejected", "invalid_date")
	sonucBekle(t, r[5], "rejected", "invalid_date")

	after := loadUser(t, gdb, u.ID)
	if after.GroupsStartedTodayCount != 1 || after.UnlockedGroupsToday != "A1:3" {
		t.Errorf("bugünkü kullanım = %d %q, beklenen 1 \"A1:3\"", after.GroupsStartedTodayCount, after.UnlockedGroupsToday)
	}
	want := []gunlukAcilis{{dunGun, "A1", 1, "free"}, {dunGun, "A1", 2, "offline_free"}, {bugun, "A1", 3, "free"}}
	if got := gunlukAcilislar(t, gdb, u.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("açılış geçmişi = %+v, beklenen %+v", got, want)
	}
	lines := kayit.ihlaller()
	if len(lines) != 1 || !strings.Contains(lines[0], "reason=offline_free_group_conflict") {
		t.Errorf("rapor = %v, beklenen tek offline_free_group_conflict satırı", lines)
	}
}

func TestSync_DailyLogin(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	dunGun := services.UsageDate(dun())
	op := func(day string) fiber.Map {
		return fiber.Map{"opId": uuid.NewString(), "kind": "daily_login", "date": day, "answeredAt": rfc(time.Now())}
	}
	r := syncOK(t, app,
		op(dunGun),
		op(dunGun),
		op(services.UsageDateDaysAgo(time.Now(), 7)),
		op(services.UsageDateDaysAgo(time.Now(), 8)),
		op(services.UsageDateDaysAgo(time.Now(), -1)), // yarın
		op("dün"),
	)
	sonucBekle(t, r[0], "applied", "")
	sonucBekle(t, r[1], "noop", "")
	sonucBekle(t, r[2], "applied", "")
	sonucBekle(t, r[3], "rejected", "invalid_date")
	sonucBekle(t, r[4], "rejected", "invalid_date")
	sonucBekle(t, r[5], "rejected", "invalid_date")

	// created_at kendi gününe düşer: senkronizasyon günü (bugün) girişli sayılmaz.
	got := kayitliGunler(t, gdb, u.ID)
	want := []string{services.UsageDateDaysAgo(time.Now(), 7), dunGun}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("giriş günleri = %v, beklenen %v", got, want)
	}
	var created []time.Time
	gdb.Raw(`SELECT created_at FROM daily_logins WHERE user_id = ?`, u.ID).Scan(&created)
	for _, c := range created {
		if d := services.UsageDate(c); d == services.UsageDate(time.Now()) {
			t.Errorf("created_at bugüne düştü (%v): seri bugünü girişli sayardı", c)
		}
	}
}

func TestSync_AdvanceGroup(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	groups := testLevel(t, gdb, "A1", 2, 2)
	setPointer(t, gdb, u.ID, "A1", 1)
	adv := func() fiber.Map {
		return fiber.Map{"opId": uuid.NewString(), "kind": "advance_group", "level": "A1", "group": 1}
	}

	// Grup görülmedi: işaretçi yerinde, neden previous_not_seen.
	r := syncOK(t, app, adv())
	sonucBekle(t, r[0], "noop", "previous_not_seen")

	for _, w := range groups[0] {
		setProgress(t, gdb, u.ID, w, 2, nil)
	}
	r = syncOK(t, app, adv(), adv())
	sonucBekle(t, r[0], "applied", "")
	sonucBekle(t, r[1], "noop", "") // işaretçi artık 2'de
	if after := loadUser(t, gdb, u.ID); after.CurrentLevel != "A1" || after.CurrentGroup != 2 {
		t.Errorf("işaretçi = %s:%d, beklenen A1:2", after.CurrentLevel, after.CurrentGroup)
	}
}

func TestSync_GecersizIslemlerReddedilirDigerleriSurer(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	ids := dueWords(t, gdb, u.ID, "off_gecersiz", 2, 1)
	gecerli := swipeOp(ids[0], "right", time.Now())
	kotuZaman := uuid.NewString()

	r := syncOK(t, app,
		fiber.Map{"opId": "", "kind": "swipe", "wordId": ids[0], "direction": "right"},
		fiber.Map{"opId": uuid.NewString(), "kind": "fly", "wordId": ids[0]},
		fiber.Map{"opId": kotuZaman, "kind": "swipe", "wordId": ids[0], "direction": "right", "answeredAt": "dün akşam"},
		fiber.Map{"opId": uuid.NewString(), "kind": "swipe", "wordId": ids[0], "direction": "up"},
		fiber.Map{"opId": uuid.NewString(), "kind": "learn", "wordId": "sozlukte_yok"},
		fiber.Map{"opId": uuid.NewString(), "kind": "swipe", "wordId": "kaydi_yok", "direction": "left"},
		fiber.Map{"opId": uuid.NewString(), "kind": "advance_group", "level": "Z9", "group": 1},
		gecerli,
	)
	for i, code := range []string{"invalid_op", "invalid_op", "invalid_op", "invalid_op", "unknown_word", "not_found", "invalid_group"} {
		sonucBekle(t, r[i], "rejected", code)
	}
	sonucBekle(t, r[7], "applied", "")
	if wp := loadProgress(t, gdb, u.ID, ids[0]); wp.Box != 3 {
		t.Errorf("kutu = %d, beklenen 3 (yalnızca geçerli işlem uygulanmalı)", wp.Box)
	}

	// 200'den fazla işlem tek istekte kabul edilmez.
	ops := make([]fiber.Map, maxSyncOps+1)
	for i := range ops {
		ops[i] = swipeOp(ids[0], "left", time.Now())
	}
	status, body, _ := syncIstegi(t, app, ops...)
	if status != fiber.StatusBadRequest || body["code"] != "too_many_ops" {
		t.Errorf("201 işlem: status = %d body = %v, beklenen 400 too_many_ops", status, body)
	}
}

func TestSync_YanitKullaniciVeKullanimiTasir(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	ids := dueWords(t, gdb, u.ID, "off_yanit", 2, 1)

	status, body, _ := syncIstegi(t, app, swipeOp(ids[0], "right", time.Now()))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (%v)", status, body)
	}
	user, _ := body["user"].(map[string]any)
	if user["id"] != u.ID {
		t.Errorf("user = %v, beklenen id %s", user, u.ID)
	}
	usage, _ := body["usage"].(map[string]any)
	if usage["reviewAnswersToday"] != float64(1) {
		t.Errorf("usage = %v, beklenen reviewAnswersToday 1", usage)
	}
	// usage, GET /users/me/usage ile aynı biçimdedir.
	if got := getUsage2(t, app); !reflect.DeepEqual(got, usage) {
		t.Errorf("usage = %v, GET /users/me/usage = %v", usage, got)
	}

	// Boş parça da geçerlidir.
	status, body, results := syncIstegi(t, app)
	if status != fiber.StatusOK || results != nil || fmt.Sprint(body["results"]) != "[]" {
		t.Errorf("boş parça: status = %d body = %v", status, body)
	}
}

// getUsage2, offlineApp'teki GET /users/me/usage'ı çağırır.
func getUsage2(t *testing.T, app *fiber.App) map[string]any {
	t.Helper()
	status, body := send(t, app, httptest.NewRequest(http.MethodGet, "/users/me/usage", nil))
	if status != fiber.StatusOK {
		t.Fatalf("usage status = %d", status)
	}
	return body
}

// Geçmiş günün tekrar cevapları 120'yi aşınca raporlanır, reddedilmez.
func TestSync_GecmisGunFazlaTekrarRaporlanir(t *testing.T) {
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	at := dun()
	// O günün 120 tekrar cevabı zaten kayıtlı.
	for i := 0; i < pastDayReviewReportThreshold; i++ {
		ref := &opRef{id: uuid.NewString(), raw: "x", kind: opKindSwipe}
		if err := claimOp(gdb, u.ID, ref, wordOutcome{status: opApplied}.result(), services.UsageDate(at), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	ids := dueWords(t, gdb, u.ID, "off_fazla", 2, 1)
	r := syncOK(t, app, swipeOp(ids[0], "right", at))
	sonucBekle(t, r[0], "applied", "")
	lines := kayit.ihlaller()
	if len(lines) != 1 || !strings.Contains(lines[0], "reason=past_day_review_overshoot") ||
		!strings.Contains(lines[0], "answers_today=121") {
		t.Errorf("rapor = %v, beklenen tek past_day_review_overshoot satırı (121)", lines)
	}
}

// jsonEsit, iki JSON değerini çözülmüş halleriyle karşılaştırır.
func jsonEsit(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("JSON çözülemedi: %v (%s)", err, a)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("JSON çözülemedi: %v (%s)", err, b)
	}
	return reflect.DeepEqual(x, y)
}

// Aynı istekte grubun cevaplarından sonra gelen advance_group, o cevapların
// yazdığı kayıtları görür: her işlem kendi transaction'ında commit edilir,
// işaretçi ilerler (mobil kuyruk advance_group'u cevaplardan sonra ekler).
func TestSync_AdvanceGroupAyniIstekteCevaplardanSonra(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	groups := testLevel(t, gdb, "A1", 3, 2)
	setPointer(t, gdb, u.ID, "A1", 1)
	now := time.Now()
	gun := services.UsageDate(now)

	ops := []fiber.Map{
		{"opId": uuid.NewString(), "kind": "group_started", "level": "A1", "group": 1, "usageDate": gun, "answeredAt": rfc(now)},
		learnOp(groups[0][0].ID, now),
		{"opId": uuid.NewString(), "kind": "unknown", "wordId": groups[0][1].ID, "answeredAt": rfc(now)},
		learnOp(groups[0][2].ID, now),
		{"opId": uuid.NewString(), "kind": "advance_group", "level": "A1", "group": 1, "answeredAt": rfc(now)},
	}
	r := syncOK(t, app, ops...)
	for _, s := range r {
		sonucBekle(t, s, "applied", "")
	}
	if after := loadUser(t, gdb, u.ID); after.CurrentLevel != "A1" || after.CurrentGroup != 2 {
		t.Errorf("işaretçi = %s:%d, beklenen A1:2 (grup aynı istekte görülmüş sayılmalı)", after.CurrentLevel, after.CurrentGroup)
	}

	// Bütün parça yeniden gönderilirse (yanıt kayboldu) hiçbir şey ikinci kez
	// uygulanmaz: işaretçi A1:2'de kalır, sonuçlar ilk sonuçlardır.
	r = syncOK(t, app, ops...)
	for _, s := range r {
		sonucBekle(t, s, "applied", "")
	}
	if after := loadUser(t, gdb, u.ID); after.CurrentGroup != 2 {
		t.Errorf("yeniden gönderimde işaretçi = %d, beklenen 2", after.CurrentGroup)
	}
	if after := loadUser(t, gdb, u.ID); after.GroupsStartedTodayCount != 1 {
		t.Errorf("groups_started_today_count = %d, beklenen 1", after.GroupsStartedTodayCount)
	}

	// Cevapları eksik grup (son kelime cevaplanmadı) aynı istekte
	// ilerletilemez.
	ops = []fiber.Map{
		learnOp(groups[1][0].ID, now),
		{"opId": uuid.NewString(), "kind": "advance_group", "level": "A1", "group": 2, "answeredAt": rfc(now)},
	}
	setUsage(t, gdb, u.ID, map[string]any{"groups_started_today_count": 2, "unlocked_groups_today": "A1:1,A1:2"})
	r = syncOK(t, app, ops...)
	sonucBekle(t, r[0], "applied", "")
	sonucBekle(t, r[1], "noop", "previous_not_seen")
}

func TestSync_SetCurrentGroup(t *testing.T) {
	gdb, u, app := newOfflineTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 1, 1, 1, 1, 1)
	setPointer(t, gdb, u.ID, "A1", 1)
	set := func(level string, group int) fiber.Map {
		return fiber.Map{"opId": uuid.NewString(), "kind": "set_current_group", "level": level, "group": group, "answeredAt": rfc(time.Now())}
	}

	ileri := set("A1", 3)
	r := syncOK(t, app,
		ileri,
		set("A1", 3), // aynı konum: noop
		set("A1", 2), // geri: noop (PUT gibi yalnızca ileri)
		set("Z9", 1),
		set("A1", 0),
		set("A1", 99), // seviyedeki grup sayısından büyük
	)
	sonucBekle(t, r[0], "applied", "")
	sonucBekle(t, r[1], "noop", "")
	sonucBekle(t, r[2], "noop", "")
	sonucBekle(t, r[3], "rejected", "invalid_group")
	sonucBekle(t, r[4], "rejected", "invalid_group")
	sonucBekle(t, r[5], "rejected", "invalid_group")
	if after := loadUser(t, gdb, u.ID); after.CurrentLevel != "A1" || after.CurrentGroup != 3 {
		t.Fatalf("işaretçi = %s:%d, beklenen A1:3", after.CurrentLevel, after.CurrentGroup)
	}

	// Aynı opId yeniden gelince (işaretçi bu arada geri alınmış olsa da)
	// ikinci kez uygulanmaz, ilk sonuç döner.
	setPointer(t, gdb, u.ID, "A1", 1)
	r = syncOK(t, app, ileri)
	sonucBekle(t, r[0], "applied", "")
	if after := loadUser(t, gdb, u.ID); after.CurrentGroup != 1 {
		t.Errorf("yeniden gönderimde işaretçi = %d, beklenen 1 (uygulanmamalı)", after.CurrentGroup)
	}

	// Açık grup sınırı: üç grup başlanmış ve bitmemişken başlanmamış gruba
	// taşınmaz; reddedilen işlem kaydedilmez, sınır açılınca aynı opId
	// uygulanır.
	for g := 0; g < 3; g++ {
		setProgress(t, gdb, u.ID, a1[g][0], 1, nil)
	}
	setPointer(t, gdb, u.ID, "A1", 3)
	sinir := set("A1", 5)
	r = syncOK(t, app, sinir)
	sonucBekle(t, r[0], "rejected", "open_group_limit")
	if after := loadUser(t, gdb, u.ID); after.CurrentGroup != 3 {
		t.Errorf("sınırda işaretçi = %d, beklenen 3", after.CurrentGroup)
	}
	// Grup 1 tamamlandı.
	if err := gdb.Model(&models.WordProgress{}).Where("user_id = ? AND word_id = ?", u.ID, a1[0][0].ID).
		Update("box", 2).Error; err != nil {
		t.Fatal(err)
	}
	r = syncOK(t, app, sinir)
	sonucBekle(t, r[0], "applied", "")
	if after := loadUser(t, gdb, u.ID); after.CurrentGroup != 5 {
		t.Errorf("sınır açılınca işaretçi = %d, beklenen 5", after.CurrentGroup)
	}
	var n int64
	gdb.Model(&models.AppliedOp{}).Where("user_id = ? AND kind = ?", u.ID, "set_current_group").Count(&n)
	if n != 4 {
		t.Errorf("set_current_group applied_ops = %d, beklenen 4 (applied/noop kaydedilir, ret kaydedilmez)", n)
	}
}
