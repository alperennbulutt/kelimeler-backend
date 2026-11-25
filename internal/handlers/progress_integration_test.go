package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/dto"
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

// --- GET /progress/review/box/:box ve /counts ---

func reviewList(t *testing.T, app *fiber.App, path string) []map[string]any {
	t.Helper()

	status, raw := getJSON(t, app, path)
	if status != fiber.StatusOK {
		t.Fatalf("GET %s status = %d (body: %s)", path, status, raw)
	}
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("liste çözülemedi: %v (body: %s)", err, raw)
	}
	return list
}

func reviewCounts(t *testing.T, app *fiber.App, path string) dto.UpcomingCounts {
	t.Helper()

	status, raw := getJSON(t, app, path)
	if status != fiber.StatusOK {
		t.Fatalf("GET %s status = %d (body: %s)", path, status, raw)
	}
	var counts dto.UpcomingCounts
	if err := json.Unmarshal(raw, &counts); err != nil {
		t.Fatalf("sayılar çözülemedi: %v (body: %s)", err, raw)
	}
	return counts
}

func wordIDs(list []map[string]any) []string {
	ids := make([]string, 0, len(list))
	for _, w := range list {
		ids = append(ids, fmt.Sprint(w["wordId"]))
	}
	return ids
}

func TestReviewBox_BugunKullanimSaatDilimineGore(t *testing.T) {
	gdb, u, app := newProgressTest(t)
	loc := services.UsageLocation()
	at := func(y int, m time.Month, d, hh, mm int, l *time.Location) *time.Time {
		v := time.Date(y, m, d, hh, mm, 0, 0, l)
		return &v
	}
	word := func(id string) models.DictionaryWord {
		return models.DictionaryWord{ID: id, Level: "A1", Grp: 1}
	}

	// ?date=2030-01-15, kullanım saat diliminde 15 Ocak demektir.
	setProgress(t, gdb, u.ID, word("r_nil"), 2, nil)
	setProgress(t, gdb, u.ID, word("r_past"), 2, at(2030, 1, 10, 12, 0, loc))
	setProgress(t, gdb, u.ID, word("r_today_late"), 2, at(2030, 1, 15, 23, 59, loc))
	// UTC'de hâlâ 15 Ocak, kullanım saat diliminde (UTC+3) 16 Ocak 01:30: yarın.
	setProgress(t, gdb, u.ID, word("r_utc_today"), 2, at(2030, 1, 15, 22, 30, time.UTC))
	setProgress(t, gdb, u.ID, word("r_tomorrow"), 2, at(2030, 1, 16, 0, 1, loc))
	setProgress(t, gdb, u.ID, word("r_day3"), 2, at(2030, 1, 18, 10, 0, loc))
	setProgress(t, gdb, u.ID, word("r_far"), 2, at(2030, 2, 20, 10, 0, loc))
	setProgress(t, gdb, u.ID, word("r_other_box"), 3, nil)

	if loc.String() == "UTC" {
		t.Skip("kullanım saat dilimi UTC; saat dilimi farkı sınanamaz")
	}

	list := reviewList(t, app, "/progress/review/box/2?date=2030-01-15")
	want := []string{"r_nil", "r_past", "r_today_late"}
	if got := wordIDs(list); !reflect.DeepEqual(got, want) {
		t.Errorf("liste = %v, beklenen %v (sıra: tarihsiz önce, sonra tarih)", got, want)
	}

	counts := reviewCounts(t, app, "/progress/review/box/2/counts?date=2030-01-15")
	wantCounts := dto.UpcomingCounts{
		Today:           3,
		Tomorrow:        2,
		Later:           1,
		DailyCounts:     map[string]int{"0": 3, "1": 2, "3": 1},
		TotalMonthCount: 6,
		BusiestDayLabel: "1 gün sonra",
		BusiestDayCount: 2,
	}
	if !reflect.DeepEqual(counts, wantCounts) {
		t.Errorf("sayılar = %+v, beklenen %+v", counts, wantCounts)
	}
}

