package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/db/dbtest"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Bu dosya gerçek bir PostgreSQL gerektirir; no-fill sınırı, gün dönümü
// sıfırlaması ve grup açma koşullu UPDATE ... WHERE ile uygulandığı için
// yalnızca gerçek veritabanında (satır kilitleriyle) doğrulanabilir.
//
// Çalıştırmak için (depo kökünden):
//
//	make db-up            # docker compose'daki Postgres (localhost:5434)
//	make test-integration
//
// Başka bir veritabanına karşı:
//
//	TEST_DATABASE_URL="postgres://KULLANICI@localhost:5432/kelimeler_itest?sslmode=disable" \
//	  go test ./internal/handlers/ -v
//
// Testler şemayı migrate eder ve kullanıcı satırları yazıp siler; canlı
// veritabanını değil, test için ayrılmış bir veritabanını göster.
//
// TEST_DATABASE_URL tanımlı değilse testler lokalde atlanır, CI'da (CI=true)
// başarısız olur (bkz. dbtest.DSN).
func testDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := dbtest.DSN(t)

	gdb, err := db.Connect(dsn)
	if err != nil {
		t.Fatalf("veritabanına bağlanılamadı: %v", err)
	}
	// Her test kendi havuzunu açar; kapatılmazsa boşta kalan bağlantılar birikip
	// (özellikle -count ile) PostgreSQL'in max_connections sınırını tüketir.
	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			sqlDB.Close()
		}
	})
	if err := db.Migrate(gdb); err != nil {
		t.Fatalf("migrate başarısız: %v", err)
	}
	return gdb
}

// testUser, benzersiz e-postalı bir kullanıcı oluşturur ve test bitince siler.
func testUser(t *testing.T, gdb *gorm.DB) *models.User {
	t.Helper()

	u := &models.User{
		Email:       "test-" + time.Now().Format("20060102150405.000000000") + "@example.test",
		DisplayName: "Test",
	}
	if err := gdb.Create(u).Error; err != nil {
		t.Fatalf("kullanıcı oluşturulamadı: %v", err)
	}
	t.Cleanup(func() {
		gdb.Unscoped().Delete(&models.User{}, "id = ?", u.ID)
	})
	return u
}

// testApp, auth middleware'ini atlayıp userID'yi doğrudan enjekte eden bir
// Fiber uygulaması kurar. Amaç token akışını değil, sayaç mantığını sınamak.
func testApp(h *Handlers, userID string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	inject := func(c *fiber.Ctx) error {
		c.Locals("userID", userID)
		return c.Next()
	}
	app.Post("/no-fill-pass", inject, h.GrantNoFillPass)
	app.Get("/usage", inject, h.GetDailyUsage)
	app.Post("/swipe", inject, h.Swipe)
	app.Post("/group-started", inject, h.UnlockGroup("groups_started_today_count"))
	app.Post("/group-unlocked-by-ad", inject, h.UnlockGroup("groups_unlocked_by_ad_today"))
	app.Post("/review-ad-pass", inject, h.RecordReviewAdPass)
	return app
}

// testWord, kullanıcı için verilen kutuda bir word_progress satırı açar.
// Satır, kullanıcı silinince CASCADE ile temizlenir.
func testWord(t *testing.T, gdb *gorm.DB, userID, wordID string, box int) {
	t.Helper()

	wp := &models.WordProgress{
		UserID: userID,
		WordID: wordID,
		Level:  "A1",
		Grp:    1,
		Box:    box,
	}
	if err := gdb.Create(wp).Error; err != nil {
		t.Fatalf("kelime ilerlemesi oluşturulamadı: %v", err)
	}
}

// dunkuKullanim, tüm günlük sayaçları 5'e çeker ve son sıfırlamayı düne taşır:
// gün dönümünden sonraki ilk isteğin karşılaşacağı durum.
func dunkuKullanim(t *testing.T, gdb *gorm.DB, userID string) {
	t.Helper()

	err := gdb.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
		"groups_started_today_count":  5,
		"groups_unlocked_by_ad_today": 5,
		"review_ad_pass_count_today":  5,
		"no_fill_passes_today":        5,
		"review_answers_today":        5,
		"unlocked_groups_today":       "A1:1,A1:2",
		"last_reset_date":             time.Now().AddDate(0, 0, -1).UnixMilli(),
	}).Error
	if err != nil {
		t.Fatalf("dünkü kullanım hazırlanamadı: %v", err)
	}
}

func loadUser(t *testing.T, gdb *gorm.DB, userID string) models.User {
	t.Helper()

	var u models.User
	if err := gdb.First(&u, "id = ?", userID).Error; err != nil {
		t.Fatalf("kullanıcı okunamadı: %v", err)
	}
	return u
}

func jsonRequest(path string, payload any) *http.Request {
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	return req
}

func swipeRequest(wordID, direction string) *http.Request {
	return jsonRequest("/swipe", fiber.Map{"wordId": wordID, "direction": direction})
}

func send(t *testing.T, app *fiber.App, req *http.Request) (int, map[string]any) {
	t.Helper()

	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("istek başarısız: %v", err)
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	return res.StatusCode, decoded
}

// postJSON, JSON gövdeli bir POST atar (Content-Type dahil; gövdesiz istek
// BodyParser'dan geçemez).
func postJSON(t *testing.T, app *fiber.App, path string, payload any) (int, map[string]any) {
	t.Helper()
	return send(t, app, jsonRequest(path, payload))
}

func getUsage(t *testing.T, app *fiber.App) map[string]any {
	t.Helper()

	status, body := send(t, app, httptest.NewRequest(http.MethodGet, "/usage", nil))
	if status != fiber.StatusOK {
		t.Fatalf("GET /usage status = %d, beklenen 200 (body: %v)", status, body)
	}
	return body
}

