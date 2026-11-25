package db

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"kelimeler-backend/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

// Options, bağlantı havuzu ve sunucu tarafı zaman aşımı ayarlarıdır.
type Options struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration

	// StatementTimeout, her sorguya PostgreSQL'in kendisinde uygulanan üst
	// süredir (statement_timeout). İstemci vazgeçtikten sonra da süren kaçak
	// sorgular bu sürede iptal edilir ve havuzdaki bağlantıyı geri verir.
	// 0 ise sunucunun varsayılanı (sınırsız) geçerlidir. DSN'de
	// statement_timeout zaten verilmişse DSN'deki değer kullanılır.
	StatementTimeout time.Duration
}

// DefaultOptions, Connect'in kullandığı ayarlardır (zaman aşımı yok).
func DefaultOptions() Options {
	return Options{MaxOpenConns: 25, MaxIdleConns: 5, ConnMaxLifetime: time.Hour}
}

// Connect, PostgreSQL'e varsayılan havuz ayarlarıyla bağlanır.
func Connect(dsn string) (*gorm.DB, error) {
	return ConnectWithOptions(dsn, DefaultOptions())
}

// ConnectWithOptions, PostgreSQL'e verilen havuz ve zaman aşımı ayarlarıyla
// bağlanır ve *gorm.DB döndürür.
func ConnectWithOptions(dsn string, opts Options) (*gorm.DB, error) {
	dsn, err := withStatementTimeout(dsn, opts.StatementTimeout)
	if err != nil {
		return nil, err
	}

	gdb, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true, // çoklu-statement seed Exec'i için gerekli
	}), &gorm.Config{
		// Yavaş sorgu ve hata logları sorgunun parametrelerini (ör. giriş
		// denemesindeki e-posta adresi) yazmaz; bulunamayan kayıt hata sayılmaz
		// (bilinmeyen adresle giriş ve ilk sosyal giriş olağan akıştır).
		Logger: gormlogger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      true,
			Colorful:                  true,
		}),
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(opts.MaxOpenConns)
	sqlDB.SetMaxIdleConns(opts.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(opts.ConnMaxLifetime)

	return gdb, nil
}

// withStatementTimeout, DSN'e statement_timeout bağlantı parametresini ekler.
// pgx bilmediği parametreleri açılış mesajında sunucuya iletir; böylece her
// bağlantı oturumu bu ayarla başlar. Yalnızca statement_timeout eklenir:
// TimeZone gibi oturum ayarlarına dokunulmaz (date kolonları PreferSimpleProtocol
// ile metin olarak gidip geldiğinden, oturum saat dilimi tarihleri kaydırabilir).
func withStatementTimeout(dsn string, d time.Duration) (string, error) {
	if d <= 0 || strings.Contains(dsn, "statement_timeout") {
		return dsn, nil
	}
	ms := d.Milliseconds()
	if ms == 0 {
		ms = 1
	}
	value := strconv.FormatInt(ms, 10)

	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			// Ayrıştırma hatası parolayı içerebilir; DSN loglanmasın.
			return "", errors.New("DATABASE_URL ayrıştırılamadı")
		}
		q := u.Query()
		q.Set("statement_timeout", value)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	// "host=... dbname=..." biçimi.
	return strings.TrimSpace(dsn) + " statement_timeout=" + value, nil
}

// migrateLockKey, aynı veritabanında Migrate'lerin aynı anda çalışmasını
// engelleyen advisory lock anahtarıdır (keyfi ama sabit; refresh token
// temizliğininkinden farklı).
const migrateLockKey int64 = 0x6b656c2d6d6967 // "kel-mig"