func TestReviewBox_ListeUzunluguBugunSayisinaEsit(t *testing.T) {
	gdb, u, app := newProgressTest(t)
	start, end := usageDay()
	offsets := []*time.Time{
		nil,
		ptrTime(start.AddDate(0, 0, -5)),
		ptrTime(start),
		ptrTime(end.Add(-time.Second)),
		ptrTime(end),
		ptrTime(end.Add(time.Second)),
		ptrTime(end.AddDate(0, 0, 6)),
	}
	for i, next := range offsets {
		setProgress(t, gdb, u.ID, models.DictionaryWord{ID: fmt.Sprintf("eq_%d", i), Level: "A1", Grp: 1}, 4, next)
	}

	for _, q := range []string{"", "?date=" + start.Format("2006-01-02")} {
		list := reviewList(t, app, "/progress/review/box/4"+q)
		counts := reviewCounts(t, app, "/progress/review/box/4/counts"+q)
		if len(list) != counts.Today || counts.Today != 4 {
			t.Errorf("%q: liste %d kelime, counts.today %d; ikisi de 4 olmalı", q, len(list), counts.Today)
		}
		if counts.Tomorrow != 2 {
			t.Errorf("%q: counts.tomorrow = %d, beklenen 2", q, counts.Tomorrow)
		}
	}
}

func TestReviewBox_LimitVeSabitSiralama(t *testing.T) {
	gdb, u, app := newProgressTest(t)
	start, _ := usageDay()
	same := start.Add(-time.Hour)
	// Aynı tarihli kelimeler eklendikleri sırayla (id) gelir.
	for _, id := range []string{"l_c", "l_a", "l_b"} {
		setProgress(t, gdb, u.ID, models.DictionaryWord{ID: id, Level: "A1", Grp: 1}, 2, &same)
	}
	setProgress(t, gdb, u.ID, models.DictionaryWord{ID: "l_old", Level: "A1", Grp: 1}, 2, ptrTime(start.AddDate(0, 0, -3)))
	setProgress(t, gdb, u.ID, models.DictionaryWord{ID: "l_nil", Level: "A1", Grp: 1}, 2, nil)

	if got, want := wordIDs(reviewList(t, app, "/progress/review/box/2")), []string{"l_nil", "l_old", "l_c", "l_a", "l_b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("limitsiz liste = %v, beklenen %v", got, want)
	}
	if got, want := wordIDs(reviewList(t, app, "/progress/review/box/2?limit=3")), []string{"l_nil", "l_old", "l_c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("limit=3 liste = %v, beklenen %v", got, want)
	}

	for _, bad := range []string{"0", "201", "-1", "abc"} {
		status, raw := getJSON(t, app, "/progress/review/box/2?limit="+bad)
		if status != fiber.StatusBadRequest {
			t.Errorf("limit=%s status = %d, beklenen 400 (body: %s)", bad, status, raw)
		}
	}
}

func TestReviewBox_GecersizKutuBosListe(t *testing.T) {
	gdb, u, app := newProgressTest(t)
	setProgress(t, gdb, u.ID, models.DictionaryWord{ID: "b_1", Level: "A1", Grp: 1}, 2, nil)

	for _, box := range []string{"0", "7", "abc"} {
		if list := reviewList(t, app, "/progress/review/box/"+box); len(list) != 0 {
			t.Errorf("kutu %s: %d kelime, beklenen boş liste", box, len(list))
		}
		if counts := reviewCounts(t, app, "/progress/review/box/"+box+"/counts"); counts.Today != 0 || counts.TotalMonthCount != 0 {
			t.Errorf("kutu %s: sayılar %+v, beklenen sıfır", box, counts)
		}
	}
}

// --- Veritabanı hataları ---