// intField, JSON gövdesindeki sayı alanını okur; alan yoksa testi düşürür
// (istemci alanın yokluğunu "sunucu bu sayacı tutmuyor" diye yorumlar).
func intField(t *testing.T, body map[string]any, key string) int {
	t.Helper()

	v, ok := body[key].(float64)
	if !ok {
		t.Fatalf("%q alanı yok ya da sayı değil (body: %v)", key, body)
	}
	return int(v)
}

func unlockedGroups(t *testing.T, body map[string]any) []string {
	t.Helper()

	raw, ok := body["unlockedGroupsToday"].([]any)
	if !ok {
		t.Fatalf("unlockedGroupsToday yok ya da dizi değil (body: %v)", body)
	}
	keys := make([]string, 0, len(raw))
	for _, k := range raw {
		keys = append(keys, k.(string))
	}
	return keys
}

// kilitAltindaEszamanli, kullanıcı satırını kilitli tutarken n isteği başlatır
// ve hepsi o satırda beklemeye geçince kilidi bırakır; status kodlarını döner
// (gönderilemeyen istek 0 sayılır).
//
// Yarışı zamanlamaya bırakmaz, en kötü sıralamayı zorlar: bütün istekler aynı
// (kilit öncesi) durumu görmüş ve aynı satırı yazmak için sıraya girmiş olur.
// "Önce oku, sonra koşulsuz yaz" türü bir uygulama burada her seferinde yanlış
// sonuç üretir; yerel veritabanında serbest bırakılan istekler ise çoğu zaman
// üst üste binmeden biter ve hatayı gizler.
func kilitAltindaEszamanli(t *testing.T, gdb *gorm.DB, app *fiber.App, userID string, n int, build func(i int) *http.Request) []int {
	t.Helper()
	return satirKilidiAltindaEszamanli(t, gdb, app, "SELECT 1 FROM users WHERE id = ? FOR UPDATE", userID, n, build)
}

// satirKilidiAltindaEszamanli, kilitAltindaEszamanli'nin genel halidir: kilitSQL
// ile seçilen satırı (ör. "SELECT 1 FROM tablo WHERE id = ? FOR UPDATE") kilitli
// tutar ve isteklerin o satırda beklemesini bekler.
func satirKilidiAltindaEszamanli(t *testing.T, gdb *gorm.DB, app *fiber.App, kilitSQL string, kilitArg any, n int, build func(i int) *http.Request) []int {
	t.Helper()

	kilit := gdb.Begin()
	if err := kilit.Exec(kilitSQL, kilitArg).Error; err != nil {
		kilit.Rollback()
		t.Fatalf("satır kilitlenemedi: %v", err)
	}

	done := make(chan int, n)
	for i := 0; i < n; i++ {
		req := build(i)
		go func() {
			res, err := app.Test(req, -1)
			if err != nil {
				done <- 0
				return
			}
			res.Body.Close()
			done <- res.StatusCode
		}()
	}

	var bekleyen int64
	for son := time.Now().Add(10 * time.Second); bekleyen < int64(n) && time.Now().Before(son); {
		time.Sleep(10 * time.Millisecond)
		gdb.Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&bekleyen)
	}
	if err := kilit.Commit().Error; err != nil {
		t.Errorf("satır kilidi bırakılamadı: %v", err)
	}
	if bekleyen < int64(n) {
		t.Errorf("yalnızca %d/%d istek kilitli satırda bekledi; yarış sıralaması kurulamadı", bekleyen, n)
	}

	statuses := make([]int, 0, n)
	for i := 0; i < n; i++ {
		statuses = append(statuses, <-done)
	}
	return statuses
}

// post, gövdesiz bir POST atar.
func post(t *testing.T, app *fiber.App, path string) (int, map[string]any) {
	t.Helper()
	return send(t, app, httptest.NewRequest(http.MethodPost, path, nil))
}

func TestGrantNoFillPass_GunlukSiniriAsmaz(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	// Sınır kadar istek başarılı olmalı.
	for i := 1; i <= services.MaxNoFillPassesPerDay; i++ {
		status, body := post(t, app, "/no-fill-pass")
		if status != fiber.StatusOK {
			t.Fatalf("%d. geçiş: status = %d, beklenen 200 (body: %v)", i, status, body)
		}
		if got := int(body["noFillPassesToday"].(float64)); got != i {
			t.Errorf("%d. geçiş: noFillPassesToday = %d, beklenen %d", i, got, i)
		}
		wantRemaining := services.MaxNoFillPassesPerDay - i
		if got := int(body["noFillPassesRemaining"].(float64)); got != wantRemaining {
			t.Errorf("%d. geçiş: kalan = %d, beklenen %d", i, got, wantRemaining)
		}
	}

	// Sınırın üstündeki istek reddedilmeli.
	status, body := post(t, app, "/no-fill-pass")
	if status != fiber.StatusTooManyRequests {
		t.Fatalf("sınır üstü: status = %d, beklenen 429 (body: %v)", status, body)
	}
	if body["code"] != "no_fill_limit_reached" {
		t.Errorf("sınır üstü: code = %v, beklenen no_fill_limit_reached", body["code"])
	}

	// Reddedilen istek sayacı artırmamalı.
	var after models.User
	gdb.First(&after, "id = ?", u.ID)
	if after.NoFillPassesToday != services.MaxNoFillPassesPerDay {
		t.Errorf("reddedilen istek sayacı artırdı: %d, beklenen %d",
			after.NoFillPassesToday, services.MaxNoFillPassesPerDay)
	}
}

