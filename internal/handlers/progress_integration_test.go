package handlers

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Bu dosya Leitner uçlarını (learn, swipe, tekrar listesi ve sayıları) gerçek
// PostgreSQL'e karşı sınar. Kurulum ve çalıştırma için usage_integration_test.go
// başındaki nota bakın; TEST_DATABASE_URL yoksa testler atlanır.

// progressApp, ilerleme uçlarını auth middleware'i olmadan, userID'yi doğrudan
// enjekte ederek kurar.
func progressApp(h *Handlers, userID string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	inject := func(c *fiber.Ctx) error {
		c.Locals("userID", userID)
		return c.Next()
	}
	app.Get("/progress/word/:wordId", inject, h.GetUserWord)
	app.Get("/progress/group", inject, h.GetProgressForGroup)
	app.Post("/progress/learn", inject, h.LearnWord)
	app.Post("/progress/swipe", inject, h.Swipe)
	app.Get("/progress/review/box/:box", inject, h.ReviewWordsForBox)
	app.Get("/progress/review/box/:box/counts", inject, h.UpcomingCountsForBox)
	return app
}

// testDictWord, sözlüğe tek bir kelime ekler ve test bitince siler. Seviye
// toplamlarına bakan testler bunun yerine testLevel'i kullanmalıdır.
func testDictWord(t *testing.T, gdb *gorm.DB, id, level string, group, order int) models.DictionaryWord {
	t.Helper()

	w := models.DictionaryWord{
		ID:        id,
		English:   "en-" + id,
		Turkish:   "tr-" + id,
		Level:     level,
		Grp:       group,
		WordOrder: order,
	}
	if err := gdb.Create(&w).Error; err != nil {
		t.Fatalf("sözlük kelimesi oluşturulamadı: %v", err)
	}
	t.Cleanup(func() {
		gdb.Delete(&models.DictionaryWord{}, "id = ?", id)
	})
	return w
}

// testLevel, seviyeye sizes[i] kelimelik (i+1). grupları ekler ve kelimeleri
// grup grup döndürür. Seviyede başka kelime varsa (ör. seed yüklenmiş bir
// veritabanı) toplamlar bilinemeyeceği için test atlanır.
func testLevel(t *testing.T, gdb *gorm.DB, level string, sizes ...int) [][]models.DictionaryWord {
	t.Helper()

	var existing int64
	if err := gdb.Model(&models.DictionaryWord{}).Where("level = ?", level).Count(&existing).Error; err != nil {
		t.Fatalf("sözlük okunamadı: %v", err)
	}
	if existing > 0 {
		t.Skipf("%s seviyesinde sözlük kelimesi var (%d); boş bir test veritabanı gerekir", level, existing)
	}

	groups := make([][]models.DictionaryWord, len(sizes))
	for g, n := range sizes {
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("itest_%s_%d_%d", level, g+1, i+1)
			groups[g] = append(groups[g], testDictWord(t, gdb, id, level, g+1, i+1))
		}
	}
	return groups
}

// setProgress, kullanıcının kelimesi için verilen kutu ve sonraki tekrar
// tarihiyle bir word_progress satırı yazar.
func setProgress(t *testing.T, gdb *gorm.DB, userID string, w models.DictionaryWord, box int, next *time.Time) {
	t.Helper()

	wp := &models.WordProgress{
		UserID:         userID,
		WordID:         w.ID,
		Level:          w.Level,
		Grp:            w.Grp,
		English:        w.English,
		Turkish:        w.Turkish,
		Box:            box,
		NextReviewDate: next,
	}
	if err := gdb.Create(wp).Error; err != nil {
		t.Fatalf("kelime ilerlemesi oluşturulamadı: %v", err)
	}
}

func loadProgress(t *testing.T, gdb *gorm.DB, userID, wordID string) models.WordProgress {
	t.Helper()

	var wp models.WordProgress
	if err := gdb.First(&wp, "user_id = ? AND word_id = ?", userID, wordID).Error; err != nil {
		t.Fatalf("kelime ilerlemesi okunamadı: %v", err)
	}
	return wp
}

func getJSON(t *testing.T, app *fiber.App, path string) (int, []byte) {
	t.Helper()

	res, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil), -1)
	if err != nil {
		t.Fatalf("istek başarısız: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("yanıt okunamadı: %v", err)
	}
	return res.StatusCode, body
}

func newProgressTest(t *testing.T) (*gorm.DB, *models.User, *fiber.App) {
	t.Helper()

	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	return gdb, u, progressApp(h, u.ID)
}

// --- POST /progress/learn ---

