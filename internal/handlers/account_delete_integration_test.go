package handlers

import (
	"net/http"
	"testing"
	"time"

	"kelimeler-backend/internal/config"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Bu dosya hesap silmenin eşzamanlı swipe/learn ve refresh ile
// kilitlenmediğini gerçek PostgreSQL'e karşı sınar; kurulum için
// usage_integration_test.go başındaki nota bakın.

// silmeyiBaslat, DELETE /auth/account'u arka planda gönderir ve isteğin bir
// satır kilidinde beklemeye geçmesini bekler; durum kodu kanaldan gelir.
func silmeyiBaslat(t *testing.T, gdb *gorm.DB, app *fiber.App, req *http.Request) <-chan int {
	t.Helper()

	done := make(chan int, 1)
	go func() {
		res, err := app.Test(req, -1)
		if err != nil {
			done <- 0
			return
		}
		res.Body.Close()
		done <- res.StatusCode
	}()

	var bekleyen int64
	for son := time.Now().Add(10 * time.Second); bekleyen < 1 && time.Now().Before(son); {
		time.Sleep(10 * time.Millisecond)
		gdb.Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&bekleyen)
	}
	if bekleyen < 1 {
		t.Fatal("silme isteği kilitte beklemeye geçmedi; sıralama kurulamadı")
	}
	return done
}

func TestDeleteAccount_SuranSwipeIleKilitlenmez(t *testing.T) {
	// Swipe'ın kilit sırası: önce kelime satırı (FOR UPDATE), sonra kullanıcı
	// satırı (reviewGate'te FOR UPDATE ya da sayaç UPDATE'i). Swipe kelimeyi
	// tutarken silme gelir, sonra swipe kullanıcı satırını ister. Silme önce
	// kullanıcıyı kilitleseydi ikisi birbirini bekler ve PostgreSQL birini iptal
	// ederdi (swipe ya da silme 500).
	gdb := testDB(t)
	u := testUser(t, gdb)
	wordID := "w-silme-swipe-" + u.ID
	testWord(t, gdb, u.ID, wordID, 2)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)
	app := authApp(h)

	swipe := gdb.Begin()
	defer swipe.Rollback()
	if err := swipe.Exec("SELECT 1 FROM word_progresses WHERE user_id = ? AND word_id = ? FOR UPDATE", u.ID, wordID).Error; err != nil {
		t.Fatalf("kelime satırı kilitlenemedi: %v", err)
	}

	done := silmeyiBaslat(t, gdb, app, authRequest(t, h, u, http.MethodDelete, "/api/v1/auth/account", nil))

	if err := swipe.Exec("SELECT 1 FROM users WHERE id = ? FOR UPDATE", u.ID).Error; err != nil {
		t.Fatalf("swipe kullanıcı satırını alamadı (kilitlenme): %v", err)
	}
	if err := swipe.Exec("UPDATE word_progresses SET box = 3 WHERE user_id = ? AND word_id = ?", u.ID, wordID).Error; err != nil {
		t.Fatalf("swipe yazamadı: %v", err)
	}
	if err := swipe.Commit().Error; err != nil {
		t.Fatalf("swipe commit edilemedi: %v", err)
	}

	if status := <-done; status != fiber.StatusNoContent {
		t.Fatalf("silme status = %d, beklenen 204", status)
	}
	if userExists(t, gdb, u.ID) {
		t.Error("kullanıcı silinmedi")
	}
	if n := progressRowCount(t, gdb, u.ID, wordID); n != 0 {
		t.Errorf("kelime satırı kaldı (%d)", n)
	}
}

func TestDeleteAccount_KilitlenmedeBirKezYenidenDener(t *testing.T) {
	// Ters sıralı başka bir işlem: refresh token satırını tutup kullanıcı
	// satırına paylaşımlı kilit ister (ör. aynı kullanıcıya bağlı yeni satır
	// yazan bir yenileme). Silme önce beklemeye geçtiği için kilitlenmeyi o
	// fark eder ve iptal edilir; yeniden deneme karşı işlem bitince geçer.
	gdb := testDB(t)
	u := testUser(t, gdb)
	testRefreshToken(t, gdb, u.ID, time.Now().Add(time.Hour), nil)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)
	app := authApp(h)

	diger := gdb.Begin()
	defer diger.Rollback()
	if err := diger.Exec("SELECT 1 FROM refresh_tokens WHERE user_id = ? FOR UPDATE", u.ID).Error; err != nil {
		t.Fatalf("refresh token satırı kilitlenemedi: %v", err)
	}

	done := silmeyiBaslat(t, gdb, app, authRequest(t, h, u, http.MethodDelete, "/api/v1/auth/account", nil))
	// Silmenin bekleme süresi (deadlock_timeout) önce dolsun: kilitlenmeyi
	// o fark eder.
	time.Sleep(200 * time.Millisecond)

	if err := diger.Exec("SELECT 1 FROM users WHERE id = ? FOR KEY SHARE", u.ID).Error; err != nil {
		t.Fatalf("karşı işlem kullanıcı satırını alamadı: %v", err)
	}
	if err := diger.Commit().Error; err != nil {
		t.Fatalf("karşı işlem commit edilemedi: %v", err)
	}

	if status := <-done; status != fiber.StatusNoContent {
		t.Fatalf("silme status = %d, beklenen 204 (kilitlenmeden sonra yeniden denenmeliydi)", status)
	}
	if userExists(t, gdb, u.ID) {
		t.Error("kullanıcı silinmedi")
	}
}
