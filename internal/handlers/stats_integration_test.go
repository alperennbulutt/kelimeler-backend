package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Bu dosya istatistik uçlarını (/stats/*) gerçek PostgreSQL'e karşı sınar.
// Çalıştırma talimatı ve TEST_DATABASE_URL için usage_integration_test.go'ya bakın.

// statsApp, istatistik uçlarını auth'suz (userID enjekte edilerek) kurar.
func statsApp(h *Handlers, userID string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	inject := func(c *fiber.Ctx) error {
		c.Locals("userID", userID)
		return c.Next()
	}
	app.Post("/stats/daily-login", inject, h.LogDailyLogin)
	app.Get("/stats/summary", inject, h.StatsSummary)
	app.Get("/stats/learned-words", inject, h.LearnedWords)
	app.Get("/stats/login-dates", inject, h.LoginDates)
	return app
}

func statsGet(t *testing.T, app *fiber.App, path string) (int, map[string]any) {
	t.Helper()
	return send(t, app, httptest.NewRequest(http.MethodGet, path, nil))
}

func TestStats_VeritabaniHatasi500Doner(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)

	// Ayrı bir havuz açıp kapatıyoruz: kapanınca her sorgu hata döner. Uçlar
	// bunu boş/sıfır veriyle 200 olarak gizlememeli (istemci sıfır seriyi
	// gerçek sanar). testUser'ın temizliği asıl havuzla çalışmaya devam eder.
	bozuk, err := db.Connect(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("veritabanına bağlanılamadı: %v", err)
	}
	sqlDB, err := bozuk.DB()
	if err != nil {
		t.Fatalf("sql.DB alınamadı: %v", err)
	}
	sqlDB.Close()

	h := New(bozuk, &config.Config{AppEnv: "test"}, nil, nil)
	app := statsApp(h, u.ID)

	cases := []struct {
		method, path string
	}{
		{http.MethodPost, "/stats/daily-login"},
		{http.MethodGet, "/stats/summary"},
		{http.MethodGet, "/stats/learned-words"},
		{http.MethodGet, "/stats/login-dates"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			status, body := send(t, app, httptest.NewRequest(c.method, c.path, nil))
			if status != fiber.StatusInternalServerError {
				t.Fatalf("status = %d, beklenen 500 (body: %v)", status, body)
			}
			if body["code"] != "internal" {
				t.Errorf("code = %v, beklenen \"internal\"", body["code"])
			}
		})
	}
}

// sabitSaat, istatistik uçlarının saatini verilen ana sabitler ve kullanım
// konumunu Europe/Istanbul'a çeker; test bitince ikisini de geri alır.
func sabitSaat(t *testing.T, at time.Time) {
	t.Helper()

	oncekiSaat, oncekiKonum := statsNow, services.UsageLocation()
	if err := services.SetUsageLocation("Europe/Istanbul"); err != nil {
		t.Fatalf("kullanım konumu ayarlanamadı: %v", err)
	}
	statsNow = func() time.Time { return at }
	t.Cleanup(func() {
		statsNow = oncekiSaat
		_ = services.SetUsageLocation(oncekiKonum.String())
	})
}

func utcAn(t *testing.T, s string) time.Time {
	t.Helper()

	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("zaman çözülemedi %q: %v", s, err)
	}
	return v
}

// girisYap, saati at'e çekip POST /stats/daily-login atar ve dönen seriyi verir.
func girisYap(t *testing.T, app *fiber.App, at time.Time) int {
	t.Helper()

	sabitSaat(t, at)
	status, body := post(t, app, "/stats/daily-login")
	if status != fiber.StatusOK {
		t.Fatalf("POST /stats/daily-login (%s) status = %d (body: %v)", at.Format(time.RFC3339), status, body)
	}
	return intField(t, body, "currentStreak")
}

// kayitliGunler, kullanıcının daily_logins satırlarındaki günleri metin olarak okur.
func kayitliGunler(t *testing.T, gdb *gorm.DB, userID string) []string {
	t.Helper()

	var gunler []string
	if err := gdb.Raw(`SELECT to_char(login_date, 'YYYY-MM-DD') FROM daily_logins
		WHERE user_id = ? ORDER BY login_date`, userID).Scan(&gunler).Error; err != nil {
		t.Fatalf("giriş günleri okunamadı: %v", err)
	}
	return gunler
}

// statsLoginDates, GET /stats/login-dates yanıtındaki tarihleri döner.
func statsLoginDates(t *testing.T, app *fiber.App) []string {
	t.Helper()

	status, body := statsGet(t, app, "/stats/login-dates")
	if status != fiber.StatusOK {
		t.Fatalf("GET /stats/login-dates status = %d (body: %v)", status, body)
	}
	raw, ok := body["dates"].([]any)
	if !ok {
		t.Fatalf("dates yok ya da dizi değil (body: %v)", body)
	}
	out := make([]string, 0, len(raw))
	for _, d := range raw {
		out = append(out, d.(string))
	}
	return out
}

func TestLogDailyLogin_GunTRGeceYarisindaDoner(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := statsApp(New(gdb, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	// TR 23 Ekim 23:30 (20:30Z), 24 Ekim 01:30 (22:30Z, UTC'de hâlâ 23'ü) ve
	// 25 Ekim 10:00. Kullanıcı her yerel gün girdi; seri 3 olmalı. UTC gün
	// sınırıyla ikinci giriş 23'üne düşüyor ve seri 25'inde 1'e iniyordu.
	if got := girisYap(t, app, utcAn(t, "2026-10-23T20:30:00Z")); got != 1 {
		t.Errorf("ilk giriş serisi = %d, beklenen 1", got)
	}
	if got := girisYap(t, app, utcAn(t, "2026-10-23T22:30:00Z")); got != 2 {
		t.Errorf("TR 01:30 girişi serisi = %d, beklenen 2", got)
	}
	if got := girisYap(t, app, utcAn(t, "2026-10-25T07:00:00Z")); got != 3 {
		t.Errorf("üçüncü gün serisi = %d, beklenen 3", got)
	}

	if got, want := kayitliGunler(t, gdb, u.ID), []string{"2026-10-23", "2026-10-24", "2026-10-25"}; !reflect.DeepEqual(got, want) {
		t.Errorf("kayıtlı günler = %v, beklenen %v", got, want)
	}
	if got, want := statsLoginDates(t, app), []string{"2026-10-25", "2026-10-24", "2026-10-23"}; !reflect.DeepEqual(got, want) {
		t.Errorf("login-dates = %v, beklenen %v", got, want)
	}
}

func TestLogDailyLogin_UTC2330AyniTRGunundeTekKayit(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := statsApp(New(gdb, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	// UTC 22 Ekim 23:30 = TR 23 Ekim 02:30; UTC 23 Ekim 09:00 = TR 23 Ekim 12:00.
	// İkisi de aynı kullanım günü: tek satır, seri 1.
	girisYap(t, app, utcAn(t, "2026-10-22T23:30:00Z"))
	if got := girisYap(t, app, utcAn(t, "2026-10-23T09:00:00Z")); got != 1 {
		t.Errorf("seri = %d, beklenen 1", got)
	}
	if got, want := kayitliGunler(t, gdb, u.ID), []string{"2026-10-23"}; !reflect.DeepEqual(got, want) {
		t.Errorf("kayıtlı günler = %v, beklenen %v", got, want)
	}
}

func TestLogDailyLogin_EskiUTCKayitlarlaSeriKirilmaz(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := statsApp(New(gdb, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	// Geçiş öncesi kayıtlar: kullanıcı her gece TR 01:00'de (UTC 22:00) giriyor,
	// eski kod bunları UTC gününe yazmış (TR 21 ve 22 Ekim girişleri 20 ve 21
	// olarak duruyor).
	for _, r := range []struct{ gun, an string }{
		{"2026-10-20", "2026-10-20T22:00:00Z"},
		{"2026-10-21", "2026-10-21T22:00:00Z"},
	} {
		if err := gdb.Exec(`INSERT INTO daily_logins (user_id, login_date, created_at)
			VALUES (?, CAST(? AS date), ?)`, u.ID, r.gun, utcAn(t, r.an)).Error; err != nil {
			t.Fatalf("eski kayıt yazılamadı: %v", err)
		}
	}

	// Geçişten sonraki ilk gece girişi TR 23 Ekim 01:00. TR 22 Ekim'in girişi
	// eski kayıtta 21 olarak durduğu için yalnızca login_date'e bakan hesap
	// 22'yi boş görüp seriyi 1'e düşürürdü.
	if got := girisYap(t, app, utcAn(t, "2026-10-22T22:00:00Z")); got != 4 {
		t.Errorf("geçiş sonrası seri = %d, beklenen 4 (20, 21, 22, 23 Ekim)", got)
	}
}

func TestStatsSummary_HaftalikVeriVeSeriTRGunuyle(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := statsApp(New(gdb, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	girisYap(t, app, utcAn(t, "2026-10-22T10:00:00Z")) // TR 22 Ekim 13:00
	girisYap(t, app, utcAn(t, "2026-10-22T23:30:00Z")) // TR 23 Ekim 02:30

	status, body := statsGet(t, app, "/stats/summary")
	if status != fiber.StatusOK {
		t.Fatalf("GET /stats/summary status = %d (body: %v)", status, body)
	}
	if got := intField(t, body, "currentStreak"); got != 2 {
		t.Errorf("currentStreak = %d, beklenen 2", got)
	}
	weekly, ok := body["weeklyLoginData"].(map[string]any)
	if !ok {
		t.Fatalf("weeklyLoginData yok (body: %v)", body)
	}
	if len(weekly) != 10 {
		t.Errorf("weeklyLoginData %d gün içeriyor, beklenen 10: %v", len(weekly), weekly)
	}
	for gun, want := range map[string]bool{"2026-10-23": true, "2026-10-22": true, "2026-10-21": false, "2026-10-14": false} {
		if got, ok := weekly[gun]; !ok || got != want {
			t.Errorf("weeklyLoginData[%s] = %v (var: %v), beklenen %v", gun, got, ok, want)
		}
	}
	if _, ok := weekly["2026-10-13"]; ok {
		t.Errorf("weeklyLoginData 11. günü (2026-10-13) içermemeli: %v", weekly)
	}
}

// yeniRozetler, özet yanıtındaki newlyEarnedBadges kimliklerini döner.
func yeniRozetler(t *testing.T, body map[string]any) []string {
	t.Helper()

	raw, ok := body["newlyEarnedBadges"].([]any)
	if !ok {
		t.Fatalf("newlyEarnedBadges yok ya da dizi değil (body: %v)", body)
	}
	ids := make([]string, 0, len(raw))
	for _, b := range raw {
		ids = append(ids, b.(map[string]any)["id"].(string))
	}
	return ids
}

// ozetTabloKilidiAltinda, earned_badges'i SHARE kipinde kilitleyip n özet
// isteği başlatır; hepsi rozet INSERT'ünde beklemeye geçince kilidi bırakır ve
// yanıt gövdelerini döner. SHARE kilidi okumayı engellemez, yazmayı engeller:
// böylece bütün istekler aynı "önceden kazanılan" kümesini görmüş olur.
func ozetTabloKilidiAltinda(t *testing.T, gdb *gorm.DB, app *fiber.App, n int) []map[string]any {
	t.Helper()

	kilit := gdb.Begin()
	if err := kilit.Exec("LOCK TABLE earned_badges IN SHARE MODE").Error; err != nil {
		kilit.Rollback()
		t.Fatalf("tablo kilitlenemedi: %v", err)
	}

	type sonuc struct {
		status int
		body   map[string]any
	}
	done := make(chan sonuc, n)
	for i := 0; i < n; i++ {
		go func() {
			res, err := app.Test(httptest.NewRequest(http.MethodGet, "/stats/summary", nil), -1)
			if err != nil {
				done <- sonuc{}
				return
			}
			defer res.Body.Close()
			var body map[string]any
			_ = json.NewDecoder(res.Body).Decode(&body)
			done <- sonuc{res.StatusCode, body}
		}()
	}

	var bekleyen int64
	for son := time.Now().Add(10 * time.Second); bekleyen < int64(n) && time.Now().Before(son); {
		time.Sleep(10 * time.Millisecond)
		gdb.Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&bekleyen)
	}
	if err := kilit.Commit().Error; err != nil {
		t.Errorf("tablo kilidi bırakılamadı: %v", err)
	}
	if bekleyen < int64(n) {
		t.Errorf("yalnızca %d/%d istek kilitte bekledi; yarış sıralaması kurulamadı", bekleyen, n)
	}

	bodies := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		r := <-done
		if r.status != fiber.StatusOK {
			t.Fatalf("GET /stats/summary status = %d (body: %v)", r.status, r.body)
		}
		bodies = append(bodies, r.body)
	}
	return bodies
}

func TestStatsSummary_YeniRozetEszamanliIsteklerdeBirKezDoner(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := statsApp(New(gdb, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	// first_day her kullanıcıda ilk özet çağrısında kazanılır.
	sayi := 0
	for _, body := range ozetTabloKilidiAltinda(t, gdb, app, 3) {
		for _, id := range yeniRozetler(t, body) {
			if id == "first_day" {
				sayi++
			}
		}
	}
	if sayi != 1 {
		t.Errorf("first_day %d yanıtta yeni döndü, beklenen 1", sayi)
	}

	var satir int64
	gdb.Raw("SELECT count(*) FROM earned_badges WHERE user_id = ? AND badge_id = 'first_day'", u.ID).Scan(&satir)
	if satir != 1 {
		t.Errorf("first_day için %d satır var, beklenen 1", satir)
	}

	// Sonraki çağrı artık yeni rozet döndürmez.
	status, body := statsGet(t, app, "/stats/summary")
	if status != fiber.StatusOK {
		t.Fatalf("GET /stats/summary status = %d (body: %v)", status, body)
	}
	if ids := yeniRozetler(t, body); len(ids) != 0 {
		t.Errorf("ikinci çağrıda yeni rozet döndü: %v", ids)
	}
}

// testSozlukKelimeleri, sözlüğe verilen seviyede n kelime ekler, seviye
// toplamları önbelleğini boşaltır ve test bitince kelimeleri siler. Sözlük
// paylaşılan bir tablodur: testler mevcut içeriğe değil, eklediklerine ve
// veritabanındaki gerçek sayılara bakar.
func testSozlukKelimeleri(t *testing.T, gdb *gorm.DB, level string, n int) []string {
	t.Helper()

	onek := "itest_bb_" + time.Now().Format("150405.000000000") + "_"
	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("%s%d", onek, i)
		w := &models.DictionaryWord{ID: id, English: "word" + id, Turkish: "kelime", Level: level, Grp: 1, WordOrder: 1000 + i}
		if err := gdb.Create(w).Error; err != nil {
			t.Fatalf("sözlük kelimesi eklenemedi: %v", err)
		}
		ids = append(ids, id)
	}
	services.ResetLevelTotalsCache()
	t.Cleanup(func() {
		gdb.Where("id IN ?", ids).Delete(&models.DictionaryWord{})
		services.ResetLevelTotalsCache()
	})
	return ids
}

// sozlukSayilari, veritabanındaki seviye başına sözlük kelime sayısını okur.
func sozlukSayilari(t *testing.T, gdb *gorm.DB) map[string]int {
	t.Helper()

	var rows []struct {
		Level string
		N     int
	}
	if err := gdb.Raw("SELECT level, COUNT(*) AS n FROM dictionary_words GROUP BY level").Scan(&rows).Error; err != nil {
		t.Fatalf("sözlük sayılamadı: %v", err)
	}
	out := map[string]int{}
	for _, r := range rows {
		if services.IndexOfLevel(r.Level) >= 0 {
			out[r.Level] = r.N
		}
	}
	return out
}

func statsIntMap(t *testing.T, body map[string]any, key string) map[string]int {
	t.Helper()

	raw, ok := body[key].(map[string]any)
	if !ok {
		t.Fatalf("%q alanı yok ya da nesne değil (body: %v)", key, body)
	}
	out := make(map[string]int, len(raw))
	for k, v := range raw {
		out[k] = int(v.(float64))
	}
	return out
}

func rozetKazanildi(t *testing.T, body map[string]any, id string) bool {
	t.Helper()

	raw, _ := body["badges"].([]any)
	for _, b := range raw {
		m := b.(map[string]any)
		if m["id"] == id {
			return m["isEarned"].(bool)
		}
	}
	t.Fatalf("%s rozeti yanıtta yok", id)
	return false
}

func statsOzet(t *testing.T, app *fiber.App) map[string]any {
	t.Helper()

	status, body := statsGet(t, app, "/stats/summary")
	if status != fiber.StatusOK {
		t.Fatalf("GET /stats/summary status = %d (body: %v)", status, body)
	}
	return body
}

func TestStatsSummary_LevelTotalsSozluktenGelir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := statsApp(New(gdb, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	testSozlukKelimeleri(t, gdb, "C2", 3)
	want := sozlukSayilari(t, gdb)

	body := statsOzet(t, app)
	if got := statsIntMap(t, body, "levelTotals"); !reflect.DeepEqual(got, want) {
		t.Errorf("levelTotals = %v, beklenen (sözlükteki sayılar) %v", got, want)
	}
	total := statsIntMap(t, body, "totalWordsByLevel")
	for _, lvl := range services.Levels {
		beklenen := want[lvl]
		if beklenen == 0 {
			beklenen = services.FallbackLevelTotals[lvl]
		}
		if total[lvl] != beklenen {
			t.Errorf("totalWordsByLevel[%s] = %d, beklenen %d", lvl, total[lvl], beklenen)
		}
	}
}

func TestStatsSummary_SeviyeRozetiSozluktekiTumKelimelerleKazanilir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := statsApp(New(gdb, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	ids := testSozlukKelimeleri(t, gdb, "C2", 3)

	// Kullanıcı sözlükteki bütün C2 kelimelerini kutu 2'ye taşımış, biri hariç.
	if err := gdb.Exec(`INSERT INTO word_progresses (user_id, word_id, level, grp, box, created_at, updated_at)
		SELECT ?, id, level, grp, 2, now(), now() FROM dictionary_words WHERE level = 'C2'`, u.ID).Error; err != nil {
		t.Fatalf("ilerleme yazılamadı: %v", err)
	}
	if err := gdb.Exec("UPDATE word_progresses SET box = 1 WHERE user_id = ? AND word_id = ?", u.ID, ids[0]).Error; err != nil {
		t.Fatalf("ilerleme güncellenemedi: %v", err)
	}
	// Sözlükte olmayan (silinmiş) bir kelimenin ilerlemesi eksik kelimeyi telafi etmemeli.
	statsTestWord(t, gdb, u.ID, "itest_bb_sozlukte_yok", "C2", 2)

	if body := statsOzet(t, app); rozetKazanildi(t, body, "level_C2") {
		t.Fatalf("level_C2 bir kelime kutu 1'deyken kazanıldı")
	}

	if err := gdb.Exec("UPDATE word_progresses SET box = 2 WHERE user_id = ? AND word_id = ?", u.ID, ids[0]).Error; err != nil {
		t.Fatalf("ilerleme güncellenemedi: %v", err)
	}
	body := statsOzet(t, app)
	if !rozetKazanildi(t, body, "level_C2") {
		t.Errorf("level_C2 sözlükteki bütün kelimeler kutu 2'deyken kazanılmadı")
	}
	if !statsIcerir(yeniRozetler(t, body), "level_C2") {
		t.Errorf("level_C2 yeni kazanılanlarda yok: %v", yeniRozetler(t, body))
	}
}

// statsTestWord, testWord'ün seviyesi verilebilen halidir.
func statsTestWord(t *testing.T, gdb *gorm.DB, userID, wordID, level string, box int) {
	t.Helper()

	wp := &models.WordProgress{UserID: userID, WordID: wordID, Level: level, Grp: 1, Box: box}
	if err := gdb.Create(wp).Error; err != nil {
		t.Fatalf("kelime ilerlemesi oluşturulamadı: %v", err)
	}
}

func statsIcerir(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// statsKelime, verilen kutu/seviye/son tekrar tarihiyle bir ilerleme satırı açar.
func statsKelime(t *testing.T, gdb *gorm.DB, userID, wordID, level string, box int, last *time.Time, english string) {
	t.Helper()

	wp := &models.WordProgress{UserID: userID, WordID: wordID, Level: level, Grp: 1, Box: box, LastReviewDate: last, English: english, Turkish: "tr-" + english}
	if err := gdb.Create(wp).Error; err != nil {
		t.Fatalf("kelime ilerlemesi oluşturulamadı: %v", err)
	}
}

func TestStatsSummary_KutuSayilariVeSonOgrenilenler(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := statsApp(New(gdb, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	sozluk := testSozlukKelimeleri(t, gdb, "C1", 1)
	gun := func(n int) *time.Time {
		v := time.Date(2026, 10, n, 9, 0, 0, 0, time.UTC)
		return &v
	}

	statsKelime(t, gdb, u.ID, "w_a1_kutu1", "A1", 1, nil, "k1") // sayılmaz
	statsKelime(t, gdb, u.ID, "w_a1_kutu2a", "A1", 2, nil, "k2a")
	statsKelime(t, gdb, u.ID, "w_a1_kutu2b", "A1", 2, nil, "k2b")
	statsKelime(t, gdb, u.ID, "w_a1_kutu3", "A1", 3, nil, "k3")
	statsKelime(t, gdb, u.ID, "w_a2_kutu4", "A2", 4, nil, "k4")
	statsKelime(t, gdb, u.ID, "w_a2_kutu5", "A2", 5, nil, "k5")
	statsKelime(t, gdb, u.ID, "w_a1_ogr1", "A1", 6, gun(1), "o1")
	statsKelime(t, gdb, u.ID, "w_a1_ogr2", "A1", 6, gun(5), "o2")
	statsKelime(t, gdb, u.ID, "w_a1_ogr3", "A1", 6, nil, "o3") // tarihsiz en sona
	statsKelime(t, gdb, u.ID, "w_b1_ogr4", "B1", 6, gun(3), "o4")
	statsKelime(t, gdb, u.ID, "w_b1_ogr5", "B1", 6, gun(7), "o5")
	// İngilizcesi boş satır sözlükten doldurulur.
	statsKelime(t, gdb, u.ID, sozluk[0], "C1", 6, gun(6), "")

	body := statsOzet(t, app)

	if got := intField(t, body, "totalLearned"); got != 6 {
		t.Errorf("totalLearned = %d, beklenen 6", got)
	}
	for alan, want := range map[string]int{"box2Count": 2, "box3Count": 1, "box4Count": 1, "box5Count": 1} {
		if got := intField(t, body, alan); got != want {
			t.Errorf("%s = %d, beklenen %d", alan, got, want)
		}
	}
	if got, want := statsIntMap(t, body, "learnedByLevel"), map[string]int{"A1": 6, "A2": 2, "B1": 2, "C1": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("learnedByLevel = %v, beklenen %v", got, want)
	}
	if got, want := statsIntMap(t, body, "box6ByLevel"), map[string]int{"A1": 3, "B1": 2, "C1": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("box6ByLevel = %v, beklenen %v", got, want)
	}

	raw, _ := body["lastLearnedWords"].([]any)
	var ids []string
	for _, w := range raw {
		m := w.(map[string]any)
		ids = append(ids, m["wordId"].(string))
		if m["wordId"] == sozluk[0] && m["english"] != "word"+sozluk[0] {
			t.Errorf("boş İngilizce sözlükten doldurulmadı: %v", m)
		}
	}
	if want := []string{"w_b1_ogr5", sozluk[0], "w_a1_ogr2", "w_b1_ogr4", "w_a1_ogr1"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("lastLearnedWords = %v, beklenen %v", ids, want)
	}
}

// learnedWordsSayfasi, GET /stats/learned-words yanıtının kimliklerini ve total'ını döner.
func learnedWordsSayfasi(t *testing.T, app *fiber.App, query string) ([]string, int) {
	t.Helper()

	status, body := statsGet(t, app, "/stats/learned-words"+query)
	if status != fiber.StatusOK {
		t.Fatalf("GET /stats/learned-words%s status = %d (body: %v)", query, status, body)
	}
	raw, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items yok ya da dizi değil (body: %v)", body)
	}
	ids := make([]string, 0, len(raw))
	for _, w := range raw {
		ids = append(ids, w.(map[string]any)["wordId"].(string))
	}
	return ids, intField(t, body, "total")
}

func TestLearnedWords_SayfalamaSinirliVeKaymaz(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := statsApp(New(gdb, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	// 205 öğrenilmiş kelime, hepsi aynı (boş) son tekrar tarihiyle: sıralama
	// yalnızca tarihe bakarsa sayfalar arasında kelime tekrar edip kaybolabilir.
	if err := gdb.Exec(`INSERT INTO word_progresses (user_id, word_id, level, grp, box, english, turkish, created_at, updated_at)
		SELECT ?, 'lw_' || g, 'A1', 1, 6, 'w' || g, 'k', now(), now() FROM generate_series(1, 205) g`, u.ID).Error; err != nil {
		t.Fatalf("kelimeler yazılamadı: %v", err)
	}
	statsKelime(t, gdb, u.ID, "lw_b1", "B1", 6, nil, "b1")
	statsKelime(t, gdb, u.ID, "lw_kutu5", "A1", 5, nil, "k5") // öğrenilmemiş

	if ids, total := learnedWordsSayfasi(t, app, ""); len(ids) != 25 || total != 206 {
		t.Errorf("varsayılan sayfa: %d kelime, total %d; beklenen 25 ve 206", len(ids), total)
	}
	if ids, total := learnedWordsSayfasi(t, app, "?limit=100000"); len(ids) != 200 || total != 206 {
		t.Errorf("limit=100000: %d kelime, total %d; beklenen 200 (üst sınır) ve 206", len(ids), total)
	}
	if ids, _ := learnedWordsSayfasi(t, app, "?limit=abc&offset=-5"); len(ids) != 25 {
		t.Errorf("geçersiz limit/offset: %d kelime, beklenen 25", len(ids))
	}
	if ids, total := learnedWordsSayfasi(t, app, "?level=B1&limit=1"); !reflect.DeepEqual(ids, []string{"lw_b1"}) || total != 1 {
		t.Errorf("level=B1: %v total %d; beklenen [lw_b1] ve 1", ids, total)
	}

	gorulen := map[string]bool{}
	for offset := 0; offset < 206; offset += 50 {
		ids, _ := learnedWordsSayfasi(t, app, fmt.Sprintf("?level=A1&limit=50&offset=%d", offset))
		for _, id := range ids {
			if gorulen[id] {
				t.Fatalf("%s iki sayfada döndü (offset %d)", id, offset)
			}
			gorulen[id] = true
		}
	}
	if len(gorulen) != 205 {
		t.Errorf("sayfalar %d farklı kelime döndü, beklenen 205", len(gorulen))
	}
}

func TestLoginDates_Son365GunYenidenEskiye(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := statsApp(New(gdb, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	now := utcAn(t, "2026-10-23T09:00:00Z") // TR 23 Ekim 12:00
	sabitSaat(t, now)

	for _, r := range []struct{ gun, an string }{
		{"2025-09-01", "2025-09-01T09:00:00Z"}, // pencere dışında
		// 366 gün önce yazılmış eski UTC kaydı; created_at TR'de 365 gün önceye düşer.
		{"2025-10-22", "2025-10-22T22:30:00Z"},
		{"2026-01-15", "2026-01-15T12:00:00Z"},
		{"2026-10-22", "2026-10-22T12:00:00Z"},
	} {
		if err := gdb.Exec(`INSERT INTO daily_logins (user_id, login_date, created_at)
			VALUES (?, CAST(? AS date), ?)`, u.ID, r.gun, utcAn(t, r.an)).Error; err != nil {
			t.Fatalf("giriş kaydı yazılamadı: %v", err)
		}
	}

	want := []string{"2026-10-22", "2026-01-15", "2025-10-23"}
	if got := statsLoginDates(t, app); !reflect.DeepEqual(got, want) {
		t.Errorf("login-dates = %v, beklenen %v", got, want)
	}

	// Bugün girilmediği için seri 0 (dün girişli olsa bile).
	if got := intField(t, statsOzet(t, app), "currentStreak"); got != 0 {
		t.Errorf("currentStreak = %d, beklenen 0", got)
	}
}

func TestLogDailyLogin_AyniGunTekrarIdempotent(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := statsApp(New(gdb, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	at := utcAn(t, "2026-10-23T09:00:00Z")
	for i := 0; i < 3; i++ {
		if got := girisYap(t, app, at); got != 1 {
			t.Errorf("%d. giriş serisi = %d, beklenen 1", i+1, got)
		}
	}
	if got := kayitliGunler(t, gdb, u.ID); !reflect.DeepEqual(got, []string{"2026-10-23"}) {
		t.Errorf("kayıtlı günler = %v, beklenen [2026-10-23]", got)
	}
}

func TestStatsSummary_YanitSekliGeriyeUyumlu(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := statsApp(New(gdb, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	// Yeni kullanıcı (ilerleme ve giriş yok): yayındaki istemcinin okuduğu
	// alanların hepsi bulunmalı; diziler null değil [] olmalı.
	body := statsOzet(t, app)
	for _, alan := range []string{
		"totalLearned", "currentStreak", "learnedByLevel", "box6ByLevel",
		"box2Count", "box3Count", "box4Count", "box5Count", "weeklyLoginData",
		"badges", "lastLearnedWords", "totalWordsByLevel", "newlyEarnedBadges",
	} {
		if _, ok := body[alan]; !ok {
			t.Errorf("%q alanı yanıtta yok", alan)
		}
	}
	for _, alan := range []string{"lastLearnedWords", "newlyEarnedBadges", "badges"} {
		if _, ok := body[alan].([]any); !ok {
			t.Errorf("%q dizi değil: %v", alan, body[alan])
		}
	}
	for _, lvl := range services.Levels {
		if statsIntMap(t, body, "totalWordsByLevel")[lvl] <= 0 {
			t.Errorf("totalWordsByLevel[%s] pozitif değil (istemci paydası)", lvl)
		}
	}
}

// eszamanliIstek, aynı isteği n goroutine'den aynı anda atar ve durum
// kodlarıyla gövdeleri döner.
func eszamanliIstek(t *testing.T, app *fiber.App, method, path string, n int) ([]int, []map[string]any) {
	t.Helper()

	type sonuc struct {
		status int
		body   map[string]any
	}
	basla := make(chan struct{})
	done := make(chan sonuc, n)
	for i := 0; i < n; i++ {
		go func() {
			<-basla
			res, err := app.Test(httptest.NewRequest(method, path, nil), -1)
			if err != nil {
				done <- sonuc{}
				return
			}
			defer res.Body.Close()
			var body map[string]any
			_ = json.NewDecoder(res.Body).Decode(&body)
			done <- sonuc{res.StatusCode, body}
		}()
	}
	close(basla)

	statuses := make([]int, 0, n)
	bodies := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		r := <-done
		statuses = append(statuses, r.status)
		bodies = append(bodies, r.body)
	}
	return statuses, bodies
}

func TestLogDailyLogin_EszamanliGirislerTRGeceYarisindaTekSatir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := statsApp(New(gdb, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	// TR 23 Ekim 23:59:59 (20:59:59Z): son saniye hâlâ 23 Ekim.
	if got := girisYap(t, app, utcAn(t, "2026-10-23T20:59:59Z")); got != 1 {
		t.Fatalf("23:59:59 girişi serisi = %d, beklenen 1", got)
	}

	// TR 24 Ekim 00:00:00 (21:00:00Z) tam gece yarısında aynı kullanıcıdan 10
	// eşzamanlı giriş: hepsi 200, tek satır, hepsi aynı seriyi (2) görür.
	sabitSaat(t, utcAn(t, "2026-10-23T21:00:00Z"))
	statuses, bodies := eszamanliIstek(t, app, http.MethodPost, "/stats/daily-login", 10)
	for i, s := range statuses {
		if s != fiber.StatusOK {
			t.Fatalf("%d. eşzamanlı giriş status = %d (body: %v)", i+1, s, bodies[i])
		}
		if got := intField(t, bodies[i], "currentStreak"); got != 2 {
			t.Errorf("%d. eşzamanlı giriş serisi = %d, beklenen 2", i+1, got)
		}
	}
	if got, want := kayitliGunler(t, gdb, u.ID), []string{"2026-10-23", "2026-10-24"}; !reflect.DeepEqual(got, want) {
		t.Errorf("kayıtlı günler = %v, beklenen %v", got, want)
	}
}

func TestStatsSummary_KilitsizEszamanliIsteklerdeHerRozetEnFazlaBirKez(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	app := statsApp(New(gdb, &config.Config{AppEnv: "test"}, nil, nil), u.ID)

	// first_day + box_mastery_1 (bir kelime kutu 6'da) ilk özette kazanılır.
	statsTestWord(t, gdb, u.ID, "itest_bb_eszamanli_1", "A1", 6)

	statuses, bodies := eszamanliIstek(t, app, http.MethodGet, "/stats/summary", 8)
	sayac := map[string]int{}
	for i, s := range statuses {
		if s != fiber.StatusOK {
			t.Fatalf("%d. eşzamanlı özet status = %d (body: %v)", i+1, s, bodies[i])
		}
		for _, id := range yeniRozetler(t, bodies[i]) {
			sayac[id]++
		}
	}
	for _, id := range []string{"first_day", "box_mastery_1"} {
		if sayac[id] != 1 {
			t.Errorf("%s %d yanıtta yeni döndü, beklenen tam 1", id, sayac[id])
		}
	}
	for id, n := range sayac {
		if n > 1 {
			t.Errorf("%s %d yanıtta yeni döndü", id, n)
		}
	}

	var satirlar []struct {
		BadgeID string
		N       int
	}
	gdb.Raw(`SELECT badge_id, count(*) AS n FROM earned_badges WHERE user_id = ?
		GROUP BY badge_id`, u.ID).Scan(&satirlar)
	for _, r := range satirlar {
		if r.N != 1 {
			t.Errorf("%s için %d satır var, beklenen 1", r.BadgeID, r.N)
		}
		if sayac[r.BadgeID] != 1 {
			t.Errorf("%s yazıldı ama %d yanıtta yeni bildirildi", r.BadgeID, sayac[r.BadgeID])
		}
	}
}

func TestSeviyeRozeti_HaritaKilidiyleTutarli(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	app := statsApp(h, u.ID)
	app.Get("/map/levels/:level/groups", func(c *fiber.Ctx) error {
		c.Locals("userID", u.ID)
		return c.Next()
	}, h.LevelGroups)

	ids := testSozlukKelimeleri(t, gdb, "B2", 3)
	testSozlukKelimeleri(t, gdb, "C1", 1)

	// Sözlükteki bütün B2 kelimeleri kutu 2'de, biri hariç (kutu 1'de).
	if err := gdb.Exec(`INSERT INTO word_progresses (user_id, word_id, level, grp, box, created_at, updated_at)
		SELECT ?, id, level, grp, 2, now(), now() FROM dictionary_words WHERE level = 'B2'`, u.ID).Error; err != nil {
		t.Fatalf("ilerleme yazılamadı: %v", err)
	}
	if err := gdb.Exec("UPDATE word_progresses SET box = 1 WHERE user_id = ? AND word_id = ?", u.ID, ids[0]).Error; err != nil {
		t.Fatalf("ilerleme güncellenemedi: %v", err)
	}

	c1IlkGrupAcik := func() bool {
		t.Helper()
		res, err := app.Test(httptest.NewRequest(http.MethodGet, "/map/levels/C1/groups", nil), -1)
		if err != nil {
			t.Fatalf("GET /map/levels/C1/groups: %v", err)
		}
		defer res.Body.Close()
		var groups []map[string]any
		if err := json.NewDecoder(res.Body).Decode(&groups); err != nil || len(groups) == 0 {
			t.Fatalf("C1 grupları çözülemedi (status %d): %v", res.StatusCode, err)
		}
		return groups[0]["isUnlocked"].(bool)
	}

	// B2 bitmeden ne rozet verilmeli ne de C1 açılmalı (eski 2200 eşiğinin hatası).
	if rozetKazanildi(t, statsOzet(t, app), "level_B2") {
		t.Fatalf("level_B2 bir B2 kelimesi kutu 1'deyken kazanıldı")
	}
	if c1IlkGrupAcik() {
		t.Errorf("C1 B2 bitmeden açıldı")
	}

	if err := gdb.Exec("UPDATE word_progresses SET box = 2 WHERE user_id = ? AND word_id = ?", u.ID, ids[0]).Error; err != nil {
		t.Fatalf("ilerleme güncellenemedi: %v", err)
	}
	body := statsOzet(t, app)
	if !rozetKazanildi(t, body, "level_B2") || !statsIcerir(yeniRozetler(t, body), "level_B2") {
		t.Fatalf("level_B2 B2 bitince yeni kazanılmadı: %v", yeniRozetler(t, body))
	}
	if !c1IlkGrupAcik() {
		t.Errorf("C1 B2 bitip rozet kazanıldıktan sonra açılmadı")
	}

	// Rozet kalıcıdır: bir kelime kutu 1'e dönse de (earnedBefore) hem rozet hem
	// harita hızlı yolu açık kalır.
	if err := gdb.Exec("UPDATE word_progresses SET box = 1 WHERE user_id = ? AND word_id = ?", u.ID, ids[1]).Error; err != nil {
		t.Fatalf("ilerleme güncellenemedi: %v", err)
	}
	if body := statsOzet(t, app); !rozetKazanildi(t, body, "level_B2") || len(yeniRozetler(t, body)) != 0 {
		t.Errorf("level_B2 kalıcı değil ya da yeniden bildirildi: %v", yeniRozetler(t, body))
	}
	if !c1IlkGrupAcik() {
		t.Errorf("C1 kazanılmış level_B2 rozetine rağmen kilitlendi")
	}
}