func TestGrantNoFillPass_GunDonumundeSifirlanir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	// Sınırı doldur.
	for i := 0; i < services.MaxNoFillPassesPerDay; i++ {
		if status, body := post(t, app, "/no-fill-pass"); status != fiber.StatusOK {
			t.Fatalf("hazırlık isteği başarısız: %d (%v)", status, body)
		}
	}
	if status, _ := post(t, app, "/no-fill-pass"); status != fiber.StatusTooManyRequests {
		t.Fatalf("sınır dolmadı, status = %d", status)
	}

	// Sayacı düne ait göster: gün dönümü sıfırlamayı tetiklemeli.
	dun := time.Now().AddDate(0, 0, -1).UnixMilli()
	gdb.Model(&models.User{}).Where("id = ?", u.ID).
		Update("last_reset_date", dun)

	status, body := post(t, app, "/no-fill-pass")
	if status != fiber.StatusOK {
		t.Fatalf("gün dönümü sonrası status = %d, beklenen 200 (body: %v)", status, body)
	}
	if got := int(body["noFillPassesToday"].(float64)); got != 1 {
		t.Errorf("gün dönümü sonrası sayaç = %d, beklenen 1 (sıfırlanıp artmalı)", got)
	}
}

func TestGrantNoFillPass_EszamanliIstekleriSiniriAsmaz(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	// Sınırın çok üstünde eşzamanlı istek: koşullu UPDATE yarışı engellemeli.
	const n = 10
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go func() {
			res, err := app.Test(httptest.NewRequest(http.MethodPost, "/no-fill-pass", nil), -1)
			if err != nil {
				done <- 0
				return
			}
			res.Body.Close()
			done <- res.StatusCode
		}()
	}

	basarili := 0
	for i := 0; i < n; i++ {
		if <-done == fiber.StatusOK {
			basarili++
		}
	}

	if basarili > services.MaxNoFillPassesPerDay {
		t.Errorf("%d istek başarılı oldu, sınır %d — koşullu UPDATE yarışı engellemiyor",
			basarili, services.MaxNoFillPassesPerDay)
	}

	var after models.User
	gdb.First(&after, "id = ?", u.ID)
	if after.NoFillPassesToday > services.MaxNoFillPassesPerDay {
		t.Errorf("sayaç sınırı aştı: %d > %d", after.NoFillPassesToday, services.MaxNoFillPassesPerDay)
	}
}

// --- Günlük kullanım yanıtı ---

func TestGetDailyUsage_YeniKullanicidaYeniAlanlarHerZamanBulunur(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	body := getUsage(t, app)

	// 0 ve boş küme de gönderilmeli: alanın yokluğu eski sunucu anlamına gelir.
	if got := intField(t, body, "reviewAnswersToday"); got != 0 {
		t.Errorf("reviewAnswersToday = %d, beklenen 0", got)
	}
	if got := unlockedGroups(t, body); len(got) != 0 {
		t.Errorf("unlockedGroupsToday = %v, beklenen boş dizi", got)
	}
	if got := intField(t, body, "noFillPassesRemaining"); got != services.MaxNoFillPassesPerDay {
		t.Errorf("noFillPassesRemaining = %d, beklenen %d", got, services.MaxNoFillPassesPerDay)
	}
}

func TestGetDailyUsage_GunDonumundeTumSayaclariSifirlar(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)
	dunkuKullanim(t, gdb, u.ID)

	body := getUsage(t, app)

	for _, key := range []string{
		"groupsStartedTodayCount", "groupsUnlockedByAdToday",
		"reviewAdPassCountToday", "noFillPassesToday", "reviewAnswersToday",
	} {
		if got := intField(t, body, key); got != 0 {
			t.Errorf("gün dönümü sonrası %s = %d, beklenen 0", key, got)
		}
	}
	if got := unlockedGroups(t, body); len(got) != 0 {
		t.Errorf("gün dönümü sonrası unlockedGroupsToday = %v, beklenen boş", got)
	}
	if services.NeedsReset(loadUser(t, gdb, u.ID).LastResetDate) {
		t.Error("sıfırlama last_reset_date'i bugüne çekmedi")
	}
}

// --- Swipe: günlük tekrar sayacı ---

func TestSwipe_TekrarCevabiGunlukSayaciArtirir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)
	testWord(t, gdb, u.ID, "kutu2", 2)
	testWord(t, gdb, u.ID, "kutu3", 3)

	if status, body := send(t, app, swipeRequest("kutu2", "right")); status != fiber.StatusOK {
		t.Fatalf("sağ swipe status = %d (body: %v)", status, body)
	}
	if got := intField(t, getUsage(t, app), "reviewAnswersToday"); got != 1 {
		t.Errorf("kutu 2 sağ swipe sonrası reviewAnswersToday = %d, beklenen 1", got)
	}

	// Yanlış cevap da bir tekrar cevabıdır.
	if status, body := send(t, app, swipeRequest("kutu3", "left")); status != fiber.StatusOK {
		t.Fatalf("sol swipe status = %d (body: %v)", status, body)
	}
	if got := intField(t, getUsage(t, app), "reviewAnswersToday"); got != 2 {
		t.Errorf("kutu 3 sol swipe sonrası reviewAnswersToday = %d, beklenen 2", got)
	}
}

func TestSwipe_OgrenmeSwipeiTekrarSayilmaz(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)
	testWord(t, gdb, u.ID, "yeni", 1)

	// Kutu 1 → 2: swipe sonrası kutu tekrar aralığındadır ama cevap öğrenme
	// ekranından gelmiştir.
	status, body := send(t, app, swipeRequest("yeni", "right"))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	if got := intField(t, body, "box"); got != 2 {
		t.Fatalf("swipe sonrası box = %d, beklenen 2", got)
	}
	if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 0 {
		t.Errorf("öğrenme swipe'ı sayıldı: review_answers_today = %d, beklenen 0", got)
	}
}

