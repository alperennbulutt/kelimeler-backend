package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"kelimeler-backend/internal/auth"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/db/dbtest"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestParseFlags(t *testing.T) {
	cases := []struct {
		args    []string
		want    options
		wantErr string
	}{
		{nil, options{inactiveDays: 180}, ""},
		{[]string{"--dry-run"}, options{inactiveDays: 180}, ""},
		{[]string{"--apply"}, options{inactiveDays: 180, apply: true}, ""},
		{[]string{"--apply", "--dry-run=false", "--inactive-days", "365"}, options{inactiveDays: 365, apply: true}, ""},
		{[]string{"--apply", "--dry-run"}, options{}, "birlikte"},
		{[]string{"--inactive-days", "29"}, options{}, "en az 30"},
		{[]string{"--inactive-days", "abc"}, options{}, "invalid value"},
		{[]string{"fazla"}, options{}, "beklenmeyen"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			got, err := parseFlags(c.args, io.Discard)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("hata = %v, beklenen %q içersin", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("hata = %v", err)
			}
			if got != c.want {
				t.Errorf("seçenekler = %+v, beklenen %+v", got, c.want)
			}
		})
	}
}

// --- Gerçek PostgreSQL ile (TEST_DATABASE_URL; bkz. internal/db/dbtest) ---

