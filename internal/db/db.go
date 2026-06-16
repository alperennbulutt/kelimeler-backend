package db

import (
	"log"
	"os"
	"time"

	"kelimeler-backend/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Connect, PostgreSQL'e bağlanır ve *gorm.DB döndürür.
func Connect(dsn string) (*gorm.DB, error) {
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
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

// SeedDictionaryIfEmpty, dictionary_words tablosu boşsa verilen SQL seed dosyasını yükler.
// Production'da imaja gömülü /seed/dictionary_words.sql ile ilk veriyi doldurur;
// dosya yoksa (lokal geliştirme) veya tablo zaten doluysa sessizce atlar. İdempotenttir.
func SeedDictionaryIfEmpty(gdb *gorm.DB, seedPath string) {
	var count int64
	gdb.Model(&models.DictionaryWord{}).Count(&count)
	if count > 0 {
		return
	}
	data, err := os.ReadFile(seedPath)
	if err != nil {
		log.Printf("db: seed atlandı (%s okunamadı: %v)", seedPath, err)
		return
	}
	log.Printf("db: dictionary_words boş, seed yükleniyor (%d byte)...", len(data))
	if err := gdb.Exec(string(data)).Error; err != nil {
		log.Printf("db: seed yükleme hatası: %v", err)
		return
	}
	var newCount int64
	gdb.Model(&models.DictionaryWord{}).Count(&newCount)
	log.Printf("db: seed tamamlandı, %d kelime yüklendi", newCount)
}
