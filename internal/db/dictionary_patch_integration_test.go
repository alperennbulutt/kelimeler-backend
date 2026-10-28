package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kelimeler-backend/internal/models"

	"gorm.io/gorm"
)

// Bu dosya gerçek bir PostgreSQL gerektirir; her test isolatedDB ile kendi
// geçici şemasında çalışır (bkz. db_integration_test.go).

const ucKelime = `INSERT INTO dictionary_words (id, english, turkish, example, level, grp, word_order) VALUES ('T_1_1', 'a', 'b', 'c', 'A1', 1, 1);
INSERT INTO dictionary_words (id, english, turkish, example, level, grp, word_order) VALUES ('T_1_2', 'd', 'e', 'f', 'A1', 1, 2);
INSERT INTO dictionary_words (id, english, turkish, example, level, grp, word_order) VALUES ('T_1_3', 'g', 'h', 'i', 'A1', 1, 3);
`

func seededDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb := isolatedDB(t)
	if _, err := SeedDictionaryIfEmpty(gdb, seedFile(t, ucKelime)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return gdb
}

func word(t *testing.T, gdb *gorm.DB, id string) models.DictionaryWord {
	t.Helper()
	var w models.DictionaryWord
	if err := gdb.First(&w, "id = ?", id).Error; err != nil {
		t.Fatalf("%s okunamadı: %v", id, err)
	}
	return w
}

func appliedPatches(t *testing.T, gdb *gorm.DB) []models.DictionaryPatchApplied {
	t.Helper()
	var rows []models.DictionaryPatchApplied
	if err := gdb.Order("name").Find(&rows).Error; err != nil {
		t.Fatalf("dictionary_patches_applied okunamadı: %v", err)
	}
	return rows
}

func setText(t *testing.T, gdb *gorm.DB, id, english string) {
	t.Helper()
	if err := gdb.Exec(`UPDATE dictionary_words SET english = ? WHERE id = ?`, english, id).Error; err != nil {
		t.Fatalf("%s güncellenemedi: %v", id, err)
	}
}