func TestSwipe_SayilmayanDurumlardaSayacDegismez(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)
	testWord(t, gdb, u.ID, "ogrenilmis", 6)
	testWord(t, gdb, u.ID, "kutu2", 2)

	cases := []struct {
		name       string
		req        *http.Request
		wantStatus int
	}{
		{"ogrenilmis kelime (kutu 6)", swipeRequest("ogrenilmis", "right"), fiber.StatusOK},
		{"gecersiz direction", swipeRequest("kutu2", "up"), fiber.StatusBadRequest},
		{"olmayan kelime", swipeRequest("yok", "right"), fiber.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := send(t, app, c.req)
			if status != c.wantStatus {
				t.Fatalf("status = %d, beklenen %d (body: %v)", status, c.wantStatus, body)
			}
			if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != 0 {
				t.Errorf("review_answers_today = %d, beklenen 0", got)
			}
		})
	}

	// Reddedilen swipe kelimeyi de ilerletmemiş olmalı.
	var wp models.WordProgress
	gdb.First(&wp, "user_id = ? AND word_id = ?", u.ID, "kutu2")
	if wp.Box != 2 || wp.WrongCount != 0 {
		t.Errorf("geçersiz direction kelimeyi değiştirdi: box = %d, wrongCount = %d", wp.Box, wp.WrongCount)
	}
}

func TestSwipe_GunDonumundeSifirlayipSayar(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)
	testWord(t, gdb, u.ID, "kutu2", 2)
	dunkuKullanim(t, gdb, u.ID)

	if status, body := send(t, app, swipeRequest("kutu2", "right")); status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}

	// last_reset_date bütün sayaçlarda ortaktır: swipe yalnızca kendi sayacını
	// sıfırlayıp tarihi bugüne çekseydi diğerleri o gün hiç sıfırlanmazdı.
	after := loadUser(t, gdb, u.ID)
	if after.ReviewAnswersToday != 1 {
		t.Errorf("review_answers_today = %d, beklenen 1 (sıfırlanıp artmalı)", after.ReviewAnswersToday)
	}
	if after.GroupsStartedTodayCount != 0 || after.GroupsUnlockedByAdToday != 0 ||
		after.ReviewAdPassCountToday != 0 || after.NoFillPassesToday != 0 {
		t.Errorf("diğer sayaçlar sıfırlanmadı: started=%d byAd=%d reviewPass=%d noFill=%d",
			after.GroupsStartedTodayCount, after.GroupsUnlockedByAdToday,
			after.ReviewAdPassCountToday, after.NoFillPassesToday)
	}
	if after.UnlockedGroupsToday != "" {
		t.Errorf("unlocked_groups_today = %q, beklenen boş", after.UnlockedGroupsToday)
	}
}

func TestSwipe_EszamanliCevaplarKayipsizSayilir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	const n = 20
	for i := 0; i < n; i++ {
		testWord(t, gdb, u.ID, fmt.Sprintf("kelime-%d", i), 2)
	}

	statuses := kilitAltindaEszamanli(t, gdb, app, u.ID, n, func(i int) *http.Request {
		return swipeRequest(fmt.Sprintf("kelime-%d", i), "right")
	})
	for _, status := range statuses {
		if status != fiber.StatusOK {
			t.Fatalf("eşzamanlı swipe status = %d, beklenen 200 (tümü: %v)", status, statuses)
		}
	}

	if got := loadUser(t, gdb, u.ID).ReviewAnswersToday; got != n {
		t.Errorf("review_answers_today = %d, beklenen %d (artırım atomik değil)", got, n)
	}
}

func TestSwipe_GunDonumundeEszamanliIsteklerArtirimKaybetmez(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	const swipes, reads = 10, 5
	for i := 0; i < swipes; i++ {
		testWord(t, gdb, u.ID, fmt.Sprintf("kelime-%d", i), 2)
	}
	dunkuKullanim(t, gdb, u.ID)

	// Hepsi bayat last_reset_date ile başlar; sıfırlama koşulsuz olsaydı geç
	// kalan bir sıfırlama öndeki artırımları silerdi.
	statuses := kilitAltindaEszamanli(t, gdb, app, u.ID, swipes+reads, func(i int) *http.Request {
		if i < swipes {
			return swipeRequest(fmt.Sprintf("kelime-%d", i), "right")
		}
		return httptest.NewRequest(http.MethodGet, "/usage", nil)
	})
	for _, status := range statuses {
		if status != fiber.StatusOK {
			t.Fatalf("eşzamanlı istek status = %d, beklenen 200 (tümü: %v)", status, statuses)
		}
	}

	after := loadUser(t, gdb, u.ID)
	if after.ReviewAnswersToday != swipes {
		t.Errorf("review_answers_today = %d, beklenen %d (kayıp artırım)", after.ReviewAnswersToday, swipes)
	}
	if after.GroupsStartedTodayCount != 0 || after.ReviewAdPassCountToday != 0 || after.UnlockedGroupsToday != "" {
		t.Errorf("dünkü sayaçlar sıfırlanmadı: started=%d reviewPass=%d küme=%q",
			after.GroupsStartedTodayCount, after.ReviewAdPassCountToday, after.UnlockedGroupsToday)
	}
}