func testDB(t *testing.T) *gorm.DB {
	t.Helper()

	gdb, err := db.Connect(dbtest.DSN(t))
	if err != nil {
		t.Fatalf("veritabanına bağlanılamadı: %v", err)
	}
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

// eskiKullanici, açılışı ve son yazımı ago önce olan bir kullanıcı açar ve
// test bitince siler.
func eskiKullanici(t *testing.T, gdb *gorm.DB, guest bool, ago time.Duration) models.User {
	t.Helper()

	id := uuid.NewString()
	u := models.User{ID: id, Email: dto.SyntheticEmail("guest_" + id), IsGuest: guest, CurrentLevel: "A1", CurrentGroup: 1}
	if !guest {
		u.Email = "kayitli-" + id + "@example.test"
	}
	if err := gdb.Create(&u).Error; err != nil {
		t.Fatalf("kullanıcı açılamadı: %v", err)
	}
	t.Cleanup(func() { gdb.Delete(&models.User{}, "id = ?", id) })
	when := time.Now().Add(-ago)
	if err := gdb.Exec(`UPDATE users SET created_at = ?, updated_at = ? WHERE id = ?`, when, when, id).Error; err != nil {
		t.Fatalf("zaman damgaları yazılamadı: %v", err)
	}
	return u
}

func refreshTokenAt(t *testing.T, gdb *gorm.DB, userID string, createdAt time.Time) {
	t.Helper()

	token, _ := auth.GenerateRefreshToken()
	row := &models.RefreshToken{UserID: userID, TokenHash: auth.HashToken(token),
		ExpiresAt: createdAt.Add(365 * 24 * time.Hour), CreatedAt: createdAt}
	if err := gdb.Create(row).Error; err != nil {
		t.Fatalf("refresh token yazılamadı: %v", err)
	}
}

func exists(t *testing.T, gdb *gorm.DB, id string) bool {
	t.Helper()

	var n int64
	if err := gdb.Model(&models.User{}).Where("id = ?", id).Count(&n).Error; err != nil {
		t.Fatalf("kullanıcı sayılamadı: %v", err)
	}
	return n > 0
}

func TestRun_YalnizcaTerkEdilmisMisafirleriSiler(t *testing.T) {
	gdb := testDB(t)
	const gun = 24 * time.Hour

	terk := eskiKullanici(t, gdb, true, 200*gun)
	refreshTokenAt(t, gdb, terk.ID, time.Now().Add(-190*gun)) // son açılış 190 gün önce
	if err := gdb.Create(&models.DailyLogin{UserID: terk.ID, LoginDate: time.Now().Add(-190 * gun)}).Error; err != nil {
		t.Fatalf("giriş kaydı yazılamadı: %v", err)
	}
	// daily_logins.created_at şimdiki zamanla yazıldı; geriye çek.
	gdb.Exec(`UPDATE daily_logins SET created_at = ? WHERE user_id = ?`, time.Now().Add(-190*gun), terk.ID)

	hicKullanilmamis := eskiKullanici(t, gdb, true, 181*gun)

	yeniTokenli := eskiKullanici(t, gdb, true, 300*gun)
	refreshTokenAt(t, gdb, yeniTokenli.ID, time.Now().Add(-2*gun))

	yeniIlerlemeli := eskiKullanici(t, gdb, true, 300*gun)
	wp := models.WordProgress{UserID: yeniIlerlemeli.ID, WordID: "w-" + yeniIlerlemeli.ID, Box: 2}
	if err := gdb.Create(&wp).Error; err != nil {
		t.Fatalf("ilerleme yazılamadı: %v", err)
	}

	genc := eskiKullanici(t, gdb, true, 100*gun)
	kayitli := eskiKullanici(t, gdb, false, 400*gun)

	// is_guest bir şekilde true kalmış ama kimliği olan hesap silinmez.
	kimlikli := eskiKullanici(t, gdb, true, 400*gun)
	sub := "sub-" + kimlikli.ID
	if err := gdb.Create(&models.AuthIdentity{UserID: kimlikli.ID, Provider: "google", ProviderSubject: &sub}).Error; err != nil {
		t.Fatalf("kimlik yazılamadı: %v", err)
	}

	silinecek := []string{terk.ID, hicKullanilmamis.ID}
	kalacak := []string{yeniTokenli.ID, yeniIlerlemeli.ID, genc.ID, kayitli.ID, kimlikli.ID}

	// Varsayılan: yalnızca rapor.
	var out bytes.Buffer
	res, err := run(context.Background(), gdb, options{inactiveDays: 180}, time.Now(), &out)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	for _, id := range silinecek {
		if !strings.Contains(out.String(), id) {
			t.Errorf("dry-run raporunda aday %s yok:\n%s", id, out.String())
		}
	}
	for _, id := range kalacak {
		if strings.Contains(out.String(), id) {
			t.Errorf("dry-run raporunda aday olmaması gereken %s var", id)
		}
	}
	for _, id := range append(append([]string{}, silinecek...), kalacak...) {
		if !exists(t, gdb, id) {
			t.Errorf("dry-run %s'yi sildi", id)
		}
	}
	if res.deleted != 0 || !strings.Contains(out.String(), "hiçbir şey silinmedi") {
		t.Errorf("dry-run sonucu = %+v, çıktı:\n%s", res, out.String())
	}

	// --apply
	out.Reset()
	res, err = run(context.Background(), gdb, options{inactiveDays: 180, apply: true}, time.Now(), &out)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, id := range silinecek {
		if exists(t, gdb, id) {
			t.Errorf("terk edilmiş misafir %s silinmedi", id)
		}
	}
	for _, id := range kalacak {
		if !exists(t, gdb, id) {
			t.Errorf("silinmemesi gereken %s silindi", id)
		}
	}
	if res.failed != 0 || res.deleted < len(silinecek) {
		t.Errorf("sonuç = %+v, beklenen en az %d silme ve 0 hata", res, len(silinecek))
	}
	// Bağlı veriler CASCADE ile gider.
	for _, m := range []any{&models.RefreshToken{}, &models.DailyLogin{}} {
		var n int64
		gdb.Model(m).Where("user_id = ?", terk.ID).Count(&n)
		if n != 0 {
			t.Errorf("%T: silinen misafirin %d satırı kaldı", m, n)
		}
	}
}

// Aday listesi okunduktan sonra uygulamayı açan (yeni token alan) misafir
// silinmez.
func TestDeleteInactiveGuest_ListedenSonraEtkinlesenSilinmez(t *testing.T) {
	gdb := testDB(t)
	const gun = 24 * time.Hour
	cutoff := time.Now().Add(-180 * gun)

	u := eskiKullanici(t, gdb, true, 200*gun)
	guests, err := models.FindInactiveGuests(context.Background(), gdb, cutoff)
	if err != nil {
		t.Fatalf("adaylar okunamadı: %v", err)
	}
	found := false
	for _, g := range guests {
		found = found || g.ID == u.ID
	}
	if !found {
		t.Fatal("misafir aday listesinde yok")
	}

	refreshTokenAt(t, gdb, u.ID, time.Now())
	deleted, err := models.DeleteInactiveGuest(context.Background(), gdb, u.ID, cutoff)
	if err != nil {
		t.Fatalf("silme: %v", err)
	}
	if deleted || !exists(t, gdb, u.ID) {
		t.Error("listeden sonra etkinleşen misafir silindi")
	}

	// Bu arada hesabını kaydetmiş misafir de silinmez.
	k := eskiKullanici(t, gdb, true, 200*gun)
	gdb.Model(&models.User{}).Where("id = ?", k.ID).UpdateColumn("is_guest", false)
	if deleted, err := models.DeleteInactiveGuest(context.Background(), gdb, k.ID, cutoff); err != nil || deleted {
		t.Errorf("kaydolmuş hesap: deleted=%v err=%v, beklenen silinmemesi", deleted, err)
	}
}