func TestApplyDictionaryPatches_UygulanirVeBirKezUygulanir(t *testing.T) {
	gdb := seededDB(t)
	// Kullanıcı ilerlemesi kartın id'sine bağlıdır: kutu ve tarihler aynen
	// kalır, yalnızca satırdaki metin kopyası yeni metne eşitlenir.
	if err := gdb.Exec(`INSERT INTO users (id, email) VALUES ('00000000-0000-0000-0000-0000000000a1', 'yama@ornek.test')`).Error; err != nil {
		t.Fatalf("kullanıcı: %v", err)
	}
	if err := gdb.Exec(`INSERT INTO word_progresses (user_id, word_id, level, grp, box, wrong_count, english, turkish, next_review_date)
		VALUES ('00000000-0000-0000-0000-0000000000a1', 'T_1_1', 'A1', 1, 3, 2, 'a', 'b', '2026-11-01T00:00:00Z'),
		       ('00000000-0000-0000-0000-0000000000a1', 'T_1_3', 'A1', 1, 4, 0, 'g', 'h', NULL)`).Error; err != nil {
		t.Fatalf("ilerleme: %v", err)
	}

	dir := writePatches(t, map[string]string{
		"0001_ilk.json": tekDegisiklik,
		"0002_ikinci.json": `{"changes": [
		  {"id": "T_1_1", "new": {"english": "a3", "turkish": "b2", "example": "c2"}},
		  {"id": "T_1_2", "new": {"english": "d", "turkish": "e", "example": "f"}}
		]}`,
	})

	rep, err := ApplyDictionaryPatches(gdb, dir)
	if err != nil {
		t.Fatalf("ilk açılış: %v", err)
	}
	if fmt.Sprint(rep.Applied) != "[0001_ilk 0002_ikinci]" || len(rep.Warnings) != 0 {
		t.Fatalf("rapor = %+v, beklenen iki yama sırayla, uyarısız", rep)
	}
	// Yamalar sırayla uygulandı: T_1_1'in son hâli 0002'den.
	if w := word(t, gdb, "T_1_1"); w.English != "a3" || w.Turkish != "b2" || w.Example != "c2" ||
		w.Level != "A1" || w.Grp != 1 || w.WordOrder != 1 {
		t.Errorf("T_1_1 = %+v, beklenen a3/b2/c2 ve aynı seviye/grup/sıra", w)
	}
	rows := appliedPatches(t, gdb)
	if len(rows) != 2 || rows[0].Name != "0001_ilk" || rows[0].Words != 1 || rows[0].Updated != 1 ||
		rows[1].Words != 2 || rows[1].Updated != 1 || len(rows[0].Checksum) != 64 || rows[0].AppliedAt.IsZero() {
		t.Errorf("kayıtlar = %+v", rows)
	}
	if got := dictCount(t, gdb); got != 3 {
		t.Errorf("sözlük = %d kelime, beklenen 3 (yama kart eklemez/silmez)", got)
	}

	// İkinci açılış: yamalar kayıtlı, yeniden uygulanmaz. Kart elle
	// değiştirilmişse de geri yazılmaz.
	setText(t, gdb, "T_1_1", "elle")
	rep, err = ApplyDictionaryPatches(gdb, dir)
	if err != nil || len(rep.Applied) != 0 || len(rep.Warnings) != 0 {
		t.Fatalf("ikinci açılış = (%+v, %v), beklenen boş rapor", rep, err)
	}
	if w := word(t, gdb, "T_1_1"); w.English != "elle" {
		t.Errorf("T_1_1 = %q, yama ikinci kez uygulanmış", w.English)
	}
	if len(appliedPatches(t, gdb)) != 2 {
		t.Error("ikinci açılışta kayıt sayısı değişti")
	}

	var progress []models.WordProgress
	if err := gdb.Order("word_id").Find(&progress).Error; err != nil || len(progress) != 2 {
		t.Fatalf("ilerleme = (%d satır, %v)", len(progress), err)
	}
	p1, p3 := progress[0], progress[1]
	// Metin kopyası yamanın (0002) değerinde; ikinci açılıştaki elle değişiklik
	// yalnızca sözlüğe yazıldı.
	if p1.English != "a3" || p1.Turkish != "b2" {
		t.Errorf("T_1_1 ilerleme metni = %q/%q, beklenen a3/b2", p1.English, p1.Turkish)
	}
	if p1.Box != 3 || p1.WrongCount != 2 || p1.NextReviewDate == nil ||
		!p1.NextReviewDate.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("T_1_1 ilerlemesi değişti: kutu %d, yanlış %d, sonraki tekrar %v", p1.Box, p1.WrongCount, p1.NextReviewDate)
	}
	if p3.English != "g" || p3.Box != 4 {
		t.Errorf("yamada olmayan T_1_3'ün ilerlemesi değişti: %+v", p3)
	}
}

func TestApplyDictionaryPatches_Idempotent(t *testing.T) {
	// Seed yeni değerleri zaten taşıyorsa (yeni kurulum) yama kaydedilir ama
	// hiçbir kartı değiştirmez.
	gdb := seededDB(t)
	setText(t, gdb, "T_1_1", "a2")
	if err := gdb.Exec(`UPDATE dictionary_words SET turkish = 'b2', example = 'c2' WHERE id = 'T_1_1'`).Error; err != nil {
		t.Fatal(err)
	}
	rep, err := ApplyDictionaryPatches(gdb, writePatches(t, map[string]string{"0001_ilk.json": tekDegisiklik}))
	if err != nil || len(rep.Applied) != 1 || len(rep.Warnings) != 0 {
		t.Fatalf("= (%+v, %v), beklenen uygulanmış ve uyarısız", rep, err)
	}
	if rows := appliedPatches(t, gdb); len(rows) != 1 || rows[0].Updated != 0 {
		t.Errorf("kayıt = %+v, beklenen Updated=0", rows)
	}
}

