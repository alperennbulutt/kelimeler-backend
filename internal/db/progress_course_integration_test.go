package db

import (
	"slices"
	"testing"
	"time"

	"kelimeler-backend/internal/models"

	"gorm.io/gorm"
)

// progressIndexes, mevcut şemadaki word_progresses index'lerinin adlarıdır
// (sıralı).
func progressIndexes(t *testing.T, gdb *gorm.DB) []string {
	t.Helper()
	var names []string
	if err := gdb.Raw(`SELECT indexname FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'word_progresses'
		ORDER BY indexname`).Scan(&names).Error; err != nil {
		t.Fatalf("index'ler okunamadı: %v", err)
	}
	return names
}

// kursOncesiTablo, word_progresses'i kurs kolonu eklenmeden önceki hâline
// döndürür: course kolonu (ve onu içeren index'ler) yok, eski index'ler
// eski adlarıyla var.
func kursOncesiTablo(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	for _, q := range []string{
		`ALTER TABLE word_progresses DROP COLUMN course`,
		`CREATE UNIQUE INDEX uq_user_word ON word_progresses (user_id, word_id)`,
		`CREATE INDEX idx_wp_user_box ON word_progresses (user_id, box)`,
		`CREATE INDEX idx_wp_user_level_box ON word_progresses (user_id, level, box)`,
		`CREATE INDEX idx_wp_user_box_next ON word_progresses (user_id, box, next_review_date)`,
	} {
		if err := gdb.Exec(q).Error; err != nil {
			t.Fatalf("eski şema kurulamadı (%s): %v", q, err)
		}
	}
}

// Kurs öncesi şemada satırları olan tablo migrate edilince satırlar İngilizce
// kursunda kalır, eski index'ler silinir, yeni tekil anahtar (user_id,
// course, word_id) kurulur. Migrate tekrar çalıştırılabilir.
func TestMigrate_KursOncesiTabloKursaGecer(t *testing.T) {
	gdb := isolatedDB(t)
	kursOncesiTablo(t, gdb)

	want := []string{"idx_wp_user_box", "idx_wp_user_box_next", "idx_wp_user_level_box", "uq_user_word", "word_progresses_pkey"}
	if got := progressIndexes(t, gdb); !slices.Equal(got, want) {
		t.Fatalf("eski şemanın index'leri = %v, beklenen %v", got, want)
	}

	u := models.User{Email: "kurs-migrate-" + time.Now().Format("150405.000000000") + "@example.test", DisplayName: "T"}
	if err := gdb.Create(&u).Error; err != nil {
		t.Fatalf("kullanıcı oluşturulamadı: %v", err)
	}
	// course kolonu henüz yok: satırlar elle, eski kolonlarla yazılır.
	for i, w := range []string{"A1_1_1", "A1_1_2", "A1_1_3"} {
		if err := gdb.Exec(`INSERT INTO word_progresses (user_id, word_id, level, grp, box, wrong_count, learned, english, turkish)
			VALUES (?, ?, 'A1', 1, ?, 0, false, '', '')`, u.ID, w, i+2).Error; err != nil {
			t.Fatalf("eski satır yazılamadı: %v", err)
		}
	}
	// Eski tekil anahtar çalışıyor.
	if err := gdb.Exec(`INSERT INTO word_progresses (user_id, word_id, level, grp, box, wrong_count, learned, english, turkish)
		VALUES (?, 'A1_1_1', 'A1', 1, 2, 0, false, '', '')`, u.ID).Error; err == nil {
		t.Fatal("eski şemada aynı kelime ikinci kez yazılabildi")
	}

	for i := 0; i < 2; i++ { // ikinci çalıştırma: idempotent
		if err := Migrate(gdb); err != nil {
			t.Fatalf("%d. migrate başarısız: %v", i+1, err)
		}
	}

	want = []string{"idx_wp_user_course_box", "idx_wp_user_course_box_next", "idx_wp_user_course_level_box", "uq_wp_user_course_word", "word_progresses_pkey"}
	if got := progressIndexes(t, gdb); !slices.Equal(got, want) {
		t.Fatalf("migrate sonrası index'ler = %v, beklenen %v", got, want)
	}

	var rows []models.WordProgress
	if err := gdb.Where("user_id = ?", u.ID).Order("word_id").Find(&rows).Error; err != nil {
		t.Fatalf("satırlar okunamadı: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("satır sayısı = %d, beklenen 3", len(rows))
	}
	for i, r := range rows {
		if r.Course != "en" || r.Box != i+2 {
			t.Errorf("%s: course=%q box=%d, beklenen course=\"en\" box=%d", r.WordID, r.Course, r.Box, i+2)
		}
	}

	// Aynı kelime başka bir kursta ayrı bir satırdır; aynı kursta ikinci
	// satır yeni tekil anahtara takılır.
	de := models.WordProgress{UserID: u.ID, Course: "de", WordID: "A1_1_1", Level: "A1", Grp: 1, Box: 2}
	if err := gdb.Create(&de).Error; err != nil {
		t.Fatalf("aynı kelime de kursunda yazılamadı: %v", err)
	}
	if err := gdb.Create(&models.WordProgress{UserID: u.ID, Course: "en", WordID: "A1_1_1", Level: "A1", Grp: 1, Box: 2}).Error; err == nil {
		t.Fatal("aynı kursta aynı kelime ikinci kez yazılabildi")
	}
	// Kurs verilmeyen yazım İngilizce kursudur (kolon varsayılanı).
	var plain models.WordProgress
	if err := gdb.Exec(`INSERT INTO word_progresses (user_id, word_id, level, grp, box, wrong_count, learned, english, turkish)
		VALUES (?, 'A1_1_9', 'A1', 1, 2, 0, false, '', '')`, u.ID).Error; err != nil {
		t.Fatalf("kurssuz satır yazılamadı: %v", err)
	}
	if err := gdb.Where("user_id = ? AND word_id = ?", u.ID, "A1_1_9").First(&plain).Error; err != nil || plain.Course != "en" {
		t.Fatalf("kurssuz satır = (%q, %v), beklenen course \"en\"", plain.Course, err)
	}
}

// Eski index'ler yalnızca mevcut şemada silinir: search_path'te sonraki bir
// şemadaki aynı adlı index'e dokunulmaz.
func TestMigrate_EskiIndexYalnizcaMevcutSemadaSilinir(t *testing.T) {
	gdb := isolatedDB(t)
	// Geçici şemanın arkasındaki (search_path: geçici, public) bir şemada
	// eski adlı index'i taşıyan bir tablo.
	other := "dbtest_diger_" + time.Now().Format("150405000000000")
	for _, q := range []string{
		`CREATE SCHEMA ` + other,
		`CREATE TABLE ` + other + `.t (a int)`,
		`CREATE INDEX uq_user_word ON ` + other + `.t (a)`,
	} {
		if err := gdb.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() { gdb.Exec(`DROP SCHEMA IF EXISTS ` + other + ` CASCADE`) })

	var schema string
	if err := gdb.Raw(`SELECT current_schema()`).Scan(&schema).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec(`SET search_path TO ` + schema + `, ` + other + `, public`).Error; err != nil {
			return err
		}
		return dropLegacyIndexes(conn)
	}); err != nil {
		t.Fatalf("dropLegacyIndexes: %v", err)
	}

	var n int64
	if err := gdb.Raw(`SELECT COUNT(*) FROM pg_indexes WHERE schemaname = ? AND indexname = 'uq_user_word'`, other).
		Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("başka şemadaki uq_user_word silindi")
	}
}