// Migrate, gerekli extension'ları kurar ve tüm modelleri AutoMigrate eder.
//
// Tek bir bağlantıda ve statement_timeout kapalıyken çalışır: büyük bir tabloda
// index kurmak ya da eski container'ın kilidini beklemek sorgu zaman aşımından
// uzun sürebilir ve yarım kalan migration açılışı engeller.
//
// Bağlantı ömürlü bir advisory lock (pg_advisory_lock) altında çalışır: aynı
// anda açılan replikalar ya da CI'da paralel koşan test paketleri sırayla
// migrate eder. AutoMigrate önce tabloya bakıp sonra IF NOT EXISTS'siz CREATE
// TABLE yaptığından, eşzamanlı iki migrate'ten biri "already exists" ile
// düşebilirdi. Kilit bekleme de zaman aşımına tabi değildir; bağlantı koparsa
// PostgreSQL kilidi kendiliğinden bırakır.
func Migrate(gdb *gorm.DB) error {
	return gdb.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec(`SET statement_timeout = 0`).Error; err != nil {
			return err
		}
		// Bağlantı havuza açılıştaki ayarıyla dönsün.
		defer conn.Exec(`RESET statement_timeout`)
		if err := conn.Exec(`SELECT pg_advisory_lock(?)`, migrateLockKey).Error; err != nil {
			return err
		}
		defer conn.Exec(`SELECT pg_advisory_unlock(?)`, migrateLockKey)
		return migrate(conn)
	})
}

func migrate(gdb *gorm.DB) error {
	// citext (case-insensitive email) ve gen_random_uuid() için extension'lar.
	if err := gdb.Exec(`CREATE EXTENSION IF NOT EXISTS pgcrypto;`).Error; err != nil {
		return err
	}
	if err := gdb.Exec(`CREATE EXTENSION IF NOT EXISTS citext;`).Error; err != nil {
		return err
	}

	if err := gdb.AutoMigrate(
		&models.User{},
		&models.AuthIdentity{},
		&models.RefreshToken{},
		&models.DictionaryWord{},
		&models.WordProgress{},
		&models.DailyLogin{},
		&models.EarnedBadge{},
		&models.DictionaryPatchApplied{},
		&models.AppliedOp{},
		&models.DailyGroupUnlock{},
		&models.DictionaryTranslation{},
		&models.DictionaryTranslationFile{},
	); err != nil {
		return err
	}
	if err := dropLegacyIndexes(gdb); err != nil {
		return err
	}

	log.Println("db: migrate tamamlandı")
	return nil
}

// legacyIndexes, kurs (course) kolonu eklenmeden önceki index'lerdir. Yerlerini
// kursu içeren yeni adlı index'ler aldı (bkz. models.WordProgress,
// models.DailyGroupUnlock); AutoMigrate aynı adlı var olan bir index'in
// kolonlarını değiştirmediği için yeni adlar gerekti.
//
// Tekil olanlar mutlaka silinmelidir: uq_user_word (user_id, word_id) kaldıkça
// aynı kart ikinci bir kursta öğrenilemez, uq_daily_group_unlock (user_id,
// usage_date, level, grp) kaldıkça aynı grup aynı gün ikinci bir kursta
// açılamaz (her yazım tekillik ihlaliyle düşer). word_progresses'in diğer üçü
// yalnızca gereksizdir (sorgular artık kursla daralır); her yazımda boşuna
// güncellenmesinler diye onlar da silinir.
var legacyIndexes = []string{
	"uq_user_word",
	"idx_wp_user_box",
	"idx_wp_user_level_box",
	"idx_wp_user_box_next",
	"uq_daily_group_unlock",
}