func learnRequest(payload any) *http.Request {
	return jsonRequest("/progress/learn", payload)
}

func TestLearnWord_AlanlarSozluktenAlinir(t *testing.T) {
	gdb, u, app := newProgressTest(t)
	w := testDictWord(t, gdb, "itest_learn_1", "A1", 3, 1)

	// İstemci farklı seviye/grup/metin gönderse de sözlük satırı esas alınır.
	status, body := send(t, app, learnRequest(fiber.Map{
		"wordId": w.ID, "level": "C2", "group": 99, "english": "uydurma", "turkish": "uydurma",
	}))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, beklenen 200 (body: %v)", status, body)
	}

	wp := loadProgress(t, gdb, u.ID, w.ID)
	if wp.Level != "A1" || wp.Grp != 3 || wp.English != w.English || wp.Turkish != w.Turkish {
		t.Errorf("kayıt = %s/%d %q %q, beklenen sözlükteki A1/3 %q %q",
			wp.Level, wp.Grp, wp.English, wp.Turkish, w.English, w.Turkish)
	}
	if wp.Box != 2 {
		t.Errorf("box = %d, beklenen 2", wp.Box)
	}
	if body["level"] != "A1" || intField(t, body, "group") != 3 || intField(t, body, "box") != 2 {
		t.Errorf("yanıt sözlükteki değerleri taşımıyor: %v", body)
	}
}

func TestLearnWord_SozlukteOlmayanKelime404(t *testing.T) {
	gdb, u, app := newProgressTest(t)

	status, body := send(t, app, learnRequest(fiber.Map{
		"wordId": "itest_yok", "level": "A1", "group": 1, "english": "x", "turkish": "y",
	}))
	if status != fiber.StatusNotFound || body["code"] != "unknown_word" {
		t.Fatalf("status = %d code = %v, beklenen 404 unknown_word", status, body["code"])
	}
	var cnt int64
	gdb.Model(&models.WordProgress{}).Where("user_id = ?", u.ID).Count(&cnt)
	if cnt != 0 {
		t.Errorf("sözlükte olmayan kelime için %d satır yazıldı", cnt)
	}
}

func TestLearnWord_GecersizGovde400(t *testing.T) {
	_, _, app := newProgressTest(t)

	status, body := send(t, app, learnRequest(fiber.Map{"level": "A1"}))
	if status != fiber.StatusBadRequest {
		t.Fatalf("status = %d, beklenen 400 (body: %v)", status, body)
	}
}

func TestLearnWord_IlerlemisKelimeyiSifirlamaz(t *testing.T) {
	cases := []struct {
		name string
		box  int
	}{
		{"kutu 2", 2},
		{"kutu 4", 4},
		{"ogrenilmis (kutu 6)", 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gdb, u, app := newProgressTest(t)
			w := testDictWord(t, gdb, fmt.Sprintf("itest_learn_box%d", c.box), "A1", 1, 1)
			next := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Microsecond)
			setProgress(t, gdb, u.ID, w, c.box, &next)
			gdb.Model(&models.WordProgress{}).Where("user_id = ? AND word_id = ?", u.ID, w.ID).
				Updates(map[string]any{"wrong_count": 2, "learned": c.box == 6})
			before := loadProgress(t, gdb, u.ID, w.ID)

			status, body := send(t, app, learnRequest(fiber.Map{"wordId": w.ID, "level": "A1", "group": 1}))
			if status != fiber.StatusOK {
				t.Fatalf("status = %d, beklenen 200 (body: %v)", status, body)
			}

			after := loadProgress(t, gdb, u.ID, w.ID)
			if after.Box != before.Box || after.WrongCount != 2 || after.Learned != before.Learned ||
				!after.NextReviewDate.Equal(*before.NextReviewDate) {
				t.Errorf("ilerleme değişti: önce box=%d wrong=%d learned=%v next=%v, sonra box=%d wrong=%d learned=%v next=%v",
					before.Box, before.WrongCount, before.Learned, before.NextReviewDate,
					after.Box, after.WrongCount, after.Learned, after.NextReviewDate)
			}
			// Yanıt, mevcut durumu döner.
			if got := intField(t, body, "box"); got != c.box {
				t.Errorf("yanıttaki box = %d, beklenen %d (mevcut durum)", got, c.box)
			}
		})
	}
}