func TestUclar_VeritabaniHatasindaSahte200Donmez(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)

	// Kapalı havuz her sorguda hata verir: ulaşılamayan veritabanının karşılığı.
	kopuk, err := db.Connect(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("ikinci bağlantı açılamadı: %v", err)
	}
	sqlDB, err := kopuk.DB()
	if err != nil {
		t.Fatalf("havuz alınamadı: %v", err)
	}
	sqlDB.Close()
	h := New(kopuk, &config.Config{AppEnv: "test"}, nil, nil)

	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("userID", u.ID)
		return c.Next()
	})
	app.Get("/progress/word/:wordId", h.GetUserWord)
	app.Get("/progress/group", h.GetProgressForGroup)
	app.Post("/progress/learn", h.LearnWord)
	app.Post("/progress/swipe", h.Swipe)
	app.Get("/progress/review/box/:box", h.ReviewWordsForBox)
	app.Get("/progress/review/box/:box/counts", h.UpcomingCountsForBox)
	app.Patch("/users/me", h.UpdateProfile)
	app.Put("/users/me/current-group", h.SetCurrentGroup)
	app.Post("/users/me/advance-group", h.AdvanceGroup)
	app.Post("/users/me/usage/reset", h.ResetDailyUsage)
	app.Post("/users/me/usage/no-fill-pass", h.GrantNoFillPass)
	app.Get("/map/levels/:level/groups", h.LevelGroups)
	app.Get("/map/levels/:level/max-group", h.MaxGroupForLevel)
	app.Get("/map/active-boxes-count", h.ActiveBoxesCount)
	app.Get("/home/learned-count", h.LearnedCount)
	app.Get("/home/current-group", h.CurrentGroup)
	app.Get("/dictionary/words", h.GetWordsForGroup)
	app.Post("/dictionary/words/batch", h.GetWordsByIds)

	jsonReq := func(method, path string, body any) *http.Request {
		req := jsonRequest(path, body)
		req.Method = method
		return req
	}
	get := func(path string) *http.Request { return httptest.NewRequest(http.MethodGet, path, nil) }
	cases := []struct {
		name string
		req  *http.Request
	}{
		{"kelime ilerlemesi", get("/progress/word/w1")},
		{"grup ilerlemesi", get("/progress/group?level=A1&group=1")},
		{"learn", jsonRequest("/progress/learn", fiber.Map{"wordId": "w1"})},
		{"swipe", jsonRequest("/progress/swipe", fiber.Map{"wordId": "w1", "direction": "right"})},
		{"tekrar listesi", get("/progress/review/box/2")},
		{"tekrar sayilari", get("/progress/review/box/2/counts")},
		{"profil", jsonReq(http.MethodPatch, "/users/me", fiber.Map{"displayName": "Ad"})},
		{"kaldigin grup", jsonReq(http.MethodPut, "/users/me/current-group", fiber.Map{"level": "A1", "group": 1})},
		{"sonraki grup", httptest.NewRequest(http.MethodPost, "/users/me/advance-group", nil)},
		{"sayac sifirlama", httptest.NewRequest(http.MethodPost, "/users/me/usage/reset", nil)},
		{"no-fill gecisi", httptest.NewRequest(http.MethodPost, "/users/me/usage/no-fill-pass", nil)},
		{"harita gruplari", get("/map/levels/A2/groups")},
		{"max grup", get("/map/levels/A1/max-group")},
		{"aktif kutular", get("/map/active-boxes-count")},
		{"ogrenilen sayisi", get("/home/learned-count")},
		{"ana sayfa grubu", get("/home/current-group")},
		{"sozluk grubu", get("/dictionary/words?level=A1&group=1")},
		{"sozluk toplu", jsonRequest("/dictionary/words/batch", fiber.Map{"ids": []string{"w1"}})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := send(t, app, c.req)
			if status != fiber.StatusInternalServerError {
				t.Errorf("status = %d, beklenen 500 (body: %v)", status, body)
			}
		})
	}
}

// --- QA: eşzamanlılık ve gün sınırı ---

// eszamanli, n isteği aynı anda başlatır (kilit beklemeden) ve durum kodlarını
// döner.
func eszamanli(app *fiber.App, n int, build func(i int) *http.Request) []int {
	start := make(chan struct{})
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		req := build(i)
		go func() {
			<-start
			res, err := app.Test(req, -1)
			if err != nil {
				done <- 0
				return
			}
			res.Body.Close()
			done <- res.StatusCode
		}()
	}
	close(start)
	statuses := make([]int, 0, n)
	for i := 0; i < n; i++ {
		statuses = append(statuses, <-done)
	}
	return statuses
}