func TestSwipe_YanitDuzWordProgressOlarakKalir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)
	testWord(t, gdb, u.ID, "kutu2", 2)

	status, body := send(t, app, swipeRequest("kutu2", "right"))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}

	// Mağazadaki istemciler yanıtı doğrudan UserWord olarak çözer; sayaç ya da
	// sarmalayıcı bir alan eklemek onları bozar.
	if body["wordId"] != "kutu2" {
		t.Errorf("wordId = %v, beklenen kutu2 (üst düzeyde olmalı)", body["wordId"])
	}
	if got := intField(t, body, "box"); got != 3 {
		t.Errorf("box = %d, beklenen 3", got)
	}
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{
		"box", "english", "group", "lastReviewDate", "learned",
		"level", "nextReviewDate", "turkish", "wordId", "wrongCount",
	}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("yanıt alanları = %v, beklenen %v", keys, want)
	}
}

// --- Sayaç artırımı ---

func TestIncrementUsage_GunDonumundeSifirlayipArtirir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)
	dunkuKullanim(t, gdb, u.ID)

	status, body := post(t, app, "/review-ad-pass")
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	if got := intField(t, body, "reviewAdPassCountToday"); got != 1 {
		t.Errorf("reviewAdPassCountToday = %d, beklenen 1 (sıfırlanıp artmalı)", got)
	}
	if got := intField(t, body, "reviewAnswersToday"); got != 0 {
		t.Errorf("reviewAnswersToday = %d, beklenen 0", got)
	}
}

func TestIncrementUsage_YazilamayanArtirimBasariDonmez(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)

	// Olmayan kolon UPDATE'i düşürür: istemci 200 görürse kaydedilmemiş bir
	// geçişi kaydedilmiş sayıp içeriği açar.
	app := testApp(h, u.ID)
	app.Post("/bozuk", func(c *fiber.Ctx) error {
		c.Locals("userID", u.ID)
		return h.incrementUsage(c, "olmayan_kolon")
	})

	status, body := post(t, app, "/bozuk")
	if status != fiber.StatusInternalServerError {
		t.Fatalf("status = %d, beklenen 500 (body: %v)", status, body)
	}
}

// --- Tekrar reklam geçişi ---

func checkpointRequest(checkpoint any) *http.Request {
	return jsonRequest("/review-ad-pass", fiber.Map{"checkpoint": checkpoint})
}

func TestRecordReviewAdPass_AyniKapiBirKezSayilir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	steps := []struct {
		name       string
		checkpoint int
		want       int
	}{
		{"30 ilk kez", 30, 1},
		// Yanıtı kaybolan isteğin tekrarı: sayaç 2 olsaydı istemci 60 kapısını atlardı.
		{"30 yeniden", 30, 1},
		{"60", 60, 2},
		{"60 yeniden", 60, 2},
		{"gecilmis kapi sayaci dusurmez", 30, 2},
		{"90", 90, 3},
	}
	for _, s := range steps {
		status, body := send(t, app, checkpointRequest(s.checkpoint))
		if status != fiber.StatusOK {
			t.Fatalf("%s: status = %d (body: %v)", s.name, status, body)
		}
		if got := intField(t, body, "reviewAdPassCountToday"); got != s.want {
			t.Fatalf("%s: reviewAdPassCountToday = %d, beklenen %d", s.name, got, s.want)
		}
	}

	if got := loadUser(t, gdb, u.ID).ReviewAdPassCountToday; got != 3 {
		t.Errorf("review_ad_pass_count_today = %d, beklenen 3", got)
	}
}

func TestRecordReviewAdPass_KapiAtlanirsaSayacKapininSirasinaCekilir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	// 30 kapısının kaydı hiç ulaşmadıysa 60 kapısı sayacı yine 2'ye getirir;
	// istemci sıradaki kapıyı (90) bu sayaçtan hesaplar.
	_, body := send(t, app, checkpointRequest(60))
	if got := intField(t, body, "reviewAdPassCountToday"); got != 2 {
		t.Errorf("reviewAdPassCountToday = %d, beklenen 2", got)
	}
}

func TestRecordReviewAdPass_GovdesizVeGecersizGovdeSayaciArtirir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	cases := []struct {
		name string
		req  *http.Request
	}{
		{"govdesiz", httptest.NewRequest(http.MethodPost, "/review-ad-pass", nil)},
		{"bos nesne", jsonRequest("/review-ad-pass", fiber.Map{})},
		{"kapi olmayan deger", checkpointRequest(45)},
		{"sifir", checkpointRequest(0)},
		{"negatif", checkpointRequest(-30)},
		{"son kapidan buyuk", checkpointRequest(120)},
		{"yanlis tipte deger", checkpointRequest("30")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Beklenti önceki değere göredir; alt testler tek başına da koşabilsin.
			once := loadUser(t, gdb, u.ID).ReviewAdPassCountToday

			status, body := send(t, app, c.req)
			if status != fiber.StatusOK {
				t.Fatalf("status = %d (body: %v)", status, body)
			}
			if got := intField(t, body, "reviewAdPassCountToday"); got != once+1 {
				t.Errorf("reviewAdPassCountToday = %d, beklenen %d", got, once+1)
			}
		})
	}
}

func TestRecordReviewAdPass_EszamanliAyniKapiBirKezSayilir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	const n = 10
	statuses := kilitAltindaEszamanli(t, gdb, app, u.ID, n, func(int) *http.Request {
		return checkpointRequest(30)
	})
	for _, status := range statuses {
		if status != fiber.StatusOK {
			t.Fatalf("eşzamanlı istek status = %d, beklenen 200 (tümü: %v)", status, statuses)
		}
	}

	if got := loadUser(t, gdb, u.ID).ReviewAdPassCountToday; got != 1 {
		t.Errorf("review_ad_pass_count_today = %d, beklenen 1", got)
	}
}