// dropLegacyIndexes, eski index'leri siler.
//
// AutoMigrate'ten SONRA çalışır: course kolonları (NOT NULL DEFAULT 'en'; var
// olan satırlar 'en' olur, PostgreSQL 11+ tabloyu yeniden yazmaz) ve yeni
// tekil index'ler o sırada kurulmuştur. Eski tekil index yenisinden önce
// silinmez; arada tekillik korumasız kalmaz. Var olan satırlar eski anahtarda
// zaten tekil olduğundan (ve hepsi 'en' olduğundan) yeni index veriyle
// çakışamaz.
//
// İdempotenttir (DROP INDEX IF EXISTS). Index'ler mevcut şemada (AutoMigrate'in
// kullandığı current_schema()) aranır: search_path'te sonraki bir şemadaki
// (ör. testlerin geçici şeması açıkken public) aynı adlı index'e dokunulmaz.
func dropLegacyIndexes(gdb *gorm.DB) error {
	var schema string
	if err := gdb.Raw(`SELECT current_schema()`).Scan(&schema).Error; err != nil {
		return fmt.Errorf("şema okunamadı: %w", err)
	}
	for _, name := range legacyIndexes {
		if err := gdb.Exec(`DROP INDEX IF EXISTS ?`, clause.Table{Name: schema + "." + name}).Error; err != nil {
			return fmt.Errorf("eski index %s silinemedi: %w", name, err)
		}
	}
	return nil
}

// ErrSeedFileMissing, sözlük boşken seed dosyasının okunamadığını bildirir.
var ErrSeedFileMissing = errors.New("seed dosyası okunamadı")

// SeedDictionaryIfEmpty, dictionary_words tablosu boşsa verilen SQL seed dosyasını
// yükler ve yüklenen kelime sayısını döner. Production'da imaja gömülü
// /seed/dictionary_words.sql ile ilk veriyi doldurur. Sonraki içerik
// düzeltmeleri bu dosyaya da işlenir ama dolu tabloya ancak sözlük yamalarıyla
// (ApplyDictionaryPatches) ulaşır. Tablo zaten doluysa
// (0, nil) döner; idempotenttir.
//
// Tablo boş kaldığı her durum hatadır (sayım hatası, dosya yok, INSERT/commit
// hatası): sözlüksüz sunucu sağlıklı görünür ama hiçbir kelime gösteremez.
// Lokal geliştirmede (dosya yok, sözlük import aracıyla doldurulur) hata
// ErrSeedFileMissing'i sarar; çağıran ortamına göre karar verir.
func SeedDictionaryIfEmpty(gdb *gorm.DB, seedPath string) (int, error) {
	var count int64
	if err := gdb.Model(&models.DictionaryWord{}).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("sözlük sayılamadı: %w", err)
	}
	if count > 0 {
		return 0, nil
	}
	data, err := os.ReadFile(seedPath)
	if err != nil {
		return 0, fmt.Errorf("dictionary_words boş ve %w (%s: %v)", ErrSeedFileMissing, seedPath, err)
	}
	log.Printf("db: dictionary_words boş, seed yükleniyor (%d byte)...", len(data))

	// pg_dump --column-inserts çıktısı: her INSERT tek satır.
	// Her statement'ı ayrı çalıştır (prepared-statement uyumlu), hepsini tek transaction'da.
	tx := gdb.Begin()
	if tx.Error != nil {
		return 0, fmt.Errorf("seed transaction'ı açılamadı: %w", tx.Error)
	}
	// Binlerce INSERT tek transaction'da: sorgu zaman aşımı yalnızca bu
	// transaction için kapatılır.
	if err := tx.Exec(`SET LOCAL statement_timeout = 0`).Error; err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("seed: statement_timeout kapatılamadı: %w", err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(line), "INSERT INTO") {
			continue
		}
		if err := tx.Exec(line).Error; err != nil {
			tx.Rollback()
			return 0, fmt.Errorf("seed hatası (%d. INSERT): %w", n+1, err)
		}
		n++
	}
	if n == 0 {
		tx.Rollback()
		return 0, fmt.Errorf("seed dosyası %s hiç INSERT içermiyor", seedPath)
	}
	if err := tx.Commit().Error; err != nil {
		return 0, fmt.Errorf("seed commit hatası: %w", err)
	}
	log.Printf("db: seed tamamlandı, %d kelime yüklendi", n)
	return n, nil
}