func TestLearnWord_EszamanliAyniKelimeTekKayit(t *testing.T) {
	// İki cihazdan ya da yinelenen istekle aynı kelime aynı anda öğrenilir:
	// tek satır oluşur, hepsi 200 döner, kutu 2'dir.
	gdb, u, app := newProgressTest(t)
	w := testDictWord(t, gdb, "itest_learn_conc", "A1", 1, 1)

	statuses := eszamanli(app, 8, func(int) *http.Request {
		return learnRequest(fiber.Map{"wordId": w.ID})
	})
	for _, s := range statuses {
		if s != fiber.StatusOK {
			t.Fatalf("eşzamanlı learn status = %d, beklenen 200 (tümü: %v)", s, statuses)
		}
	}
	var cnt int64
	gdb.Model(&models.WordProgress{}).Where("user_id = ? AND word_id = ?", u.ID, w.ID).Count(&cnt)
	if cnt != 1 {
		t.Errorf("satır sayısı = %d, beklenen 1", cnt)
	}
	if got := loadProgress(t, gdb, u.ID, w.ID).Box; got != 2 {
		t.Errorf("box = %d, beklenen 2", got)
	}
}

func TestLearnWord_EszamanliIlerlemisKelimeyiSifirlamaz(t *testing.T) {
	gdb, u, app := newProgressTest(t)
	w := testDictWord(t, gdb, "itest_learn_conc_adv", "A1", 1, 1)
	next := time.Now().AddDate(0, 0, 10)
	setProgress(t, gdb, u.ID, w, 4, &next)

	statuses := eszamanli(app, 6, func(int) *http.Request {
		return learnRequest(fiber.Map{"wordId": w.ID})
	})
	for _, s := range statuses {
		if s != fiber.StatusOK {
			t.Fatalf("status = %d (tümü: %v)", s, statuses)
		}
	}
	if got := loadProgress(t, gdb, u.ID, w.ID).Box; got != 4 {
		t.Errorf("box = %d, beklenen 4 (ilerlemiş kelime sıfırlanmamalı)", got)
	}
}

func TestSwipe_FarkliKelimelerEszamanliSayacVeTotalLearnedKaybolmaz(t *testing.T) {
	// Aynı kullanıcının farklı kelimelerine eşzamanlı doğru cevaplar: her biri
	// sayılır (30/60/90 kapıları) ve kutu 6'ya giren her kelime total_learned'e
	// yansır; COUNT birbirinin commit'ini görmeden eskimez.
	gdb, u, app := newProgressTest(t)
	const n = 6
	for i := 0; i < n; i++ {
		setProgress(t, gdb, u.ID, models.DictionaryWord{ID: fmt.Sprintf("itest_conc5_%d", i), Level: "A1", Grp: 1}, 5, nil)
	}

	statuses := kilitAltindaEszamanli(t, gdb, app, u.ID, n, func(i int) *http.Request {
		return progressSwipe(fmt.Sprintf("itest_conc5_%d", i), "right")
	})
	for _, s := range statuses {
		if s != fiber.StatusOK {
			t.Fatalf("status = %d (tümü: %v)", s, statuses)
		}
	}
	user := loadUser(t, gdb, u.ID)
	if user.ReviewAnswersToday != n {
		t.Errorf("review_answers_today = %d, beklenen %d", user.ReviewAnswersToday, n)
	}
	if user.TotalLearned != n {
		t.Errorf("total_learned = %d, beklenen %d", user.TotalLearned, n)
	}
	var learned int64
	gdb.Model(&models.WordProgress{}).Where("user_id = ? AND box = 6", u.ID).Count(&learned)
	if learned != n {
		t.Errorf("kutu 6'daki kelime = %d, beklenen %d", learned, n)
	}
}

func TestSwipe_EszamanliSagVeSolTekCevap(t *testing.T) {
	// İki cihazdan aynı kelimeye biri doğru biri yanlış cevap: hangisi önce
	// gelirse uygulanır, ikincisi vadesi geçtiği için no-op'tur.
	gdb, u, app := newProgressTest(t)
	w := models.DictionaryWord{ID: "itest_conc_lr", Level: "A1", Grp: 1}
	setProgress(t, gdb, u.ID, w, 3, nil)

	dirs := []string{"right", "left"}
	statuses := satirKilidiAltindaEszamanli(t, gdb, app,
		"SELECT 1 FROM word_progresses WHERE word_id = ? FOR UPDATE", w.ID, 2,
		func(i int) *http.Request { return progressSwipe(w.ID, dirs[i]) })
	for _, s := range statuses {
		if s != fiber.StatusOK {
			t.Fatalf("status = %d (tümü: %v)", s, statuses)
		}
	}
	wp := loadProgress(t, gdb, u.ID, w.ID)
	if wp.Box != 4 && wp.Box != 2 {
		t.Errorf("box = %d, beklenen 4 (önce doğru) ya da 2 (önce yanlış)", wp.Box)
	}
	if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 1 {
		t.Errorf("review_answers_today = %d, beklenen 1", got)
	}
}

