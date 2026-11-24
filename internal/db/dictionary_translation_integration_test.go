package db

import (
	"errors"
	"testing"

	"kelimeler-backend/internal/models"

	"gorm.io/gorm"
)

// Gerçek PostgreSQL gerektirir; her test kendi geçici şemasında (isolatedDB).

func translationRows(t *testing.T, gdb *gorm.DB, lang string) map[string]models.DictionaryTranslation {
	t.Helper()
	var rows []models.DictionaryTranslation
	if err := gdb.Where("lang = ?", lang).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]models.DictionaryTranslation{}
	for _, r := range rows {
		out[r.WordID] = r
	}
	return out
}

func TestApplyDictionaryTranslations_YuklerVeDegismeyeniAtlar(t *testing.T) {
	gdb := seededDB(t)
	dir := t.TempDir()
	writeTranslation(t, dir, "de.json", `{"lang":"de","entries":{"T_1_1":{"w":"gehen","e":"Ich gehe."},"T_1_2":{"w":"die Pause","m":"die Pause (Schule)","e":"Pause!"}}}`)
	writeTranslation(t, dir, "ja.json", `{"lang":"ja","entries":{"T_1_1":{"w":"行く","r":"いく","e":"行きます。"}}}`)

	rep, err := ApplyDictionaryTranslations(gdb, dir)
	if err != nil || len(rep.Loaded) != 2 || len(rep.Unchanged) != 0 {
		t.Fatalf("ilk yükleme = (%+v, %v)", rep, err)
	}
	de := translationRows(t, gdb, "de")
	if len(de) != 2 || de["T_1_2"].Meaning != "die Pause (Schule)" || de["T_1_1"].Word != "gehen" {
		t.Errorf("de = %+v", de)
	}
	if ja := translationRows(t, gdb, "ja"); ja["T_1_1"].Reading != "いく" {
		t.Errorf("ja = %+v", ja)
	}

	rep, err = ApplyDictionaryTranslations(gdb, dir)
	if err != nil || len(rep.Loaded) != 0 || len(rep.Unchanged) != 2 {
		t.Fatalf("ikinci açılış = (%+v, %v)", rep, err)
	}

	// Dosya değişince dilin satırları tümüyle yeniden yazılır (çıkan kart silinir).
	writeTranslation(t, dir, "de.json", `{"lang":"de","entries":{"T_1_1":{"w":"laufen","e":"Ich laufe."}}}`)
	rep, err = ApplyDictionaryTranslations(gdb, dir)
	if err != nil || len(rep.Loaded) != 1 || rep.Loaded[0] != "de" {
		t.Fatalf("değişen dosya = (%+v, %v)", rep, err)
	}
	de = translationRows(t, gdb, "de")
	if len(de) != 1 || de["T_1_1"].Word != "laufen" {
		t.Errorf("yeniden yüklenen de = %+v", de)
	}
}

func TestApplyDictionaryTranslations_BilinmeyenKartGeriAlinirDigerDilYuklenir(t *testing.T) {
	gdb := seededDB(t)
	dir := t.TempDir()
	writeTranslation(t, dir, "de.json", `{"lang":"de","entries":{"T_1_1":{"w":"gehen","e":"Ich gehe."}}}`)
	writeTranslation(t, dir, "fr.json", `{"lang":"fr","entries":{"T_1_1":{"w":"aller","e":"Je vais."},"YOK_1_1":{"w":"x","e":"y"}}}`)

	rep, err := ApplyDictionaryTranslations(gdb, dir)
	if !errors.Is(err, ErrTranslationUnknownWord) {
		t.Fatalf("hata = %v, beklenen ErrTranslationUnknownWord", err)
	}
	if len(rep.Loaded) != 1 || rep.Loaded[0] != "de" {
		t.Errorf("yüklenen = %+v, beklenen yalnızca de", rep)
	}
	if fr := translationRows(t, gdb, "fr"); len(fr) != 0 {
		t.Errorf("fr geri alınmalıydı: %+v", fr)
	}
	var files []models.DictionaryTranslationFile
	gdb.Find(&files)
	if len(files) != 1 || files[0].Lang != "de" {
		t.Errorf("kayıtlar = %+v", files)
	}
}

func TestApplyDictionaryTranslations_BosSozlukteAtlanir(t *testing.T) {
	gdb := isolatedDB(t)
	dir := t.TempDir()
	writeTranslation(t, dir, "de.json", `{"lang":"de","entries":{"T_1_1":{"w":"gehen","e":"Ich gehe."}}}`)
	rep, err := ApplyDictionaryTranslations(gdb, dir)
	if err != nil || len(rep.Loaded) != 0 {
		t.Errorf("= (%+v, %v)", rep, err)
	}
}

func TestTranslationVersion_YuklenenDosyaylaDegisir(t *testing.T) {
	gdb := seededDB(t)
	if v, err := TranslationVersion(gdb, "de", "tr"); err != nil || v != 0 {
		t.Fatalf("yüklü dosya yokken = (%d, %v), beklenen 0", v, err)
	}
	dir := t.TempDir()
	writeTranslation(t, dir, "de.json", `{"lang":"de","entries":{"T_1_1":{"w":"gehen","e":"Ich gehe."}}}`)
	if _, err := ApplyDictionaryTranslations(gdb, dir); err != nil {
		t.Fatal(err)
	}
	v1, err := TranslationVersion(gdb, "de", "tr")
	if err != nil || v1 == 0 {
		t.Fatalf("yüklemeden sonra = (%d, %v)", v1, err)
	}
	// Yeniden yükleme (aynı saniyede olabileceği için an elle ileri alınır).
	if err := gdb.Exec(`UPDATE dictionary_translation_files SET loaded_at = loaded_at + interval '1 hour' WHERE lang = 'de'`).Error; err != nil {
		t.Fatal(err)
	}
	if v2, _ := TranslationVersion(gdb, "de", "tr"); v2 != v1+3600 {
		t.Errorf("yeniden yüklenince = %d, beklenen %d", v2, v1+3600)
	}
	if v, _ := TranslationVersion(gdb, "ja", "tr"); v != 0 {
		t.Errorf("başka çift etkilenmemeli: %d", v)
	}
}
