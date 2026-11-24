package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Bu dosya kurs başına ilerlemeyi (X-Course) gerçek PostgreSQL'e karşı sınar.
// Kurulum için usage_integration_test.go başındaki nota bakın;
// TEST_DATABASE_URL yoksa testler atlanır.

// courseApp, ilerlemeyle ilgili bütün uçları userID enjekte ederek kurar;
// yollar routes.go'dakilerle aynıdır.
func courseApp(h *Handlers, userID string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	inject := func(c *fiber.Ctx) error {
		c.Locals("userID", userID)
		return c.Next()
	}
	app.Get("/progress/word/:wordId", inject, h.GetUserWord)
	app.Get("/progress/group", inject, h.GetProgressForGroup)
	app.Post("/progress/learn", inject, h.LearnWord)
	app.Post("/progress/unknown", inject, h.MarkUnknown)
	app.Post("/progress/swipe", inject, h.Swipe)
	app.Get("/progress/review/box/:box", inject, h.ReviewWordsForBox)
	app.Get("/progress/review/box/:box/counts", inject, h.UpcomingCountsForBox)
	app.Put("/users/me/current-group", inject, h.SetCurrentGroup)
	app.Post("/users/me/advance-group", inject, h.AdvanceGroup)
	app.Get("/users/me/usage", inject, h.GetDailyUsage)
	app.Post("/users/me/usage/group-started", inject, h.UnlockGroup(freeGroupColumn))
	app.Post("/users/me/usage/group-unlocked-by-ad", inject, h.UnlockGroup("groups_unlocked_by_ad_today"))
	app.Get("/map/levels/:level/groups", inject, h.LevelGroups)
	app.Get("/map/active-boxes-count", inject, h.ActiveBoxesCount)
	app.Get("/map/open-groups", inject, h.OpenGroups)
	app.Get("/home/learned-count", inject, h.LearnedCount)
	app.Get("/home/current-group", inject, h.CurrentGroup)
	app.Get("/stats/summary", inject, h.StatsSummary)
	app.Get("/stats/learned-words", inject, h.LearnedWords)
	app.Post("/progress/sync", inject, h.SyncProgress)
	app.Get("/sync/snapshot", inject, h.SyncSnapshot)
	return app
}

func newCourseTest(t *testing.T, mode config.GateEnforcement) (*gorm.DB, *models.User, *fiber.App) {
	t.Helper()

	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test", GateEnforcement: mode}, nil, nil)
	return gdb, u, courseApp(h, u.ID)
}

// kursIstegi, verilen kursla (boşsa başlıksız) bir istek kurar; payload
// verilirse JSON gövdelidir.
func kursIstegi(method, path, course string, payload any) *http.Request {
	var body io.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		body = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, body)
	if payload != nil {
		req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	if course != "" {
		req.Header.Set(CourseHeader, course)
	}
	return req
}

