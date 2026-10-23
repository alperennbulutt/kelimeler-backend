package db

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kelimeler-backend/internal/models"

	"gorm.io/gorm"
)

// Bu dosya gerçek bir PostgreSQL gerektirir (bkz. internal/handlers/usage_integration_test.go).
// Seed testleri dictionary_words'ü boşaltmak zorunda olduğundan her test kendi
// geçici şemasında çalışır; paralel koşan diğer paketlerin verisine dokunmaz.

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil, entegrasyon testi atlanıyor")
	}
	return dsn
}

// withParam, URL biçimindeki DSN'e bir bağlantı parametresi ekler.
func withParam(t *testing.T, dsn, key, value string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("TEST_DATABASE_URL ayrıştırılamadı: %v", err)
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String()
}

// closeOnCleanup, havuzu test bitince kapatır.
func closeOnCleanup(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			sqlDB.Close()
		}
	})
}

// isolatedDB, geçici bir şema açar, bağlantının search_path'ini ona çevirir ve
// şemayı migrate eder. Test bitince şema silinir.
func isolatedDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := testDSN(t)

	admin, err := Connect(dsn)
	if err != nil {
		t.Fatalf("veritabanına bağlanılamadı: %v", err)
	}
	closeOnCleanup(t, admin)
	// Extension'lar public'te kalır; geçici şemadaki tablolar onları search_path ile bulur.
	if err := Migrate(admin); err != nil {
		t.Fatalf("migrate başarısız: %v", err)
	}

	schema := fmt.Sprintf("dbtest_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("şema oluşturulamadı: %v", err)
	}
	t.Cleanup(func() { admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE") })

	gdb, err := Connect(withParam(t, dsn, "search_path", schema+",public"))
	if err != nil {
		t.Fatalf("geçici şemaya bağlanılamadı: %v", err)
	}
	closeOnCleanup(t, gdb)
	if err := Migrate(gdb); err != nil {
		t.Fatalf("geçici şema migrate edilemedi: %v", err)
	}
	return gdb
}

func seedFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "seed.sql")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("seed dosyası yazılamadı: %v", err)
	}
	return p
}

func dictCount(t *testing.T, gdb *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := gdb.Model(&models.DictionaryWord{}).Count(&n).Error; err != nil {
		t.Fatalf("sözlük sayılamadı: %v", err)
	}
	return n
}

const ikiKelime = `-- yorum satırı
SET statement_timeout = 0;
INSERT INTO dictionary_words (id, english, turkish, example, level, grp, word_order) VALUES ('T_1_1', 'a', 'b', 'c', 'A1', 1, 1);
INSERT INTO dictionary_words (id, english, turkish, example, level, grp, word_order) VALUES ('T_1_2', 'd', 'e', 'f', 'A1', 1, 2);
`

func TestSeedDictionaryIfEmpty(t *testing.T) {
	t.Run("bos tablo yuklenir", func(t *testing.T) {
		gdb := isolatedDB(t)
		n, err := SeedDictionaryIfEmpty(gdb, seedFile(t, ikiKelime))
		if err != nil || n != 2 {
			t.Fatalf("SeedDictionaryIfEmpty = (%d, %v), beklenen (2, nil)", n, err)
		}
		if got := dictCount(t, gdb); got != 2 {
			t.Errorf("sözlük = %d kelime, beklenen 2", got)
		}
	})

	t.Run("dolu tabloya dokunulmaz", func(t *testing.T) {
		gdb := isolatedDB(t)
		if _, err := SeedDictionaryIfEmpty(gdb, seedFile(t, ikiKelime)); err != nil {
			t.Fatalf("ilk seed: %v", err)
		}
		// Dosya olmasa da dolu tablo hata değildir.
		n, err := SeedDictionaryIfEmpty(gdb, filepath.Join(t.TempDir(), "yok.sql"))
		if err != nil || n != 0 {
			t.Fatalf("SeedDictionaryIfEmpty = (%d, %v), beklenen (0, nil)", n, err)
		}
	})

	// Eskiden yalnızca loglanıyordu; sunucu boş sözlükle "sağlıklı" açılıyordu.
	t.Run("bos tablo ve dosya yok hata", func(t *testing.T) {
		gdb := isolatedDB(t)
		_, err := SeedDictionaryIfEmpty(gdb, filepath.Join(t.TempDir(), "yok.sql"))
		if !errors.Is(err, ErrSeedFileMissing) {
			t.Fatalf("hata = %v, beklenen ErrSeedFileMissing", err)
		}
	})

	t.Run("hatali INSERT geri alinir ve hata doner", func(t *testing.T) {
		gdb := isolatedDB(t)
		bozuk := ikiKelime + "INSERT INTO dictionary_words (id) VALUES ('T_1_1');\n" // aynı id
		if _, err := SeedDictionaryIfEmpty(gdb, seedFile(t, bozuk)); err == nil {
			t.Fatal("hata dönmedi, beklenen seed hatası")
		}
		if got := dictCount(t, gdb); got != 0 {
			t.Errorf("yarım seed kaldı: %d kelime, beklenen 0", got)
		}
	})

	t.Run("INSERT icermeyen dosya hata", func(t *testing.T) {
		gdb := isolatedDB(t)
		if _, err := SeedDictionaryIfEmpty(gdb, seedFile(t, "-- boş döküm\n")); err == nil {
			t.Fatal("hata dönmedi, beklenen boş seed hatası")
		}
	})

	t.Run("veritabani hatasi yutulmaz", func(t *testing.T) {
		gdb := isolatedDB(t)
		sqlDB, _ := gdb.DB()
		sqlDB.Close()
		if _, err := SeedDictionaryIfEmpty(gdb, seedFile(t, ikiKelime)); err == nil {
			t.Fatal("kapalı veritabanında hata dönmedi")
		}
	})
}