func TestSwipe_ListedekiHerKelimeKabulEdilirDigerleriNoOp(t *testing.T) {
	// Kullanım günü (İstanbul) gece yarısı sınırında tekrar listesi ile swipe
	// vadesi aynı kümeyi kullanır: listede olan kelime ilerler, olmayan değişmez.
	start, end := usageDay()
	if time.Until(end) < 10*time.Second {
		t.Skip("kullanım gününün bitimine çok yakın; sınır kayabilir")
	}
	gdb, u, app := newProgressTest(t)
	nexts := map[string]*time.Time{
		"itest_bd_nil":       nil,
		"itest_bd_prevday":   ptrTime(start.Add(-time.Second)),
		"itest_bd_midnight":  ptrTime(start),
		"itest_bd_lastsec":   ptrTime(end.Add(-time.Second)),
		"itest_bd_tomorrow":  ptrTime(end),
		"itest_bd_tomorrow1": ptrTime(end.Add(time.Second)),
	}
	for id, next := range nexts {
		setProgress(t, gdb, u.ID, models.DictionaryWord{ID: id, Level: "A1", Grp: 1}, 3, next)
	}

	inList := map[string]bool{}
	for _, id := range wordIDs(reviewList(t, app, "/progress/review/box/3")) {
		inList[id] = true
	}
	if len(inList) != 4 {
		t.Fatalf("listede %d kelime, beklenen 4: %v", len(inList), inList)
	}
	for id := range nexts {
		if status, body := send(t, app, progressSwipe(id, "right")); status != fiber.StatusOK {
			t.Fatalf("%s swipe status = %d (body: %v)", id, status, body)
		}
		got := loadProgress(t, gdb, u.ID, id).Box
		want := 3
		if inList[id] {
			want = 4
		}
		if got != want {
			t.Errorf("%s: box = %d, beklenen %d (listede: %v)", id, got, want, inList[id])
		}
	}
	if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 4 {
		t.Errorf("review_answers_today = %d, beklenen 4", got)
	}
}

func TestLearnWord_SeviyesiFarkliEskiKayitIlerlemesiKorunarakDuzeltilir(t *testing.T) {
	// Firestore'dan gelen, seviyesi/grubu sözlükten farklı yazılmış ilerlemiş
	// bir kayıt: öğrenme ekranı onu grup sorgusunda göremez ve learn çağırır.
	// Kutu korunmalı ama seviye/grup sözlüğe eşitlenmeli; yoksa harita grubu
	// hiçbir zaman tamamlanmış saymaz ve sonraki grup kilitli kalır.
	gdb, u, app := newProgressTest(t)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	words := testLevel(t, gdb, "A1", 2)[0]
	next := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Microsecond)
	setProgress(t, gdb, u.ID, words[0], 2, nil)
	setProgress(t, gdb, u.ID, models.DictionaryWord{ID: words[1].ID, Level: "B1", Grp: 7}, 4, &next)

	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": words[1].ID})); status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	wp := loadProgress(t, gdb, u.ID, words[1].ID)
	if wp.Box != 4 || wp.NextReviewDate == nil || !wp.NextReviewDate.Equal(next) {
		t.Errorf("ilerleme değişti: box=%d next=%v, beklenen box 4 next %v", wp.Box, wp.NextReviewDate, next)
	}
	if wp.Level != "A1" || wp.Grp != 1 || wp.English != words[1].English {
		t.Errorf("sözlük alanları = %s:%d %q, beklenen A1:1 %q", wp.Level, wp.Grp, wp.English, words[1].English)
	}

	groups, err := h.computeLevelGroups(u.ID, services.DefaultCourse, "A1")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || !groups[0].IsCompleted || groups[0].RemainingInBox1 != 0 {
		t.Errorf("A1 grupları = %+v, beklenen grup 1 tamamlanmış", groups)
	}
}
