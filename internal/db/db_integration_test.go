package db

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kelimeler-backend/internal/db/dbtest"
	"kelimeler-backend/internal/models"

	"gorm.io/gorm"
)

// Bu dosya gerçek bir PostgreSQL gerektirir (bkz. internal/handlers/usage_integration_test.go).
// Seed testleri dictionary_words'ü boşaltmak zorunda olduğundan her test kendi
// geçici şemasında çalışır; paralel koşan diğer paketlerin verisine dokunmaz.

func testDSN(t *testing.T) string {
	t.Helper()
	return dbtest.DSN(t)
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

func show(t *testing.T, gdb *gorm.DB, setting string) string {
	t.Helper()
	var v string
	if err := gdb.Raw("SHOW " + setting).Scan(&v).Error; err != nil {
		t.Fatalf("SHOW %s: %v", setting, err)
	}
	return v
}

func TestConnectWithOptions_HavuzVeSorguZamanAsimi(t *testing.T) {
	dsn := testDSN(t)

	opts := Options{MaxOpenConns: 7, MaxIdleConns: 3, ConnMaxLifetime: time.Minute, StatementTimeout: 200 * time.Millisecond}
	gdb, err := ConnectWithOptions(dsn, opts)
	if err != nil {
		t.Fatalf("bağlanılamadı: %v", err)
	}
	closeOnCleanup(t, gdb)

	sqlDB, _ := gdb.DB()
	if got := sqlDB.Stats().MaxOpenConnections; got != 7 {
		t.Errorf("MaxOpenConnections = %d, beklenen 7", got)
	}
	if got := show(t, gdb, "statement_timeout"); got != "200ms" {
		t.Errorf("statement_timeout = %q, beklenen 200ms", got)
	}

	// Kaçak sorgu sunucu tarafında iptal edilir (SQLSTATE 57014).
	err = gdb.Exec("SELECT pg_sleep(2)").Error
	if err == nil || !strings.Contains(err.Error(), "57014") {
		t.Errorf("pg_sleep(2) hatası = %v, beklenen statement timeout (57014)", err)
	}

	// İptalden sonra havuz kullanılabilir kalır.
	if got := show(t, gdb, "statement_timeout"); got != "200ms" {
		t.Errorf("iptal sonrası statement_timeout = %q", got)
	}

	// Seed'in çoklu statement Exec'i için simple protocol korunur.
	if err := gdb.Exec("SELECT 1; SELECT 2").Error; err != nil {
		t.Errorf("çoklu statement Exec başarısız (PreferSimpleProtocol kaybolmuş olabilir): %v", err)
	}

	// Oturum saat dilimi değişmez: date kolonları bu ayara göre yorumlanır.
	plain, err := Connect(dsn)
	if err != nil {
		t.Fatalf("bağlanılamadı: %v", err)
	}
	closeOnCleanup(t, plain)
	if a, b := show(t, gdb, "TimeZone"), show(t, plain, "TimeZone"); a != b {
		t.Errorf("TimeZone = %q, zaman aşımsız bağlantıda %q; oturum saat dilimi değişmemeli", a, b)
	}
}

func TestConnectWithOptions_DSNdekiZamanAsimiOncelikli(t *testing.T) {
	dsn := withParam(t, testDSN(t), "statement_timeout", "300")
	gdb, err := ConnectWithOptions(dsn, Options{MaxOpenConns: 2, MaxIdleConns: 1, StatementTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("bağlanılamadı: %v", err)
	}
	closeOnCleanup(t, gdb)
	if got := show(t, gdb, "statement_timeout"); got != "300ms" {
		t.Errorf("statement_timeout = %q, beklenen DSN'deki 300ms", got)
	}
}

// Migration ve seed sorgu zaman aşımına takılmamalı; ardından havuzdaki
// bağlantılar yine zaman aşımıyla çalışmalı.
func TestMigrateVeSeed_ZamanAsimindanEtkilenmez(t *testing.T) {
	dsn := testDSN(t)
	schema := fmt.Sprintf("dbtest_to_%d", time.Now().UnixNano())

	admin, err := Connect(dsn)
	if err != nil {
		t.Fatalf("bağlanılamadı: %v", err)
	}
	closeOnCleanup(t, admin)
	if err := Migrate(admin); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("şema: %v", err)
	}
	t.Cleanup(func() { admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE") })

	gdb, err := ConnectWithOptions(withParam(t, dsn, "search_path", schema+",public"),
		Options{MaxOpenConns: 1, MaxIdleConns: 1, StatementTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("bağlanılamadı: %v", err)
	}
	closeOnCleanup(t, gdb)

	// Zaman aşımı kapatılmasaydı uzun süren bir migration adımı yarıda kesilirdi.
	if err := gdb.Connection(func(conn *gorm.DB) error {
		return conn.Exec("SELECT pg_sleep(0.3)").Error
	}); err == nil {
		t.Fatal("ön koşul: 100ms zaman aşımında pg_sleep(0.3) başarısız olmalıydı")
	}
	if err := Migrate(gdb); err != nil {
		t.Fatalf("zaman aşımlı havuzda migrate başarısız: %v", err)
	}
	yavasSeed := "INSERT INTO dictionary_words (id, english, turkish, example, level, grp, word_order) " +
		"SELECT 'Y_1_1', 'a', 'b', 'c', 'A1', 1, 1 FROM pg_sleep(0.3);\n"
	if n, err := SeedDictionaryIfEmpty(gdb, seedFile(t, yavasSeed)); err != nil || n != 1 {
		t.Fatalf("zaman aşımından uzun seed = (%d, %v), beklenen (1, nil)", n, err)
	}

	// Migrate sonrası bağlantı yine zaman aşımıyla döner (MaxOpenConns=1: aynı bağlantı).
	if got := show(t, gdb, "statement_timeout"); got != "100ms" {
		t.Errorf("migrate sonrası statement_timeout = %q, beklenen 100ms", got)
	}
}

func TestMigrate_AdvisoryLockAltindaCalisir(t *testing.T) {
	// Başka bir oturum migrate kilidini tutarken Migrate bekler; kilit
	// bırakılınca tamamlanır.
	dsn := testDSN(t)
	tutan, err := Connect(dsn)
	if err != nil {
		t.Fatalf("veritabanına bağlanılamadı: %v", err)
	}
	closeOnCleanup(t, tutan)
	gdb, err := Connect(dsn)
	if err != nil {
		t.Fatalf("veritabanına bağlanılamadı: %v", err)
	}
	closeOnCleanup(t, gdb)

	err = tutan.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec(`SELECT pg_advisory_lock(?)`, migrateLockKey).Error; err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- Migrate(gdb) }()

		var bekleyen int64
		for son := time.Now().Add(5 * time.Second); bekleyen < 1 && time.Now().Before(son); {
			select {
			case err := <-done:
				conn.Exec(`SELECT pg_advisory_unlock(?)`, migrateLockKey)
				return fmt.Errorf("Migrate kilidi beklemeden bitti (hata: %v)", err)
			case <-time.After(20 * time.Millisecond):
			}
			conn.Raw(`SELECT count(*) FROM pg_stat_activity
				WHERE datname = current_database() AND wait_event_type = 'Lock' AND wait_event = 'advisory'`).Scan(&bekleyen)
		}
		if err := conn.Exec(`SELECT pg_advisory_unlock(?)`, migrateLockKey).Error; err != nil {
			return err
		}
		if bekleyen < 1 {
			return errors.New("Migrate advisory lock'ta beklemeye geçmedi")
		}
		select {
		case err := <-done:
			return err
		case <-time.After(30 * time.Second):
			return errors.New("kilit bırakıldıktan sonra Migrate bitmedi")
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	// Kilit Migrate'ten sonra bırakılmış olmalı: başka oturum alabilir.
	// Paralel koşan diğer paketlerin Migrate'i kilidi kısa süre tutabilir;
	// Migrate'in (havuza dönen) bağlantısı tutsaydı hiç alınamazdı.
	var alindi bool
	for son := time.Now().Add(10 * time.Second); !alindi && time.Now().Before(son); {
		if err := tutan.Connection(func(conn *gorm.DB) error {
			if err := conn.Raw(`SELECT pg_try_advisory_lock(?)`, migrateLockKey).Scan(&alindi).Error; err != nil {
				return err
			}
			if alindi {
				return conn.Exec(`SELECT pg_advisory_unlock(?)`, migrateLockKey).Error
			}
			return nil
		}); err != nil {
			t.Fatalf("kilit denenemedi: %v", err)
		}
		if !alindi {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !alindi {
		t.Error("Migrate kilidi bırakmadı")
	}
}

func TestMigrate_EszamanliBosSemadaHepsiBasarili(t *testing.T) {
	// CI'da paralel paketler ve aynı anda açılan replikalar: boş bir şemaya
	// aynı anda gelen Migrate'lerin hiçbiri "already exists" ile düşmez.
	dsn := testDSN(t)
	admin, err := Connect(dsn)
	if err != nil {
		t.Fatalf("veritabanına bağlanılamadı: %v", err)
	}
	closeOnCleanup(t, admin)
	if err := Migrate(admin); err != nil {
		t.Fatalf("migrate başarısız: %v", err)
	}
	schema := fmt.Sprintf("dbtest_mig_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("şema oluşturulamadı: %v", err)
	}
	t.Cleanup(func() { admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE") })

	const n = 4
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		gdb, err := Connect(withParam(t, dsn, "search_path", schema+",public"))
		if err != nil {
			t.Fatalf("geçici şemaya bağlanılamadı: %v", err)
		}
		closeOnCleanup(t, gdb)
		go func() { errs <- Migrate(gdb) }()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("eşzamanlı Migrate başarısız: %v", err)
		}
	}
}