func TestRecordReviewAdPass_GunDonumundeSifirlayipAyarlar(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)
	dunkuKullanim(t, gdb, u.ID)

	// Dünkü sayaç (5) bugünkü ilk kapıyı "zaten geçilmiş" göstermemeli.
	status, body := send(t, app, checkpointRequest(30))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	if got := intField(t, body, "reviewAdPassCountToday"); got != 1 {
		t.Errorf("reviewAdPassCountToday = %d, beklenen 1 (sıfırlanıp ayarlanmalı)", got)
	}
	if got := intField(t, body, "groupsStartedTodayCount"); got != 0 {
		t.Errorf("groupsStartedTodayCount = %d, beklenen 0", got)
	}
}

// --- last_reset_date ---

func TestKullanimArtirimlari_LastResetDateGeriGitmez(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)
	testWord(t, gdb, u.ID, "kutu2", 2)

	a13 := fiber.Map{"level": "A1", "group": 3}
	a14 := fiber.Map{"level": "A1", "group": 4}
	cases := []struct {
		name string
		req  *http.Request
		// İsteğin gerçekten yazdığını gösteren sayaç: tarih korunurken artırım
		// kaybolmamalı.
		counter func(models.User) int
	}{
		{"review-ad-pass govdesiz", httptest.NewRequest(http.MethodPost, "/review-ad-pass", nil),
			func(u models.User) int { return u.ReviewAdPassCountToday }},
		{"review-ad-pass checkpoint", checkpointRequest(90),
			func(u models.User) int { return u.ReviewAdPassCountToday }},
		{"group-started govdesiz", httptest.NewRequest(http.MethodPost, "/group-started", nil),
			func(u models.User) int { return u.GroupsStartedTodayCount }},
		{"group-started govdeli", jsonRequest("/group-started", a13),
			func(u models.User) int { return u.GroupsStartedTodayCount }},
		{"group-unlocked-by-ad govdeli", jsonRequest("/group-unlocked-by-ad", a14),
			func(u models.User) int { return u.GroupsUnlockedByAdToday }},
		{"no-fill-pass", httptest.NewRequest(http.MethodPost, "/no-fill-pass", nil),
			func(u models.User) int { return u.NoFillPassesToday }},
		{"swipe", swipeRequest("kutu2", "right"),
			func(u models.User) int { return u.ReviewAnswersToday }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Daha geç bir "now" ile çalışan başka bir istek tarihi çoktan ileri
			// almış: bu isteğin kendi "now"ı ondan eskidir (gece yarısı yarışı).
			ileri := time.Now().Add(time.Minute).UnixMilli()
			if err := gdb.Model(&models.User{}).Where("id = ?", u.ID).
				Update("last_reset_date", ileri).Error; err != nil {
				t.Fatalf("last_reset_date ayarlanamadı: %v", err)
			}
			once := c.counter(loadUser(t, gdb, u.ID))

			status, body := send(t, app, c.req)
			if status != fiber.StatusOK {
				t.Fatalf("status = %d (body: %v)", status, body)
			}

			after := loadUser(t, gdb, u.ID)
			if after.LastResetDate != ileri {
				t.Errorf("last_reset_date = %d, beklenen %d (eski \"now\" tarihi geri çekmemeli)",
					after.LastResetDate, ileri)
			}
			if got := c.counter(after); got <= once {
				t.Errorf("sayaç = %d, önceki %d (artırım yazılmadı)", got, once)
			}
		})
	}
}

func TestKullanimArtirimlari_LastResetDateIleriAlinir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	// Bugüne ait ama eski bir tarih: sıfırlama tetiklenmez, artırım tarihi şimdiye çeker.
	bugunBasi := services.UsageDayStart(time.Now()).UnixMilli()
	gdb.Model(&models.User{}).Where("id = ?", u.ID).Updates(map[string]any{
		"review_ad_pass_count_today": 1,
		"last_reset_date":            bugunBasi,
	})
	once := time.Now().UnixMilli()

	status, body := send(t, app, checkpointRequest(60))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}

	after := loadUser(t, gdb, u.ID)
	if after.ReviewAdPassCountToday != 2 {
		t.Errorf("review_ad_pass_count_today = %d, beklenen 2 (aynı gün sıfırlanmamalı)", after.ReviewAdPassCountToday)
	}
	if after.LastResetDate < once {
		t.Errorf("last_reset_date = %d, beklenen >= %d", after.LastResetDate, once)
	}
}

// --- Grup açma ---