func TestLearnWord_IkinciCagriIdempotent(t *testing.T) {
	gdb, u, app := newProgressTest(t)
	w := testDictWord(t, gdb, "itest_learn_twice", "A1", 1, 1)

	payload := fiber.Map{"wordId": w.ID, "level": "A1", "group": 1, "english": "x", "turkish": "y"}
	if status, body := send(t, app, learnRequest(payload)); status != fiber.StatusOK {
		t.Fatalf("1. learn status = %d (body: %v)", status, body)
	}
	first := loadProgress(t, gdb, u.ID, w.ID)

	if status, body := send(t, app, learnRequest(payload)); status != fiber.StatusOK {
		t.Fatalf("2. learn status = %d (body: %v)", status, body)
	}
	second := loadProgress(t, gdb, u.ID, w.ID)

	if second.ID != first.ID || second.Box != 2 ||
		!second.LastReviewDate.Equal(*first.LastReviewDate) || !second.NextReviewDate.Equal(*first.NextReviewDate) {
		t.Errorf("ikinci learn kaydı değiştirdi: önce %+v, sonra %+v", first, second)
	}
	var cnt int64
	gdb.Model(&models.WordProgress{}).Where("user_id = ?", u.ID).Count(&cnt)
	if cnt != 1 {
		t.Errorf("satır sayısı = %d, beklenen 1", cnt)
	}
}

func TestLearnWord_Kutu1KaydiKutu2yeAlinir(t *testing.T) {
	// Firestore'dan gelen eski kutu 1 kayıtları öğrenilince kutu 2'ye geçer.
	gdb, u, app := newProgressTest(t)
	w := testDictWord(t, gdb, "itest_learn_box1", "A1", 1, 1)
	setProgress(t, gdb, u.ID, w, 1, nil)

	status, body := send(t, app, learnRequest(fiber.Map{"wordId": w.ID}))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	wp := loadProgress(t, gdb, u.ID, w.ID)
	if wp.Box != 2 || wp.NextReviewDate == nil {
		t.Errorf("box = %d next = %v, beklenen kutu 2 ve planlanmış tekrar", wp.Box, wp.NextReviewDate)
	}
	if intField(t, body, "box") != 2 {
		t.Errorf("yanıttaki box = %v, beklenen 2", body["box"])
	}
}

// --- POST /progress/swipe ---

func progressSwipe(wordID, direction string) *http.Request {
	return jsonRequest("/progress/swipe", fiber.Map{"wordId": wordID, "direction": direction})
}

// usageDay, şu anın kullanım günü sınırlarıdır (bugün 00:00 ve yarın 00:00).
func usageDay() (start, end time.Time) {
	start = services.UsageDayStart(time.Now())
	return start, start.AddDate(0, 0, 1)
}

func TestSwipe_VadesiGelmemisKelimeDegismezVeSayilmaz(t *testing.T) {
	_, end := usageDay()
	cases := []struct {
		name      string
		box       int
		next      *time.Time
		direction string
	}{
		{"kutu 2 yarin gun basi (sag)", 2, &end, "right"},
		{"kutu 3 uc gun sonra (sol)", 3, ptrTime(end.Add(48 * time.Hour)), "left"},
		{"kutu 5 bir ay sonra (sag)", 5, ptrTime(end.AddDate(0, 0, 29)), "right"},
		// Öğrenilmiş kelime bir daha sorulmaz; gelen swipe onu değiştirmez.
		{"kutu 6 (sol)", 6, nil, "left"},
		{"kutu 6 (sag)", 6, nil, "right"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gdb, u, app := newProgressTest(t)
			w := models.DictionaryWord{ID: "itest_notdue", Level: "A1", Grp: 1}
			setProgress(t, gdb, u.ID, w, c.box, c.next)
			before := loadProgress(t, gdb, u.ID, w.ID)

			status, body := send(t, app, progressSwipe(w.ID, c.direction))
			if status != fiber.StatusOK {
				t.Fatalf("status = %d, beklenen 200 (body: %v)", status, body)
			}
			after := loadProgress(t, gdb, u.ID, w.ID)
			if after.Box != c.box || after.WrongCount != before.WrongCount || !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Errorf("vadesi gelmemiş kelime değişti: box %d → %d, wrong %d → %d",
					c.box, after.Box, before.WrongCount, after.WrongCount)
			}
			if got := intField(t, body, "box"); got != c.box {
				t.Errorf("yanıttaki box = %d, beklenen mevcut kutu %d", got, c.box)
			}
			if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 0 {
				t.Errorf("review_answers_today = %d, beklenen 0 (cevap sayılmamalı)", got)
			}
		})
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestSwipe_BugunBitmedenVadesiGelenKelimeIlerler(t *testing.T) {
	// Vade, kullanım gününün sonuna (yarın 00:00, hariç) kadardır.
	_, end := usageDay()
	gdb, u, app := newProgressTest(t)
	w := models.DictionaryWord{ID: "itest_due_late", Level: "A1", Grp: 1}
	setProgress(t, gdb, u.ID, w, 2, ptrTime(end.Add(-time.Minute)))

	status, body := send(t, app, progressSwipe(w.ID, "right"))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	if got := loadProgress(t, gdb, u.ID, w.ID).Box; got != 3 {
		t.Errorf("box = %d, beklenen 3", got)
	}
	if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 1 {
		t.Errorf("review_answers_today = %d, beklenen 1", got)
	}
}

