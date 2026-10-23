package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/middleware"
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

func get(t *testing.T, app *fiber.App, path string) (int, map[string]any) {
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

// loginDates, GET /stats/login-dates yanıtındaki tarihleri döner.
func loginDates(t *testing.T, app *fiber.App) []string {
	t.Helper()

	status, body := get(t, app, "/stats/login-dates")
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
	if got, want := loginDates(t, app), []string{"2026-10-25", "2026-10-24", "2026-10-23"}; !reflect.DeepEqual(got, want) {
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

	status, body := get(t, app, "/stats/summary")
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
