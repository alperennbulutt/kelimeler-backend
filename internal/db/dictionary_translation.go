package db

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kelimeler-backend/internal/dictlang"
	"kelimeler-backend/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Çeviri dosyaları, sözlük kartlarının (kavramların) diğer dillerdeki
// metnidir: seed/dictionary_translations/<dil>.json (imajda
// /seed/dictionary_translations). Kullanıcı 14 dilden birini bildiği, bir
// başkasını öğrendiği dil olarak seçer; kart id'si, seviyesi, grubu ve sırası
// her dil çiftinde aynıdır.
//
//	{
//	  "lang": "de",
//	  "entries": {
//	    "A1_1_1": {"w": "gehen", "e": "Ich gehe jeden Tag zur Schule."},
//	    "A1_10_16": {"w": "die Pause", "e": "...", "m": "die Pause (in der Schule)"}
//	  }
//	}
//
//	w: o dilde öğrenilecek yalın kelime   e: örnek cümle
//	m: anlam notu (isteğe bağlı; o dili bilene anlam olarak gösterilir)
//	r: okunuş (yalnızca ja, zorunlu: hiragana)
//
// İngilizcenin kelimesi ve örneği dictionary_words'tedir; en.json yalnızca
// anlam notu gereken kartları {"m": ...} olarak taşır. tr.json'da m, kartın
// dictionary_words.turkish değeridir (bilgi amaçlı; API turkish'i kullanır).
//
// Dosya, içeriği değiştiyse (SHA-256) açılışta o dilin satırlarını tümüyle
// yeniden yazar; değişmediyse hiçbir şey yapılmaz. Böylece çeviri düzeltmesi
// dosyayı düzenleyip deploy etmekle yayına çıkar.

// translationLockKey, çevirilerin birden çok replikada aynı anda yüklenmesini
// engelleyen advisory lock anahtarıdır (migrate ve yamalarınkinden farklı).
const translationLockKey int64 = 0x6b656c2d636576 // "kel-cev"

// ErrTranslationUnknownWord, çeviri dosyasının sözlükte olmayan kart içerdiğini bildirir.
var ErrTranslationUnknownWord = errors.New("çeviri dosyası sözlükte olmayan kart içeriyor")

// TranslationEntry, tek kartın bir dildeki metnidir (dosyadaki biçim).
type TranslationEntry struct {
	W string `json:"w,omitempty"`
	M string `json:"m,omitempty"`
	E string `json:"e,omitempty"`
	R string `json:"r,omitempty"`
}

// TranslationFile, okunmuş ve doğrulanmış bir çeviri dosyasıdır.
type TranslationFile struct {
	Lang     string
	Checksum string
	Entries  map[string]TranslationEntry
}

// TranslationReport, ApplyDictionaryTranslations'ın sonucudur.
type TranslationReport struct {
	Loaded    []string // yeniden yüklenen diller
	Unchanged []string // içeriği değişmediği için atlanan diller
}

// LoadTranslationFiles, dizindeki <dil>.json dosyalarını dil sırasıyla okur ve
// doğrular. Dizin yoksa boş liste döner; desteklenmeyen dil adlı ya da
// geçersiz dosya hatadır.
func LoadTranslationFiles(dir string) ([]TranslationFile, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("çeviri dizini okunamadı: %w", err)
	}
	var files []TranslationFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		f, err := ReadTranslationFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	order := map[string]int{}
	for i, l := range dictlang.All {
		order[l.Code] = i
	}
	sort.Slice(files, func(i, j int) bool { return order[files[i].Lang] < order[files[j].Lang] })
	return files, nil
}