func TestSwipe_YinelenenCevapIkinciKezIlerletmez(t *testing.T) {
	// Yanıtı kaybolan bir swipe'ın yeniden gönderilmesi: ilk istek kelimeyi
	// kutu 4'e taşıyıp vadesini 15 gün sonraya atmıştır, ikincisi no-op'tur.
	gdb, u, app := newProgressTest(t)
	w := models.DictionaryWord{ID: "itest_dup", Level: "A1", Grp: 1}
	setProgress(t, gdb, u.ID, w, 3, nil)

	for i := 0; i < 2; i++ {
		status, body := send(t, app, progressSwipe(w.ID, "right"))
		if status != fiber.StatusOK {
			t.Fatalf("%d. swipe status = %d (body: %v)", i+1, status, body)
		}
		if got := intField(t, body, "box"); got != 4 {
			t.Errorf("%d. swipe yanıtında box = %d, beklenen 4", i+1, got)
		}
	}
	if got := loadProgress(t, gdb, u.ID, w.ID).Box; got != 4 {
		t.Errorf("box = %d, beklenen 4 (tek ilerleme)", got)
	}
	if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 1 {
		t.Errorf("review_answers_today = %d, beklenen 1 (tek cevap)", got)
	}
}

func TestSwipe_AyniKelimeyeEszamanliCevaplarTekIlerleme(t *testing.T) {
	gdb, u, app := newProgressTest(t)
	w := models.DictionaryWord{ID: "itest_concurrent_" + u.ID, Level: "A1", Grp: 1}
	setProgress(t, gdb, u.ID, w, 2, nil)

	const n = 5
	statuses := satirKilidiAltindaEszamanli(t, gdb, app,
		"SELECT 1 FROM word_progresses WHERE word_id = ? FOR UPDATE", w.ID, n,
		func(int) *http.Request { return progressSwipe(w.ID, "right") })
	for _, s := range statuses {
		if s != fiber.StatusOK {
			t.Fatalf("eşzamanlı swipe status = %d, beklenen 200 (tümü: %v)", s, statuses)
		}
	}
	if got := loadProgress(t, gdb, u.ID, w.ID).Box; got != 3 {
		t.Errorf("box = %d, beklenen 3 (tek ilerleme)", got)
	}
	if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 1 {
		t.Errorf("review_answers_today = %d, beklenen 1 (tek cevap)", got)
	}
}

func TestSwipe_YanlisCevapBirAltKutuVeYarin(t *testing.T) {
	_, end := usageDay()
	gdb, u, app := newProgressTest(t)
	w := models.DictionaryWord{ID: "itest_wrong", Level: "A1", Grp: 1}
	setProgress(t, gdb, u.ID, w, 4, nil)

	status, body := send(t, app, progressSwipe(w.ID, "left"))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	wp := loadProgress(t, gdb, u.ID, w.ID)
	if wp.Box != 3 || wp.WrongCount != 1 {
		t.Errorf("box = %d wrong = %d, beklenen box 3 wrong 1", wp.Box, wp.WrongCount)
	}
	if wp.NextReviewDate == nil || !wp.NextReviewDate.Equal(end) {
		t.Errorf("nextReviewDate = %v, beklenen yarın gün başı %v", wp.NextReviewDate, end)
	}
	if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 1 {
		t.Errorf("review_answers_today = %d, beklenen 1", got)
	}
}

func TestSwipe_Kutu6yaGecisTotalLearnedGunceller(t *testing.T) {
	gdb, u, app := newProgressTest(t)
	setProgress(t, gdb, u.ID, models.DictionaryWord{ID: "itest_learned_a", Level: "A1", Grp: 1}, 6, nil)
	setProgress(t, gdb, u.ID, models.DictionaryWord{ID: "itest_learned_b", Level: "A1", Grp: 1}, 5, nil)

	if status, body := send(t, app, progressSwipe("itest_learned_b", "right")); status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	if got := loadUser(t, gdb, u.ID).TotalLearned; got != 2 {
		t.Errorf("total_learned = %d, beklenen 2", got)
	}
}
