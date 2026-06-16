package main

import (
	"context"
	"log"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/firebase"

	"gorm.io/gorm/clause"
)

// Firebase'deki dictionaryWords koleksiyonunu PostgreSQL'e aktarır.
// İdempotenttir: id üzerinde upsert yapar, tekrar çalıştırılabilir.
//
// Çalıştırma: FIREBASE_SERVICE_ACCOUNT ve FIREBASE_PROJECT_ID ayarlı .env ile
//
//	go run ./cmd/import-dictionary   (veya: make import-dictionary)
func main() {
	cfg := config.Load()

	if cfg.FirebaseServiceAccount == "" || cfg.FirebaseProjectID == "" {
		log.Fatal("FIREBASE_SERVICE_ACCOUNT ve FIREBASE_PROJECT_ID ayarlanmalı (.env)")
	}

	ctx := context.Background()

	log.Printf("Firebase projesi '%s' içinden dictionaryWords okunuyor...", cfg.FirebaseProjectID)
	words, err := firebase.FetchDictionaryWords(ctx, cfg.FirebaseProjectID, cfg.FirebaseServiceAccount)
	if err != nil {
		log.Fatalf("Firebase okuma hatası: %v", err)
	}
	log.Printf("%d kelime okundu", len(words))
	if len(words) == 0 {
		log.Println("Aktarılacak kelime yok, çıkılıyor.")
		return
	}

	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db bağlantısı başarısız: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		log.Fatalf("db migrate başarısız: %v", err)
	}

	const batchSize = 500
	upserted := 0
	for i := 0; i < len(words); i += batchSize {
		end := i + batchSize
		if end > len(words) {
			end = len(words)
		}
		batch := words[i:end]
		if err := database.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"english", "turkish", "example", "level", "grp", "word_order",
			}),
		}).Create(&batch).Error; err != nil {
			log.Fatalf("upsert hatası (batch %d): %v", i, err)
		}
		upserted += len(batch)
		log.Printf("  %d/%d aktarıldı", upserted, len(words))
	}

	log.Printf("✅ Tamamlandı: %d kelime PostgreSQL'e aktarıldı (idempotent upsert)", upserted)
}