// ReadTranslationFile, tek çeviri dosyasını okur ve doğrular.
func ReadTranslationFile(path string) (TranslationFile, error) {
	name := filepath.Base(path)
	lang := strings.TrimSuffix(name, ".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return TranslationFile{}, fmt.Errorf("çeviri dosyası %s okunamadı: %w", name, err)
	}
	if !dictlang.Supported(lang) {
		return TranslationFile{}, fmt.Errorf("çeviri dosyası %s: desteklenmeyen dil %q", name, lang)
	}
	var raw struct {
		Lang    string                      `json:"lang"`
		Entries map[string]TranslationEntry `json:"entries"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return TranslationFile{}, fmt.Errorf("çeviri dosyası %s geçersiz: %w", name, err)
	}
	if raw.Lang != lang {
		return TranslationFile{}, fmt.Errorf("çeviri dosyası %s: lang = %q, dosya adıyla aynı olmalı", name, raw.Lang)
	}
	if len(raw.Entries) == 0 && lang != "en" {
		return TranslationFile{}, fmt.Errorf("çeviri dosyası %s boş", name)
	}
	ids := make([]string, 0, len(raw.Entries))
	for id := range raw.Entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := validEntry(lang, raw.Entries[id]); err != nil {
			return TranslationFile{}, fmt.Errorf("çeviri dosyası %s, kart %s: %w", name, id, err)
		}
	}
	sum := sha256.Sum256(data)
	return TranslationFile{Lang: lang, Checksum: hex.EncodeToString(sum[:]), Entries: raw.Entries}, nil
}

// validEntry, bir kartın metin alanlarını dile göre denetler.
func validEntry(lang string, e TranslationEntry) error {
	for f, v := range map[string]string{"w": e.W, "m": e.M, "e": e.E, "r": e.R} {
		if v != strings.TrimSpace(v) || strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("%s başta/sonda boşluk ya da satır sonu içeriyor", f)
		}
	}
	if lang == "en" {
		if e.M == "" || e.W != "" || e.E != "" || e.R != "" {
			return errors.New("en kaydında yalnızca m olmalı (kelime ve örnek dictionary_words'tedir)")
		}
		return nil
	}
	if e.W == "" || e.E == "" {
		return errors.New("w ve e zorunlu")
	}
	if dictlang.NeedsReading(lang) && e.R == "" {
		return errors.New("r (okunuş) zorunlu")
	}
	if !dictlang.NeedsReading(lang) && e.R != "" {
		return fmt.Errorf("r yalnızca okunuş gereken dillerde olur, %s için değil", lang)
	}
	return nil
}

// ApplyDictionaryTranslations, dizindeki çeviri dosyalarını veritabanına
// yükler.
//
//   - İçeriği (checksum) dictionary_translation_files'taki kayıtla aynı olan
//     dil atlanır; değişen ya da yeni dil tek transaction'da tümüyle yeniden
//     yazılır (satırlar silinip eklenir, kayıt güncellenir).
//   - Sözlükte olmayan kart içeren dosya geri alınır ve hata döner; diğer
//     diller yine yüklenir (birbirlerinden bağımsızdırlar).
//   - dictionary_words boşsa (lokalde seed'siz açılış) atlanır.
//
// Yamalar gibi tek bağlantıda, statement_timeout kapalı ve advisory lock
// altında çalışır: aynı anda açılan replikalardan yalnızca biri yükler.
func ApplyDictionaryTranslations(gdb *gorm.DB, dir string) (TranslationReport, error) {
	var rep TranslationReport
	files, err := LoadTranslationFiles(dir)
	if err != nil {
		return rep, err
	}
	if len(files) == 0 {
		log.Printf("db: çeviri dosyası yok (%s)", dir)
		return rep, nil
	}
	err = gdb.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec(`SET statement_timeout = 0`).Error; err != nil {
			return err
		}
		defer conn.Exec(`RESET statement_timeout`)
		if err := conn.Exec(`SELECT pg_advisory_lock(?)`, translationLockKey).Error; err != nil {
			return err
		}
		defer conn.Exec(`SELECT pg_advisory_unlock(?)`, translationLockKey)
		return applyTranslations(conn.Session(&gorm.Session{NewDB: true}), files, &rep)
	})
	return rep, err
}

func applyTranslations(conn *gorm.DB, files []TranslationFile, rep *TranslationReport) error {
	var count int64
	if err := conn.Model(&models.DictionaryWord{}).Count(&count).Error; err != nil {
		return fmt.Errorf("sözlük sayılamadı: %w", err)
	}
	if count == 0 {
		log.Println("db: dictionary_words boş, çeviriler atlanıyor")
		return nil
	}
	var loaded []models.DictionaryTranslationFile
	if err := conn.Find(&loaded).Error; err != nil {
		return fmt.Errorf("yüklenmiş çeviriler okunamadı: %w", err)
	}
	done := make(map[string]string, len(loaded))
	for _, l := range loaded {
		done[l.Lang] = l.Checksum
	}

	var errs []error
	for _, f := range files {
		if done[f.Lang] == f.Checksum {
			rep.Unchanged = append(rep.Unchanged, f.Lang)
			continue
		}
		if err := loadTranslation(conn, f); err != nil {
			errs = append(errs, fmt.Errorf("çeviri %s yüklenemedi, geri alındı: %w", f.Lang, err))
			continue
		}
		log.Printf("db: çeviri %s yüklendi (%d kart)", f.Lang, len(f.Entries))
		rep.Loaded = append(rep.Loaded, f.Lang)
	}
	return errors.Join(errs...)
}

// translationBatch, tek INSERT'teki satır sayısıdır (PreferSimpleProtocol ile
// değerler sorgu metnine gömülür; çok büyük sorgu üretmesin).
const translationBatch = 1000

// loadTranslation, tek dilin satırlarını tek transaction'da yeniden yazar.
func loadTranslation(conn *gorm.DB, f TranslationFile) error {
	ids := make([]string, 0, len(f.Entries))
	for id := range f.Entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows := make([]models.DictionaryTranslation, len(ids))
	for i, id := range ids {
		e := f.Entries[id]
		rows[i] = models.DictionaryTranslation{WordID: id, Lang: f.Lang, Word: e.W, Meaning: e.M, Example: e.E, Reading: e.R}
	}
	return conn.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("lang = ?", f.Lang).Delete(&models.DictionaryTranslation{}).Error; err != nil {
			return fmt.Errorf("eski satırlar silinemedi: %w", err)
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, translationBatch).Error; err != nil {
				return fmt.Errorf("satırlar yazılamadı: %w", err)
			}
		}
		var unknown []string
		if err := tx.Raw(`SELECT t.word_id FROM dictionary_translations t
			WHERE t.lang = ? AND NOT EXISTS (SELECT 1 FROM dictionary_words w WHERE w.id = t.word_id)
			ORDER BY t.word_id LIMIT 20`, f.Lang).Scan(&unknown).Error; err != nil {
			return fmt.Errorf("kartlar denetlenemedi: %w", err)
		}
		if len(unknown) > 0 {
			return fmt.Errorf("%w: %s", ErrTranslationUnknownWord, strings.Join(unknown, ", "))
		}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&models.DictionaryTranslationFile{
			Lang: f.Lang, Checksum: f.Checksum, Entries: len(rows), LoadedAt: time.Now().UTC(),
		}).Error
	})
}

// TranslationVersion, verilen dillerin yüklü çeviri dosyalarından türetilen
// sürüm katkısıdır: dosyaların yüklenme anlarının (Unix saniye) toplamı.
// Dosya değişip yeniden yüklendikçe değişir; hiçbiri yüklü değilse 0'dır.
// /sync/snapshot, kartlara dil çifti eklendiğinde bunu sözlük sürümüne ekler.
func TranslationVersion(gdb *gorm.DB, langs ...string) (int64, error) {
	var files []models.DictionaryTranslationFile
	if err := gdb.Where("lang IN ?", langs).Find(&files).Error; err != nil {
		return 0, err
	}
	var v int64
	for _, f := range files {
		v += f.LoadedAt.Unix()
	}
	return v, nil
}