func TestApplyDictionaryPatches_BilinmeyenIDGeriAlinir(t *testing.T) {
	gdb := seededDB(t)
	dir := writePatches(t, map[string]string{
		"0001_bozuk.json": `{"changes": [
		  {"id": "T_1_1", "new": {"english": "x", "turkish": "y", "example": "z"}},
		  {"id": "YOK_1", "new": {"english": "x", "turkish": "y", "example": "z"}},
		  {"id": "YOK_2", "new": {"english": "x", "turkish": "y", "example": "z"}}
		]}`,
		"0002_sonraki.json": `{"changes": [{"id": "T_1_2", "new": {"english": "x", "turkish": "y", "example": "z"}}]}`,
	})

	rep, err := ApplyDictionaryPatches(gdb, dir)
	if !errors.Is(err, ErrPatchUnknownWord) {
		t.Fatalf("hata = %v, beklenen ErrPatchUnknownWord", err)
	}
	for _, want := range []string{"0001_bozuk", "YOK_1", "YOK_2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("hata mesajı %q içermiyor: %v", want, err)
		}
	}
	if len(rep.Applied) != 0 {
		t.Errorf("Applied = %v, beklenen boş", rep.Applied)
	}
	// Yama bütünüyle geri alındı, sonraki yama da uygulanmadı.
	if w := word(t, gdb, "T_1_1"); w.English != "a" {
		t.Errorf("T_1_1 = %q, yarım yama kaldı", w.English)
	}
	if w := word(t, gdb, "T_1_2"); w.English != "d" {
		t.Errorf("T_1_2 = %q, başarısız yamadan sonraki yama uygulanmış", w.English)
	}
	if rows := appliedPatches(t, gdb); len(rows) != 0 {
		t.Errorf("kayıt = %+v, beklenen hiç kayıt", rows)
	}
	if got := dictCount(t, gdb); got != 3 {
		t.Errorf("sözlük = %d kelime, beklenen 3", got)
	}

	// Yama düzeltilince bir sonraki açılış ikisini de uygular.
	if err := os.WriteFile(filepath.Join(dir, "0001_bozuk.json"),
		[]byte(`{"changes": [{"id": "T_1_1", "new": {"english": "x", "turkish": "y", "example": "z"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err = ApplyDictionaryPatches(gdb, dir)
	if err != nil || fmt.Sprint(rep.Applied) != "[0001_bozuk 0002_sonraki]" {
		t.Fatalf("düzeltme sonrası = (%+v, %v)", rep, err)
	}
}

func TestApplyDictionaryPatches_Uyarilar(t *testing.T) {
	gdb := seededDB(t)
	dir := writePatches(t, map[string]string{"0001_ilk.json": tekDegisiklik})

	// "old" ile uyuşmayan kart (elle değiştirilmiş): yama yine uygulanır, uyarı verilir.
	setText(t, gdb, "T_1_1", "elle")
	rep, err := ApplyDictionaryPatches(gdb, dir)
	if err != nil || len(rep.Applied) != 1 || len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "T_1_1") {
		t.Fatalf("= (%+v, %v), beklenen uygulanmış ve T_1_1 için bir uyarı", rep, err)
	}
	if w := word(t, gdb, "T_1_1"); w.English != "a2" {
		t.Errorf("T_1_1 = %q, beklenen a2", w.English)
	}

	// Uygulanmış yamanın dosyası değişirse yeniden uygulanmaz, uyarı verilir.
	if err := os.WriteFile(filepath.Join(dir, "0001_ilk.json"),
		[]byte(`{"changes": [{"id": "T_1_1", "new": {"english": "yeni", "turkish": "y", "example": "z"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err = ApplyDictionaryPatches(gdb, dir)
	if err != nil || len(rep.Applied) != 0 || len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "checksum") {
		t.Fatalf("= (%+v, %v), beklenen checksum uyarısı", rep, err)
	}
	if w := word(t, gdb, "T_1_1"); w.English != "a2" {
		t.Errorf("T_1_1 = %q, değişen dosya yeniden uygulanmış", w.English)
	}
}

func TestApplyDictionaryPatches_BosSozlukteAtlanir(t *testing.T) {
	// Lokalde seed'siz açılış: uygulanacak kart yok, kayıt da yazılmaz ki sözlük
	// dolunca bir sonraki açılış yamayı uygulasın.
	gdb := isolatedDB(t)
	rep, err := ApplyDictionaryPatches(gdb, writePatches(t, map[string]string{"0001_ilk.json": tekDegisiklik}))
	if err != nil || len(rep.Applied) != 0 {
		t.Fatalf("= (%+v, %v), beklenen boş rapor", rep, err)
	}
	if rows := appliedPatches(t, gdb); len(rows) != 0 {
		t.Errorf("kayıt = %+v, beklenen hiç kayıt", rows)
	}
}

func TestApplyDictionaryPatches_EszamanliAcilistaBirKez(t *testing.T) {
	// Aynı anda açılan replikalar: hepsi hatasız biter, yamayı yalnızca biri
	// uygular. Kilit önce tutulur ki hepsi gerçekten aynı anda beklesin; kilit
	// olmasaydı ikisi de yamayı uygulanmamış görür, ikinci kayıt birincil anahtar
	// çakışmasıyla düşerdi.
	gdb := seededDB(t)
	var schema string
	if err := gdb.Raw(`SELECT current_schema()`).Scan(&schema).Error; err != nil {
		t.Fatal(err)
	}
	dir := writePatches(t, map[string]string{"0001_ilk.json": tekDegisiklik})

	const n = 4
	type sonuc struct {
		rep PatchReport
		err error
	}
	results := make(chan sonuc, n)
	err := gdb.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec(`SELECT pg_advisory_lock(?)`, patchLockKey).Error; err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			replika, err := Connect(withParam(t, testDSN(t), "search_path", schema+",public"))
			if err != nil {
				return err
			}
			closeOnCleanup(t, replika)
			go func() {
				rep, err := ApplyDictionaryPatches(replika, dir)
				results <- sonuc{rep, err}
			}()
		}
		var bekleyen int64
		for son := time.Now().Add(10 * time.Second); bekleyen < n && time.Now().Before(son); {
			time.Sleep(20 * time.Millisecond)
			conn.Raw(`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
				AND ((classid::bigint << 32) | objid::bigint) = ?`, patchLockKey).Scan(&bekleyen)
		}
		if err := conn.Exec(`SELECT pg_advisory_unlock(?)`, patchLockKey).Error; err != nil {
			return err
		}
		if bekleyen < n {
			return fmt.Errorf("kilitte bekleyen replika = %d, beklenen %d", bekleyen, n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	uygulayan := 0
	for i := 0; i < n; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Errorf("replika hatası: %v", r.err)
			}
			uygulayan += len(r.rep.Applied)
		case <-time.After(30 * time.Second):
			t.Fatal("replikalar bitmedi")
		}
	}
	if uygulayan != 1 {
		t.Errorf("yamayı uygulayan replika = %d, beklenen 1", uygulayan)
	}
	if rows := appliedPatches(t, gdb); len(rows) != 1 {
		t.Errorf("kayıt = %+v, beklenen 1", rows)
	}
}

// Depodaki gerçek seed ve yamalar: production'daki eski sözlüğe uygulanınca
// her kart yeni değerine gelir, kelime sayısı (health'teki dictionaryWords)
// değişmez; yeni kurulumda seed zaten düzeltilmiş olduğundan hiçbir kart
// değişmez.
func TestGercekSeedVeYamalar(t *testing.T) {
	patches, err := LoadDictionaryPatches(repoPatchesDir)
	if err != nil || len(patches) == 0 {
		t.Fatalf("depodaki yamalar = (%d, %v)", len(patches), err)
	}
	// Yalnızca ilk yamanın "old" değerleri seed'in yayındaki eski hâlidir;
	// sonraki yamalar eklendikçe bu test ilk yamanın eski hâlinden başlar.
	first := patches[0]

	// Döküm tabloyu public şemasıyla nitelendirir; test kendi geçici şemasına
	// yüklesin (aksi halde ortak test veritabanının public tablosu dolardı).
	data, err := os.ReadFile(repoSeedPath)
	if err != nil {
		t.Fatalf("seed okunamadı: %v", err)
	}
	gdb := isolatedDB(t)
	n, err := SeedDictionaryIfEmpty(gdb, seedFile(t,
		strings.ReplaceAll(string(data), "INSERT INTO public.dictionary_words ", "INSERT INTO dictionary_words ")))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	const sozluk = 9980
	if n != sozluk {
		t.Fatalf("seed %d kelime yükledi, beklenen %d", n, sozluk)
	}

	t.Run("yeni kurulumda kart degismez", func(t *testing.T) {
		rep, err := ApplyDictionaryPatches(gdb, repoPatchesDir)
		if err != nil || len(rep.Applied) != len(patches) || len(rep.Warnings) != 0 {
			t.Fatalf("= (%+v, %v)", rep, err)
		}
		for _, r := range appliedPatches(t, gdb) {
			if r.Updated != 0 {
				t.Errorf("%s %d kart değiştirdi; seed yamanın değerlerini taşımıyor", r.Name, r.Updated)
			}
		}
		if got := dictCount(t, gdb); got != sozluk {
			t.Errorf("sözlük = %d, beklenen %d", got, sozluk)
		}
	})

	t.Run("eski sozluge uygulanir", func(t *testing.T) {
		// Production: sözlük ilk yamanın öncesindeki hâlinde, kayıt yok.
		if err := gdb.Exec(`DELETE FROM dictionary_patches_applied`).Error; err != nil {
			t.Fatal(err)
		}
		for _, c := range first.Changes {
			if c.Old == nil {
				t.Fatalf("%s: ilk yamada old yok", c.ID)
			}
			if err := gdb.Exec(`UPDATE dictionary_words SET english = ?, turkish = ?, example = ? WHERE id = ?`,
				c.Old.English, c.Old.Turkish, c.Old.Example, c.ID).Error; err != nil {
				t.Fatal(err)
			}
		}
		rep, err := ApplyDictionaryPatches(gdb, repoPatchesDir)
		if err != nil || len(rep.Applied) != len(patches) || len(rep.Warnings) != 0 {
			t.Fatalf("= (%+v, %v)", rep, err)
		}
		if rows := appliedPatches(t, gdb); rows[0].Updated != len(first.Changes) {
			t.Errorf("%s %d kart değiştirdi, beklenen %d", rows[0].Name, rows[0].Updated, len(first.Changes))
		}
		final := map[string]WordText{}
		for _, p := range patches {
			for _, c := range p.Changes {
				final[c.ID] = c.New
			}
		}
		for id, want := range final {
			w := word(t, gdb, id)
			if got := (WordText{w.English, w.Turkish, w.Example}); got != want {
				t.Errorf("%s = %+v, beklenen %+v", id, got, want)
			}
		}
		if w := word(t, gdb, "A1_2_45"); w.English != "makeup bag" {
			t.Errorf("A1_2_45 = %q, beklenen makeup bag", w.English)
		}
		if got := dictCount(t, gdb); got != sozluk {
			t.Errorf("sözlük = %d, beklenen %d (health dictionaryWords değişmemeli)", got, sozluk)
		}
	})
}