func TestUnlockGroup_AyniGrupGundeBirKezSayilir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	a13 := fiber.Map{"level": "A1", "group": 3}

	status, body := postJSON(t, app, "/group-started", a13)
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	if got := intField(t, body, "groupsStartedTodayCount"); got != 1 {
		t.Errorf("ilk açılış: groupsStartedTodayCount = %d, beklenen 1", got)
	}
	if got := unlockedGroups(t, body); !reflect.DeepEqual(got, []string{"A1:3"}) {
		t.Errorf("ilk açılış: unlockedGroupsToday = %v, beklenen [A1:3]", got)
	}

	// Aynı istek yeniden: yanıtı kaybolan isteğin tekrarı hakkı ikinci kez yememeli.
	_, body = postJSON(t, app, "/group-started", a13)
	if got := intField(t, body, "groupsStartedTodayCount"); got != 1 {
		t.Errorf("tekrar: groupsStartedTodayCount = %d, beklenen 1 (idempotent)", got)
	}
	if got := unlockedGroups(t, body); !reflect.DeepEqual(got, []string{"A1:3"}) {
		t.Errorf("tekrar: unlockedGroupsToday = %v, beklenen [A1:3]", got)
	}

	// Farklı grup, reklamla.
	_, body = postJSON(t, app, "/group-unlocked-by-ad", fiber.Map{"level": "A1", "group": 4})
	if got := intField(t, body, "groupsUnlockedByAdToday"); got != 1 {
		t.Errorf("reklamla açılış: groupsUnlockedByAdToday = %d, beklenen 1", got)
	}
	if got := intField(t, body, "groupsStartedTodayCount"); got != 1 {
		t.Errorf("reklamla açılış: groupsStartedTodayCount = %d, beklenen 1 (değişmemeli)", got)
	}
	if got := unlockedGroups(t, body); !reflect.DeepEqual(got, []string{"A1:3", "A1:4"}) {
		t.Errorf("reklamla açılış: unlockedGroupsToday = %v, beklenen [A1:3 A1:4]", got)
	}

	// Küme iki uçta ortaktır: zaten açık grup diğer uçtan da yeniden sayılmaz.
	_, body = postJSON(t, app, "/group-unlocked-by-ad", a13)
	if got := intField(t, body, "groupsUnlockedByAdToday"); got != 1 {
		t.Errorf("açık grup diğer uçtan: groupsUnlockedByAdToday = %d, beklenen 1", got)
	}
	if got := unlockedGroups(t, body); len(got) != 2 {
		t.Errorf("açık grup diğer uçtan: unlockedGroupsToday = %v, beklenen 2 eleman", got)
	}

	// "A1:3" kümedeyken "A1:30" ayrı bir gruptur (alt dize eşleşmesi sayılmaz).
	_, body = postJSON(t, app, "/group-started", fiber.Map{"level": "A1", "group": 30})
	if got := intField(t, body, "groupsStartedTodayCount"); got != 2 {
		t.Errorf("A1:30: groupsStartedTodayCount = %d, beklenen 2", got)
	}
}

func TestUnlockGroup_EszamanliAyniAnahtarBirKezSayilir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	const n = 10
	statuses := kilitAltindaEszamanli(t, gdb, app, u.ID, n, func(int) *http.Request {
		return jsonRequest("/group-started", fiber.Map{"level": "B1", "group": 7})
	})
	for _, status := range statuses {
		if status != fiber.StatusOK {
			t.Fatalf("eşzamanlı istek status = %d, beklenen 200 (tümü: %v)", status, statuses)
		}
	}

	after := loadUser(t, gdb, u.ID)
	if after.GroupsStartedTodayCount != 1 {
		t.Errorf("groups_started_today_count = %d, beklenen 1 — koşullu UPDATE yarışı engellemiyor",
			after.GroupsStartedTodayCount)
	}
	if after.UnlockedGroupsToday != "B1:7" {
		t.Errorf("unlocked_groups_today = %q, beklenen \"B1:7\"", after.UnlockedGroupsToday)
	}
}

func TestUnlockGroup_HazirlananSorguylaDaCalisir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)

	// db.Connect bağlantıyı basit protokolle (değerler sorgu metnine gömülü)
	// açar. Sayaç sorguları bu ayara bağlı kalmamalı: burada bağlantı,
	// parametreleri ayrı gönderen varsayılan protokolle açılır.
	hazirlayan, err := gorm.Open(postgres.Open(os.Getenv("TEST_DATABASE_URL")), &gorm.Config{})
	if err != nil {
		t.Fatalf("veritabanına bağlanılamadı: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := hazirlayan.DB(); err == nil {
			sqlDB.Close()
		}
	})
	app := testApp(New(hazirlayan, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	a13 := fiber.Map{"level": "A1", "group": 3}
	for i := 1; i <= 2; i++ {
		status, body := postJSON(t, app, "/group-started", a13)
		if status != fiber.StatusOK {
			t.Fatalf("%d. istek: status = %d (body: %v)", i, status, body)
		}
		if got := intField(t, body, "groupsStartedTodayCount"); got != 1 {
			t.Errorf("%d. istek: groupsStartedTodayCount = %d, beklenen 1", i, got)
		}
		if got := unlockedGroups(t, body); !reflect.DeepEqual(got, []string{"A1:3"}) {
			t.Errorf("%d. istek: unlockedGroupsToday = %v, beklenen [A1:3]", i, got)
		}
	}

	// Diğer sayaç uçları da aynı bağlantıda çalışmalı.
	if status, body := send(t, app, checkpointRequest(30)); status != fiber.StatusOK {
		t.Errorf("review-ad-pass: status = %d (body: %v)", status, body)
	}
	if status, body := post(t, app, "/no-fill-pass"); status != fiber.StatusOK {
		t.Errorf("no-fill-pass: status = %d (body: %v)", status, body)
	}
}

func TestUnlockGroup_GovdesizIstekYalnizcaSayaciArtirir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	// Gövde göndermeyen eski istemciler: her istek sayılır, küme değişmez.
	for i := 1; i <= 2; i++ {
		status, body := post(t, app, "/group-started")
		if status != fiber.StatusOK {
			t.Fatalf("%d. istek: status = %d (body: %v)", i, status, body)
		}
		if got := intField(t, body, "groupsStartedTodayCount"); got != i {
			t.Errorf("%d. istek: groupsStartedTodayCount = %d, beklenen %d", i, got, i)
		}
		if got := unlockedGroups(t, body); len(got) != 0 {
			t.Errorf("%d. istek: unlockedGroupsToday = %v, beklenen boş", i, got)
		}
	}
}