// kursGet, kursla GET atar ve 200 bekleyip gövdeyi out'a çözer.
func kursGet(t *testing.T, app *fiber.App, path, course string, out any) {
	t.Helper()

	res, err := app.Test(kursIstegi(http.MethodGet, path, course, nil), -1)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != fiber.StatusOK {
		t.Fatalf("GET %s (kurs %q) status = %d, beklenen 200 (body: %s)", path, course, res.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("GET %s yanıtı çözülemedi: %v (%s)", path, err, raw)
	}
}

// kursSatiri, kullanıcının kurstaki kelime satırını okur; yoksa ok=false.
func kursSatiri(t *testing.T, gdb *gorm.DB, userID, course, wordID string) (models.WordProgress, bool) {
	t.Helper()

	var rows []models.WordProgress
	if err := gdb.Where("user_id = ? AND course = ? AND word_id = ?", userID, course, wordID).Find(&rows).Error; err != nil {
		t.Fatalf("kelime ilerlemesi okunamadı: %v", err)
	}
	if len(rows) == 0 {
		return models.WordProgress{}, false
	}
	return rows[0], true
}

// setCourseProgress, kullanıcının kurstaki kelimesi için vadesi gelmiş
// (next_review_date NULL) bir satır yazar.
func setCourseProgress(t *testing.T, gdb *gorm.DB, userID, course string, w models.DictionaryWord, box int) {
	t.Helper()

	wp := &models.WordProgress{
		UserID: userID, Course: course, WordID: w.ID, Level: w.Level, Grp: w.Grp,
		English: w.English, Turkish: w.Turkish, Box: box,
	}
	if err := gdb.Create(wp).Error; err != nil {
		t.Fatalf("kelime ilerlemesi oluşturulamadı: %v", err)
	}
}

// (a) + (c): aynı kelime iki kursta iki bağımsız satırdır; birinin cevabı
// diğerine dokunmaz. Kurs göndermeyen istek İngilizce kursudur.
func TestKurs_AyniKelimeIkiKurstaBagimsiz(t *testing.T) {
	gdb, u, app := newCourseTest(t, config.GateOff)
	w := testLevel(t, gdb, "A1", 2)[0][0]

	// Başlıksız learn: İngilizce kursu.
	if status, body := send(t, app, kursIstegi(http.MethodPost, "/progress/learn", "", fiber.Map{"wordId": w.ID})); status != fiber.StatusOK {
		t.Fatalf("başlıksız learn: status = %d body = %v, beklenen 200", status, body)
	}
	if status, body := send(t, app, kursIstegi(http.MethodPost, "/progress/learn", "de", fiber.Map{"wordId": w.ID})); status != fiber.StatusOK {
		t.Fatalf("de learn: status = %d body = %v, beklenen 200", status, body)
	}
	en, okEn := kursSatiri(t, gdb, u.ID, "en", w.ID)
	de, okDe := kursSatiri(t, gdb, u.ID, "de", w.ID)
	if !okEn || !okDe || en.ID == de.ID || en.Box != 2 || de.Box != 2 {
		t.Fatalf("satırlar: en=%+v (%v) de=%+v (%v); iki ayrı kutu 2 satırı beklenir", en, okEn, de, okDe)
	}

	// de satırını vadesi gelmiş yap ve de kursunda doğru cevapla.
	if err := gdb.Model(&models.WordProgress{}).Where("id = ?", de.ID).Update("next_review_date", nil).Error; err != nil {
		t.Fatal(err)
	}
	status, body := send(t, app, kursIstegi(http.MethodPost, "/progress/swipe", "de", fiber.Map{"wordId": w.ID, "direction": "right"}))
	if status != fiber.StatusOK || body["box"] != float64(3) {
		t.Fatalf("de swipe: status = %d body = %v, beklenen 200 box=3", status, body)
	}
	// Yanıtın alanları değişmedi: kurs yanıta yazılmaz.
	if _, ok := body["course"]; ok {
		t.Errorf("swipe yanıtında course alanı var: %v", body)
	}
	enAfter, _ := kursSatiri(t, gdb, u.ID, "en", w.ID)
	if enAfter.Box != en.Box || !enAfter.NextReviewDate.Equal(*en.NextReviewDate) || enAfter.WrongCount != en.WrongCount {
		t.Errorf("de cevabı en satırını değiştirdi: önce %+v, sonra %+v", en, enAfter)
	}

	// en satırı vadesi gelmemiş: başlıksız swipe no-op; de satırı aynı kalır.
	deAfter, _ := kursSatiri(t, gdb, u.ID, "de", w.ID)
	if status, body := send(t, app, kursIstegi(http.MethodPost, "/progress/swipe", "", fiber.Map{"wordId": w.ID, "direction": "left"})); status != fiber.StatusOK || body["box"] != float64(2) {
		t.Fatalf("başlıksız swipe: status = %d body = %v, beklenen 200 box=2 (en satırı)", status, body)
	}
	if got, _ := kursSatiri(t, gdb, u.ID, "de", w.ID); got.Box != deAfter.Box || got.UpdatedAt != deAfter.UpdatedAt {
		t.Errorf("en cevabı de satırını değiştirdi: önce %+v, sonra %+v", deAfter, got)
	}

	// GET /progress/word kursun satırını döner; ?course= yedeği de çalışır.
	var got models.WordProgress
	// (de satırı kutu 3, en satırı kutu 2.)
	kursGet(t, app, "/progress/word/"+w.ID, "de", &got)
	if got.Box != 3 {
		t.Errorf("de kelime = %+v, beklenen box=3", got)
	}
	kursGet(t, app, "/progress/word/"+w.ID+"?course=de", "", &got)
	if got.Box != 3 {
		t.Errorf("?course=de kelime = %+v, beklenen box=3", got)
	}
	kursGet(t, app, "/progress/word/"+w.ID, "", &got)
	if got.Box != 2 {
		t.Errorf("başlıksız kelime = %+v, beklenen box=2", got)
	}
	// Başlık sorgu parametresinden önce gelir; büyük harf kabul edilir.
	kursGet(t, app, "/progress/word/"+w.ID+"?course=de", "EN", &got)
	if got.Box != 2 {
		t.Errorf("X-Course: EN + ?course=de kelime = %+v, beklenen en satırı (box=2)", got)
	}

	// Kursta kaydı olmayan kelime 404'tür.
	if status, _ := send(t, app, kursIstegi(http.MethodGet, "/progress/word/"+w.ID, "fr", nil)); status != fiber.StatusNotFound {
		t.Errorf("fr kelime status = %d, beklenen 404", status)
	}

	// "Bilmiyorum" de kursa özeldir: fr'de yeni kutu 1 satırı açar.
	if status, body := send(t, app, kursIstegi(http.MethodPost, "/progress/unknown", "fr", fiber.Map{"wordId": w.ID})); status != fiber.StatusOK || body["box"] != float64(1) {
		t.Fatalf("fr unknown: status = %d body = %v, beklenen 200 box=1", status, body)
	}
	if fr, ok := kursSatiri(t, gdb, u.ID, "fr", w.ID); !ok || fr.Box != 1 {
		t.Errorf("fr satırı = %+v (%v), beklenen kutu 1", fr, ok)
	}
	var n int64
	gdb.Model(&models.WordProgress{}).Where("user_id = ? AND word_id = ?", u.ID, w.ID).Count(&n)
	if n != 3 {
		t.Errorf("kelimenin satır sayısı = %d, beklenen 3 (en, de, fr)", n)
	}
}

// (b): harita, açık gruplar, tekrar listeleri/sayıları ve istatistik kurs
// başınadır.
func TestKurs_HaritaTekrarVeIstatistikKursBasina(t *testing.T) {
	gdb, u, app := newCourseTest(t, config.GateOff)
	groups := testLevel(t, gdb, "A1", 2, 2)

	// de: 1. grup tamamlandı (biri kutu 2, biri kutu 6).
	setCourseProgress(t, gdb, u.ID, "de", groups[0][0], 2)
	setCourseProgress(t, gdb, u.ID, "de", groups[0][1], 6)
	// en: 1. grup yarım (açık grup).
	setCourseProgress(t, gdb, u.ID, "en", groups[0][0], 1)

	var de, en []dto.LevelGroupDTO
	kursGet(t, app, "/map/levels/A1/groups", "de", &de)
	kursGet(t, app, "/map/levels/A1/groups", "", &en)
	if len(de) != 2 || len(en) != 2 {
		t.Fatalf("grup sayısı de=%d en=%d, beklenen 2", len(de), len(en))
	}
	if !de[0].IsCompleted || !de[1].IsUnlocked || de[0].SeenWords != 2 {
		t.Errorf("de harita = %+v; 1. grup tamamlanmış, 2. grup açık beklenir", de)
	}
	if en[0].IsCompleted || en[0].SeenWords != 1 || en[1].IsUnlocked {
		t.Errorf("en harita = %+v; 1. grup yarım, 2. grup kilitli beklenir", en)
	}

	var deOpen, enOpen dto.OpenGroupsDTO
	kursGet(t, app, "/map/open-groups", "de", &deOpen)
	kursGet(t, app, "/map/open-groups", "en", &enOpen)
	if deOpen.OpenGroupCount != 0 || enOpen.OpenGroupCount != 1 {
		t.Errorf("açık grup de=%d en=%d, beklenen 0 ve 1", deOpen.OpenGroupCount, enOpen.OpenGroupCount)
	}

	var deCounts, enCounts dto.UpcomingCounts
	kursGet(t, app, "/progress/review/box/2/counts", "de", &deCounts)
	kursGet(t, app, "/progress/review/box/2/counts", "", &enCounts)
	if deCounts.Today != 1 || enCounts.Today != 0 {
		t.Errorf("kutu 2 bugün de=%d en=%d, beklenen 1 ve 0", deCounts.Today, enCounts.Today)
	}
	var deList, enList []map[string]any
	kursGet(t, app, "/progress/review/box/2", "de", &deList)
	kursGet(t, app, "/progress/review/box/2", "", &enList)
	if len(deList) != 1 || deList[0]["wordId"] != groups[0][0].ID || len(enList) != 0 {
		t.Errorf("kutu 2 listesi de=%v en=%v", deList, enList)
	}

	var deActive, enActive map[string]any
	kursGet(t, app, "/map/active-boxes-count", "de", &deActive)
	kursGet(t, app, "/map/active-boxes-count", "", &enActive)
	if deActive["count"] != float64(1) || enActive["count"] != float64(0) {
		t.Errorf("aktif kutular de=%v en=%v, beklenen 1 ve 0", deActive, enActive)
	}

	var deGroup, enGroup []models.WordProgress
	kursGet(t, app, "/progress/group?level=A1&group=1", "de", &deGroup)
	kursGet(t, app, "/progress/group?level=A1&group=1", "", &enGroup)
	if len(deGroup) != 2 || len(enGroup) != 1 {
		t.Errorf("grup ilerlemesi de=%d en=%d satır, beklenen 2 ve 1", len(deGroup), len(enGroup))
	}

	var deLearned, enLearned map[string]any
	kursGet(t, app, "/home/learned-count", "de", &deLearned)
	kursGet(t, app, "/home/learned-count", "", &enLearned)
	if deLearned["learnedCount"] != float64(1) || enLearned["learnedCount"] != float64(0) {
		t.Errorf("öğrenilen de=%v en=%v, beklenen 1 ve 0", deLearned, enLearned)
	}

	var deSummary, enSummary map[string]any
	kursGet(t, app, "/stats/summary", "de", &deSummary)
	kursGet(t, app, "/stats/summary", "", &enSummary)
	if deSummary["totalLearned"] != float64(1) || deSummary["box2Count"] != float64(1) ||
		enSummary["totalLearned"] != float64(0) || enSummary["box2Count"] != float64(0) {
		t.Errorf("özet de=(%v, %v) en=(%v, %v)", deSummary["totalLearned"], deSummary["box2Count"],
			enSummary["totalLearned"], enSummary["box2Count"])
	}
	var dePage dto.LearnedWordsPageDTO
	kursGet(t, app, "/stats/learned-words", "de", &dePage)
	if dePage.Total != 1 || len(dePage.Items) != 1 || dePage.Items[0].WordID != groups[0][1].ID {
		t.Errorf("de öğrenilen kelimeler = %+v", dePage)
	}
	var enPage dto.LearnedWordsPageDTO
	kursGet(t, app, "/stats/learned-words", "", &enPage)
	if enPage.Total != 0 {
		t.Errorf("en öğrenilen kelimeler = %+v, beklenen boş", enPage)
	}
}

// Seviye rozeti (kullanıcı başına) yalnızca İngilizce kursunda önceki
// seviyeyi açar: başka kursta seviye o kursun ilerlemesiyle tamamlanır ve
// orada kazanılan seviye rozeti yazılmaz.
func TestKurs_SeviyeRozetiYalnizcaIngilizceKursunuAcar(t *testing.T) {
	gdb, u, app := newCourseTest(t, config.GateOff)
	a1 := testLevel(t, gdb, "A1", 1)
	testLevel(t, gdb, "A2", 1)
	services.ResetLevelTotalsCache()
	t.Cleanup(services.ResetLevelTotalsCache)

	if err := gdb.Create(&models.EarnedBadge{UserID: u.ID, BadgeID: "level_A1"}).Error; err != nil {
		t.Fatalf("rozet yazılamadı: %v", err)
	}
	var en, de []dto.LevelGroupDTO
	kursGet(t, app, "/map/levels/A2/groups", "", &en)
	kursGet(t, app, "/map/levels/A2/groups", "de", &de)
	if !en[0].IsUnlocked || de[0].IsUnlocked || de[0].LockReason != "previous_level" {
		t.Errorf("A2: en=%+v de=%+v; en açık, de previous_level beklenir", en[0], de[0])
	}

	// de'de A1 tamamlanınca de'de A2 açılır.
	setCourseProgress(t, gdb, u.ID, "de", a1[0][0], 2)
	kursGet(t, app, "/map/levels/A2/groups", "de", &de)
	if !de[0].IsUnlocked {
		t.Errorf("de A1 tamamlanınca A2 = %+v, beklenen açık", de[0])
	}

	// fr'de A1 tamamlanır; fr özeti rozet yazmaz (en'in rozeti zaten var,
	// bu yüzden ayrı bir kullanıcıyla bakılır).
	u2 := testUser(t, gdb)
	app2 := courseApp(New(gdb, &config.Config{AppEnv: "test", GateEnforcement: config.GateOff}, nil, nil), u2.ID)
	setCourseProgress(t, gdb, u2.ID, "fr", a1[0][0], 2)
	var summary dto.StatsSummaryDTO
	kursGet(t, app2, "/stats/summary", "fr", &summary)
	var n int64
	gdb.Model(&models.EarnedBadge{}).Where("user_id = ? AND badge_id LIKE 'level_%'", u2.ID).Count(&n)
	if n != 0 {
		t.Errorf("fr kursu %d seviye rozeti yazdı, beklenen 0", n)
	}
	for _, b := range summary.NewlyEarnedBadges {
		if isLevelBadge(b.ID) {
			t.Errorf("fr özetinde yeni seviye rozeti: %s", b.ID)
		}
	}
	// Rozet fr'nin ilerlemesiyle o an kazanılmış görünür.
	earned := false
	for _, b := range summary.Badges {
		if b.ID == "level_A1" {
			earned = b.IsEarned
		}
	}
	if !earned {
		t.Error("fr özetinde level_A1 kazanılmış görünmüyor")
	}
	// en kursunda u2'nin A2'si hâlâ kilitli.
	kursGet(t, app2, "/map/levels/A2/groups", "", &en)
	if en[0].IsUnlocked {
		t.Errorf("fr ilerlemesi en A2'yi açtı: %+v", en[0])
	}
}

// (d): desteklenmeyen kurs 400 invalid_course; işaretçi uçları İngilizce
// dışı kursta 400 course_not_supported.
func TestKurs_GecersizKurs400(t *testing.T) {
	gdb, _, app := newCourseTest(t, config.GateOff)
	w := testLevel(t, gdb, "A1", 1)[0][0]

	cases := []struct {
		method, path string
		payload      any
	}{
		{http.MethodGet, "/progress/word/" + w.ID, nil},
		{http.MethodGet, "/progress/group?level=A1&group=1", nil},
		{http.MethodPost, "/progress/learn", fiber.Map{"wordId": w.ID}},
		{http.MethodPost, "/progress/unknown", fiber.Map{"wordId": w.ID}},
		{http.MethodPost, "/progress/swipe", fiber.Map{"wordId": w.ID, "direction": "right"}},
		{http.MethodGet, "/progress/review/box/2", nil},
		{http.MethodGet, "/progress/review/box/2/counts", nil},
		{http.MethodPost, "/users/me/usage/group-started", fiber.Map{"level": "A1", "group": 1}},
		{http.MethodPost, "/users/me/usage/group-unlocked-by-ad", fiber.Map{"level": "A1", "group": 1}},
		{http.MethodPut, "/users/me/current-group", fiber.Map{"level": "A1", "group": 1}},
		{http.MethodPost, "/users/me/advance-group", nil},
		{http.MethodGet, "/map/levels/A1/groups", nil},
		{http.MethodGet, "/map/active-boxes-count", nil},
		{http.MethodGet, "/map/open-groups", nil},
		{http.MethodGet, "/home/learned-count", nil},
		{http.MethodGet, "/home/current-group", nil},
		{http.MethodGet, "/stats/summary", nil},
		{http.MethodGet, "/stats/learned-words", nil},
		{http.MethodPost, "/progress/sync", fiber.Map{"ops": []fiber.Map{}}},
		{http.MethodGet, "/sync/snapshot", nil},
	}
	for _, c := range cases {
		// Başlıkta ve sorgu parametresinde.
		for _, req := range []*http.Request{
			kursIstegi(c.method, c.path, "xx", c.payload),
			kursIstegi(c.method, c.path+sorguEki(c.path)+"course=klingon", "", c.payload),
		} {
			status, body := send(t, app, req)
			if status != fiber.StatusBadRequest || body["code"] != "invalid_course" {
				t.Errorf("%s %s: status = %d code = %v, beklenen 400 invalid_course", c.method, req.URL, status, body["code"])
			}
		}
	}

	for _, c := range []struct {
		method, path string
		payload      any
	}{
		{http.MethodPut, "/users/me/current-group", fiber.Map{"level": "A1", "group": 1}},
		{http.MethodPost, "/users/me/advance-group", nil},
		{http.MethodGet, "/home/current-group", nil},
	} {
		status, body := send(t, app, kursIstegi(c.method, c.path, "de", c.payload))
		if status != fiber.StatusBadRequest || body["code"] != "course_not_supported" {
			t.Errorf("de %s %s: status = %d code = %v, beklenen 400 course_not_supported", c.method, c.path, status, body["code"])
		}
		if status, body := send(t, app, kursIstegi(c.method, c.path, "en", c.payload)); status != fiber.StatusOK {
			t.Errorf("en %s %s: status = %d body = %v, beklenen 200", c.method, c.path, status, body)
		}
	}
}

func sorguEki(path string) string {
	if bytes.ContainsRune([]byte(path), '?') {
		return "&"
	}
	return "?"
}

// (e): İngilizce dışı kursun açılış anahtarı kurs önekli yazılır ve kapı kurs
// başına uygulanır; gövdesiz (eski istemci) açılış yalnızca İngilizce kursunu
// karşılar.
func TestKurs_AcilisAnahtariKursOnekliVeKapiKursBasina(t *testing.T) {
	gdb, u, app := newCourseTest(t, config.GateEnforce)
	words := testLevel(t, gdb, "A1", 3)[0]

	learn := func(course string, w models.DictionaryWord) (int, map[string]any) {
		return send(t, app, kursIstegi(http.MethodPost, "/progress/learn", course, fiber.Map{"wordId": w.ID}))
	}
	usage := func() []string {
		var u dto.DailyUsageDTO
		kursGet(t, app, "/users/me/usage", "", &u)
		return u.UnlockedGroupsToday
	}

	// de kursunda grubu aç: anahtar "de/A1:1".
	if status, body := send(t, app, kursIstegi(http.MethodPost, "/users/me/usage/group-started", "de", fiber.Map{"level": "A1", "group": 1})); status != fiber.StatusOK {
		t.Fatalf("de group-started: status = %d body = %v", status, body)
	}
	if got := usage(); !slices.Equal(got, []string{"de/A1:1"}) {
		t.Fatalf("unlockedGroupsToday = %v, beklenen [de/A1:1]", got)
	}

	// en kursunda aynı grup açık değil.
	if status, body := learn("", words[0]); status != fiber.StatusForbidden || body["code"] != "gate_required" {
		t.Errorf("en learn: status = %d code = %v, beklenen 403 gate_required", status, body["code"])
	}
	if _, ok := kursSatiri(t, gdb, u.ID, "en", words[0].ID); ok {
		t.Error("reddedilen en learn satır yazdı")
	}
	// de kursunda açık.
	if status, body := learn("de", words[0]); status != fiber.StatusOK {
		t.Errorf("de learn: status = %d body = %v, beklenen 200", status, body)
	}
	// es kursunda da açık değil.
	if status, body := learn("es", words[0]); status != fiber.StatusForbidden || body["code"] != "gate_required" {
		t.Errorf("es learn: status = %d code = %v, beklenen 403 gate_required", status, body["code"])
	}

	// en kursunda reklamla aç: anahtar eski biçimde "A1:1".
	if status, body := send(t, app, kursIstegi(http.MethodPost, "/users/me/usage/group-unlocked-by-ad", "", fiber.Map{"level": "A1", "group": 1})); status != fiber.StatusOK {
		t.Fatalf("en group-unlocked-by-ad: status = %d body = %v", status, body)
	}
	if got := usage(); !slices.Equal(got, []string{"de/A1:1", "A1:1"}) {
		t.Fatalf("unlockedGroupsToday = %v, beklenen [de/A1:1 A1:1]", got)
	}
	if status, body := learn("", words[0]); status != fiber.StatusOK {
		t.Errorf("açılıştan sonra en learn: status = %d body = %v, beklenen 200", status, body)
	}
	// Aynı grubu de kursunda yeniden açmak sayacı artırmaz (idempotent).
	before := loadUser(t, gdb, u.ID).GroupsStartedTodayCount
	send(t, app, kursIstegi(http.MethodPost, "/users/me/usage/group-started", "de", fiber.Map{"level": "A1", "group": 1}))
	if after := loadUser(t, gdb, u.ID).GroupsStartedTodayCount; after != before {
		t.Errorf("de grubunu yeniden açmak sayacı %d → %d yaptı", before, after)
	}

	// Gövdesiz açılış (eski istemci): yalnızca İngilizce kursunu karşılar.
	u2 := testUser(t, gdb)
	app2 := courseApp(New(gdb, &config.Config{AppEnv: "test", GateEnforcement: config.GateEnforce}, nil, nil), u2.ID)
	setUsage(t, gdb, u2.ID, map[string]any{"groups_started_today_count": 1})
	if status, body := send(t, app2, kursIstegi(http.MethodPost, "/progress/learn", "fr", fiber.Map{"wordId": words[1].ID})); status != fiber.StatusForbidden || body["code"] != "gate_required" {
		t.Errorf("gövdesiz açılışla fr learn: status = %d code = %v, beklenen 403 gate_required", status, body["code"])
	}
	if status, body := send(t, app2, kursIstegi(http.MethodPost, "/progress/learn", "", fiber.Map{"wordId": words[1].ID})); status != fiber.StatusOK {
		t.Errorf("gövdesiz açılışla en learn: status = %d body = %v, beklenen 200", status, body)
	}
}