// daily_group_unlocks'un kurs öncesi tekil index'i (user_id, usage_date,
// level, grp) silinir; var olan satırlar İngilizce kursunda kalır ve aynı grup
// aynı gün başka bir kursta da açılabilir.
func TestMigrate_GunlukAcilislarKursaGecer(t *testing.T) {
	gdb := isolatedDB(t)
	for _, q := range []string{
		`ALTER TABLE daily_group_unlocks DROP COLUMN course`,
		`CREATE UNIQUE INDEX uq_daily_group_unlock ON daily_group_unlocks (user_id, usage_date, level, grp)`,
	} {
		if err := gdb.Exec(q).Error; err != nil {
			t.Fatalf("eski şema kurulamadı (%s): %v", q, err)
		}
	}
	u := models.User{Email: "kurs-acilis-" + time.Now().Format("150405.000000000") + "@example.test", DisplayName: "T"}
	if err := gdb.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	insert := func(course string) error {
		cols, vals := "", ""
		args := []any{u.ID}
		if course != "" {
			cols, vals = ", course", ", ?"
			args = append(args, course)
		}
		return gdb.Exec(`INSERT INTO daily_group_unlocks (user_id, usage_date, level, grp, via, created_at`+cols+`)
			VALUES (?, CAST('2026-01-02' AS date), 'A1', 3, 'free', now()`+vals+`)`, args...).Error
	}
	if err := insert(""); err != nil {
		t.Fatalf("eski satır yazılamadı: %v", err)
	}

	if err := Migrate(gdb); err != nil {
		t.Fatalf("migrate başarısız: %v", err)
	}
	var names []string
	gdb.Raw(`SELECT indexname FROM pg_indexes WHERE schemaname = current_schema()
		AND tablename = 'daily_group_unlocks' AND indexname LIKE 'uq_%' ORDER BY indexname`).Scan(&names)
	if !slices.Equal(names, []string{"uq_daily_group_unlock_course"}) {
		t.Fatalf("tekil index'ler = %v, beklenen [uq_daily_group_unlock_course]", names)
	}
	var course string
	if err := gdb.Raw(`SELECT course FROM daily_group_unlocks WHERE user_id = ?`, u.ID).Scan(&course).Error; err != nil || course != "en" {
		t.Fatalf("eski satırın kursu = %q (%v), beklenen en", course, err)
	}
	if err := insert("de"); err != nil {
		t.Fatalf("aynı grup aynı gün de kursunda açılamadı: %v", err)
	}
	if err := insert("en"); err == nil {
		t.Fatal("aynı grup aynı gün aynı kursta ikinci kez yazılabildi")
	}
}