func TestUnlockGroup_GecersizGovdeYalnizcaSayaciArtirir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	cases := []struct {
		name    string
		payload any
	}{
		{"bilinmeyen seviye", fiber.Map{"level": "X9", "group": 3}},
		{"grup sifir", fiber.Map{"level": "A1", "group": 0}},
		{"ust sinirin ustunde grup", fiber.Map{"level": "A1", "group": services.MaxUnlockGroupNumber + 1}},
		{"bos nesne", fiber.Map{}},
		{"yanlis tipte grup", fiber.Map{"level": "A1", "group": "3"}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := postJSON(t, app, "/group-unlocked-by-ad", c.payload)
			if status != fiber.StatusOK {
				t.Fatalf("status = %d (body: %v)", status, body)
			}
			if got := intField(t, body, "groupsUnlockedByAdToday"); got != i+1 {
				t.Errorf("groupsUnlockedByAdToday = %d, beklenen %d", got, i+1)
			}
			if got := unlockedGroups(t, body); len(got) != 0 {
				t.Errorf("unlockedGroupsToday = %v, beklenen boş (geçersiz anahtar kümeye yazılmamalı)", got)
			}
		})
	}
}

func TestUnlockGroup_GunDonumundeKumeBosalir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := testApp(h, u.ID)

	a13 := fiber.Map{"level": "A1", "group": 3}
	if status, body := postJSON(t, app, "/group-started", a13); status != fiber.StatusOK {
		t.Fatalf("hazırlık isteği başarısız: %d (%v)", status, body)
	}

	// Kümeyi düne ait göster.
	dun := time.Now().AddDate(0, 0, -1).UnixMilli()
	gdb.Model(&models.User{}).Where("id = ?", u.ID).Update("last_reset_date", dun)

	body := getUsage(t, app)
	if got := unlockedGroups(t, body); len(got) != 0 {
		t.Errorf("gün dönümü sonrası unlockedGroupsToday = %v, beklenen boş", got)
	}
	if got := intField(t, body, "groupsStartedTodayCount"); got != 0 {
		t.Errorf("gün dönümü sonrası groupsStartedTodayCount = %d, beklenen 0", got)
	}

	// Dün açılan grup bugün yeniden açılırsa yeniden sayılır.
	gdb.Model(&models.User{}).Where("id = ?", u.ID).Updates(map[string]any{
		"unlocked_groups_today": "A1:3",
		"last_reset_date":       dun,
	})
	_, body = postJSON(t, app, "/group-started", a13)
	if got := intField(t, body, "groupsStartedTodayCount"); got != 1 {
		t.Errorf("yeni günde aynı grup: groupsStartedTodayCount = %d, beklenen 1", got)
	}
	if got := unlockedGroups(t, body); !reflect.DeepEqual(got, []string{"A1:3"}) {
		t.Errorf("yeni günde aynı grup: unlockedGroupsToday = %v, beklenen [A1:3]", got)
	}
}

// testApp rota → kolon eşlemesini elle kurar; bu test gerçek RegisterRoutes
// tablosunun grup uçlarını idempotent handler'a bağladığını doğrular.
func TestRegisterRoutes_GrupAcmaUclariIdempotent(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	tokens := auth.NewTokenService("test-secret", time.Minute, time.Hour)
	h := New(gdb, &config.Config{AppEnv: "test"}, tokens, nil)

	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	RegisterRoutes(app.Group("/api/v1"), h, tokens, func(c *fiber.Ctx) error { return c.Next() })

	token, err := tokens.GenerateAccessToken(u.ID, u.Email)
	if err != nil {
		t.Fatalf("token üretilemedi: %v", err)
	}
	call := func(path string) map[string]any {
		t.Helper()
		req := jsonRequest("/api/v1/users/me/usage/"+path, fiber.Map{"level": "A2", "group": 5})
		req.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
		status, body := send(t, app, req)
		if status != fiber.StatusOK {
			t.Fatalf("%s: status = %d (body: %v)", path, status, body)
		}
		return body
	}

	call("group-started")
	body := call("group-started")
	if got := intField(t, body, "groupsStartedTodayCount"); got != 1 {
		t.Errorf("group-started iki kez: groupsStartedTodayCount = %d, beklenen 1", got)
	}
	body = call("group-unlocked-by-ad")
	if got := intField(t, body, "groupsUnlockedByAdToday"); got != 0 {
		t.Errorf("açık grup için group-unlocked-by-ad: groupsUnlockedByAdToday = %d, beklenen 0", got)
	}

	// Bu gövdede checkpoint yok: uç kapıyı ayırt edemez, her istek sayılmalı.
	call("review-ad-pass")
	body = call("review-ad-pass")
	if got := intField(t, body, "reviewAdPassCountToday"); got != 2 {
		t.Errorf("review-ad-pass iki kez: reviewAdPassCountToday = %d, beklenen 2", got)
	}

	// Aynı kapı (60) yeniden bildirilirse sayaç değişmez; sonraki kapı bir artırır.
	checkpoint := func(n int) map[string]any {
		t.Helper()
		req := jsonRequest("/api/v1/users/me/usage/review-ad-pass", fiber.Map{"checkpoint": n})
		req.Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
		status, body := send(t, app, req)
		if status != fiber.StatusOK {
			t.Fatalf("review-ad-pass checkpoint %d: status = %d (body: %v)", n, status, body)
		}
		return body
	}
	if got := intField(t, checkpoint(60), "reviewAdPassCountToday"); got != 2 {
		t.Errorf("checkpoint 60 (zaten geçilmiş): reviewAdPassCountToday = %d, beklenen 2", got)
	}
	if got := intField(t, checkpoint(90), "reviewAdPassCountToday"); got != 3 {
		t.Errorf("checkpoint 90: reviewAdPassCountToday = %d, beklenen 3", got)
	}
}
