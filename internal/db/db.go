package db

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"kelimeler-backend/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Connect, PostgreSQL'e bağlanır ve *gorm.DB döndürür.
func Connect(dsn string) (*gorm.DB, error) {
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true, // çoklu-statement seed Exec'i için gerekli
	}), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
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
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(time.Hour)

	return gdb, nil
}

// Migrate, gerekli extension'ları kurar ve tüm modelleri AutoMigrate eder.
func Migrate(gdb *gorm.DB) error {
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
	); err != nil {
		return err
	}

	log.Println("db: migrate tamamlandı")
	return nil
}

// ErrSeedFileMissing, sözlük boşken seed dosyasının okunamadığını bildirir.
var ErrSeedFileMissing = errors.New("seed dosyası okunamadı")

// SeedDictionaryIfEmpty, dictionary_words tablosu boşsa verilen SQL seed dosyasını
// yükler ve yüklenen kelime sayısını döner. Production'da imaja gömülü
// /seed/dictionary_words.sql ile ilk veriyi doldurur. Tablo zaten doluysa
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
