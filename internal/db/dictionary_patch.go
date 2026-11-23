package db

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kelimeler-backend/internal/models"

	"gorm.io/gorm"
)

// Sözlük yamaları, yayındaki sözlüğün kart içeriğini (english, turkish,
// example) id'ye göre düzelten, sürümlü ve bir kez uygulanan dosyalardır:
// seed/dictionary_patches/NNNN_ad.json (imajda /seed/dictionary_patches).
//
// Biçim JSON'dur, SQL değil: yama yalnızca var olan kartların üç metin
// alanını güncelleyebilir ve bu sınır kodla zorlanır (SQL dosyası her şeyi
// yapabilirdi). Bilinmeyen alan, eksik metin, tekrar eden id dosyayı geçersiz
// kılar; "old" ile karttaki değer karşılaştırılıp elle yapılmış değişiklikler
// raporlanır. kind/reason/review_note yalnızca inceleme içindir.
//
//	{
//	  "description": "...",
//	  "changes": [
//	    {"id": "A1_2_45",
//	     "old": {"english": "bag", "turkish": "makyaj çantası", "example": "My makeup bag is pink."},
//	     "new": {"english": "makeup bag", "turkish": "makyaj çantası", "example": "My makeup bag is pink."},
//	     "kind": "bilesik_kelime", "reason": "..."}
//	  ]
//	}
//
// Kartın id'si, seviyesi, grubu ve sırası değişmez; kullanıcıların
// word_progresses satırları o karta bağlı kalır (yalnızca satırdaki
// english/turkish kopyası yeni metne eşitlenir, bkz. syncProgressText).

// patchLockKey, yamaların birden çok replikada aynı anda uygulanmasını
// engelleyen advisory lock anahtarıdır (migrate'inkinden farklı).
const patchLockKey int64 = 0x6b656c2d79616d // "kel-yam"

// patchFileName, yama dosyası adının biçimidir: dört haneli sıra numarası,
// alt çizgi, küçük harf/rakam/alt çizgiden oluşan ad.
var patchFileName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.json$`)

// ErrPatchUnknownWord, yamanın sözlükte olmayan bir id içerdiğini bildirir.
var ErrPatchUnknownWord = errors.New("yama sözlükte olmayan kelime içeriyor")

// WordText, bir kartın yamayla değiştirilebilen metin alanlarıdır.
type WordText struct {
	English string `json:"english"`
	Turkish string `json:"turkish"`
	Example string `json:"example"`
}

// PatchChange, yamadaki tek kartın değişikliğidir.
type PatchChange struct {
	ID string `json:"id"`
	// Old isteğe bağlıdır; verilirse karttaki değer ne Old ne New ise
	// (sözlük elle değiştirilmiş) uyarı verilir, yama yine uygulanır.
	Old *WordText `json:"old,omitempty"`
	New WordText  `json:"new"`

	// Yalnızca inceleme içindir, uygulamada kullanılmaz.
	Kind       string `json:"kind,omitempty"`
	Reason     string `json:"reason,omitempty"`
	ReviewNote string `json:"review_note,omitempty"`
}

// DictionaryPatch, okunmuş ve doğrulanmış bir yama dosyasıdır.
type DictionaryPatch struct {
	Name        string // uzantısız dosya adı, ör. "0001_kritik_ve_a1"
	Checksum    string // dosya içeriğinin SHA-256'sı (hex)
	Description string
	Changes     []PatchChange
}

type patchFile struct {
	Description string        `json:"description"`
	Changes     []PatchChange `json:"changes"`
}

// LoadDictionaryPatches, klasördeki yama dosyalarını sıra numarasına göre
// okur ve doğrular. Klasör yoksa yama da yoktur (nil, nil). .json dışındaki
// dosyalar (ör. README) yok sayılır; adı biçime uymayan .json, aynı sıra
// numaralı iki dosya ya da geçersiz içerik hatadır.
func LoadDictionaryPatches(dir string) ([]DictionaryPatch, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("yama klasörü okunamadı: %w", err)
	}
	// os.ReadDir ada göre sıralı döner; sıra numarası sabit genişlikte
	// olduğundan ad sırası uygulama sırasıdır.
	seen := map[string]string{}
	var patches []DictionaryPatch
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		m := patchFileName.FindStringSubmatch(name)
		if m == nil {
			return nil, fmt.Errorf("yama dosyası adı geçersiz: %s (beklenen NNNN_ad.json; ad küçük harf, rakam ve alt çizgi)", name)
		}
		if prev, ok := seen[m[1]]; ok {
			return nil, fmt.Errorf("aynı sıra numaralı iki yama: %s ve %s", prev, name)
		}
		seen[m[1]] = name
		p, err := readPatch(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("yama %s: %w", name, err)
		}
		patches = append(patches, p)
	}
	return patches, nil
}

// readPatch, tek bir yama dosyasını okur ve doğrular.
func readPatch(path string) (DictionaryPatch, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return DictionaryPatch{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	// Yazım hatalı alan ("turksh") sessizce boş metin olarak uygulanmasın.
	dec.DisallowUnknownFields()
	var f patchFile
	if err := dec.Decode(&f); err != nil {
		return DictionaryPatch{}, fmt.Errorf("JSON okunamadı: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return DictionaryPatch{}, errors.New("JSON nesnesinden sonra fazladan içerik var")
	}
	if len(f.Changes) == 0 {
		return DictionaryPatch{}, errors.New("changes boş")
	}
	ids := map[string]bool{}
	for i, c := range f.Changes {
		if c.ID == "" {
			return DictionaryPatch{}, fmt.Errorf("%d. değişikliğin id'si yok", i+1)
		}
		if ids[c.ID] {
			return DictionaryPatch{}, fmt.Errorf("%s birden çok kez geçiyor", c.ID)
		}
		ids[c.ID] = true
		if err := validText(c.New); err != nil {
			return DictionaryPatch{}, fmt.Errorf("%s new: %w", c.ID, err)
		}
	}
	sum := sha256.Sum256(data)
	return DictionaryPatch{
		Name:        strings.TrimSuffix(filepath.Base(path), ".json"),
		Checksum:    hex.EncodeToString(sum[:]),
		Description: f.Description,
		Changes:     f.Changes,
	}, nil
}

// validText, yeni metinlerin boş olmadığını ve baş/son boşluk taşımadığını
// denetler: kart üç alanı da her zaman gösterir.
func validText(w WordText) error {
	for _, f := range []struct{ name, v string }{
		{"english", w.English}, {"turkish", w.Turkish}, {"example", w.Example},
	} {
		if strings.TrimSpace(f.v) == "" {
			return fmt.Errorf("%s boş", f.name)
		}
		if strings.TrimSpace(f.v) != f.v {
			return fmt.Errorf("%s baş ya da sonda boşluk içeriyor", f.name)
		}
	}
	return nil
}

// PatchReport, ApplyDictionaryPatches'in sonucudur.
type PatchReport struct {
	// Applied, bu çağrıda uygulanan yamaların adlarıdır (sırayla).
	Applied []string
	// Warnings, açılışı durdurmayan ama bakılması gereken durumlardır
	// (uygulanmış yamanın dosyası değişmiş, kart elle değiştirilmiş).
	Warnings []string
}

// ApplyDictionaryPatches, klasördeki henüz uygulanmamış yamaları sırayla
// uygular. Açılışta seed yüklemesinden sonra çağrılır.
//
//   - Her yama tek transaction'dır: kartlar güncellenir, ilerleme
//     satırlarındaki metin kopyaları eşitlenir ve dictionary_patches_applied'a
//     kaydı yazılır; ya hepsi ya hiçbiri.
//   - Uygulanmış yama (kaydı olan) bir daha uygulanmaz; dosyası sonradan
//     değiştiyse yalnızca uyarı verilir.
//   - Yama idempotenttir: değeri zaten yeni olan kart yeniden yazılmaz
//     ve değişen kart sayısına girmez.
//   - Bir yama başarısız olursa (bilinmeyen id, veritabanı hatası) geri alınır,
//     sonraki yamalar da uygulanmaz (onlara dayanıyor olabilirler) ve hata
//     döner. Sözlük son tutarlı hâlinde kalır; bir sonraki açılış yeniden dener.
//   - dictionary_words boşsa (lokalde seed'siz açılış) yamalar atlanır.
//
// Migrate gibi tek bağlantıda, statement_timeout kapalı ve bağlantı ömürlü bir
// advisory lock altında çalışır: aynı anda açılan replikalardan yalnızca biri
// uygular, diğerleri kilidi bekleyip yamayı uygulanmış bulur.
func ApplyDictionaryPatches(gdb *gorm.DB, dir string) (PatchReport, error) {
	var rep PatchReport
	patches, err := LoadDictionaryPatches(dir)
	if err != nil {
		return rep, err
	}
	if len(patches) == 0 {
		log.Printf("db: sözlük yaması yok (%s)", dir)
		return rep, nil
	}
	err = gdb.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec(`SET statement_timeout = 0`).Error; err != nil {
			return err
		}
		// Bağlantı havuza açılıştaki ayarıyla dönsün.
		defer conn.Exec(`RESET statement_timeout`)
		if err := conn.Exec(`SELECT pg_advisory_lock(?)`, patchLockKey).Error; err != nil {
			return err
		}
		defer conn.Exec(`SELECT pg_advisory_unlock(?)`, patchLockKey)
		// Connection'ın verdiği örnekte zincir metotları ortak statement'ı
		// değiştirir (Model, Count'tan sonra Find'a taşınırdı); her sorgu aynı
		// bağlantıda ama temiz bir statement'la başlasın.
		return applyPatches(conn.Session(&gorm.Session{NewDB: true}), patches, &rep)
	})
	return rep, err
}

func applyPatches(conn *gorm.DB, patches []DictionaryPatch, rep *PatchReport) error {
	var count int64
	if err := conn.Model(&models.DictionaryWord{}).Count(&count).Error; err != nil {
		return fmt.Errorf("sözlük sayılamadı: %w", err)
	}
	if count == 0 {
		log.Println("db: dictionary_words boş, sözlük yamaları atlanıyor")
		return nil
	}

	var applied []models.DictionaryPatchApplied
	if err := conn.Find(&applied).Error; err != nil {
		return fmt.Errorf("uygulanmış yamalar okunamadı: %w", err)
	}
	done := make(map[string]string, len(applied))
	for _, a := range applied {
		done[a.Name] = a.Checksum
	}

	for _, p := range patches {
		if sum, ok := done[p.Name]; ok {
			if sum != p.Checksum {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf(
					"sözlük yaması %s uygulandıktan sonra değiştirilmiş (checksum farklı); yeniden uygulanmaz, düzeltme için yeni bir yama ekleyin", p.Name))
			}
			continue
		}
		res, err := applyPatch(conn, p)
		if err != nil {
			return fmt.Errorf("sözlük yaması %s uygulanamadı, geri alındı (sonraki yamalar da uygulanmadı): %w", p.Name, err)
		}
		log.Printf("db: sözlük yaması %s uygulandı (%d kart, %d kart değişti, %d ilerleme satırının metni eşitlendi)",
			p.Name, len(p.Changes), res.updated, res.progress)
		if len(res.drift) > 0 {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf(
				"sözlük yaması %s: %d kartın önceki içeriği yamadaki \"old\" ile uyuşmuyordu (elle değiştirilmiş olabilir), yine de yamadaki değer yazıldı: %s",
				p.Name, len(res.drift), strings.Join(res.drift, ", ")))
		}
		rep.Applied = append(rep.Applied, p.Name)
	}
	return nil
}

// patchResult, tek yamanın uygulama sonucudur.
type patchResult struct {
	updated  int      // içeriği değişen kart sayısı
	progress int64    // metin kopyası eşitlenen word_progresses satırı sayısı
	drift    []string // önceki içeriği "old" ile uyuşmayan kartlar
}

// applyPatch, tek yamayı tek transaction'da uygular.
func applyPatch(conn *gorm.DB, p DictionaryPatch) (res patchResult, err error) {
	err = conn.Transaction(func(tx *gorm.DB) error {
		res = patchResult{}
		ids := make([]string, len(p.Changes))
		for i, c := range p.Changes {
			ids[i] = c.ID
		}
		// Satırlar kilitlenir: okuma ile yazma arasında kart değişmesin.
		var rows []models.DictionaryWord
		if err := tx.Raw(`SELECT id, english, turkish, example FROM dictionary_words WHERE id IN ? FOR UPDATE`, ids).
			Scan(&rows).Error; err != nil {
			return fmt.Errorf("kartlar okunamadı: %w", err)
		}
		current := make(map[string]WordText, len(rows))
		for _, r := range rows {
			current[r.ID] = WordText{English: r.English, Turkish: r.Turkish, Example: r.Example}
		}
		var missing []string
		for _, id := range ids {
			if _, ok := current[id]; !ok {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("%w: %s", ErrPatchUnknownWord, strings.Join(missing, ", "))
		}

		for _, c := range p.Changes {
			now := current[c.ID]
			if now == c.New {
				continue
			}
			if c.Old != nil && now != *c.Old {
				res.drift = append(res.drift, c.ID)
			}
			if err := tx.Exec(`UPDATE dictionary_words SET english = ?, turkish = ?, example = ? WHERE id = ?`,
				c.New.English, c.New.Turkish, c.New.Example, c.ID).Error; err != nil {
				return fmt.Errorf("%s güncellenemedi: %w", c.ID, err)
			}
			res.updated++
		}

		n, err := syncProgressText(tx, p.Changes)
		if err != nil {
			return err
		}
		res.progress = n

		return tx.Create(&models.DictionaryPatchApplied{
			Name:      p.Name,
			Checksum:  p.Checksum,
			Words:     len(p.Changes),
			Updated:   res.updated,
			AppliedAt: time.Now().UTC(),
		}).Error
	})
	return res, err
}

// syncProgressText, word_progresses'teki english/turkish kopyalarını yamanın
// yeni değerlerine eşitler ve değişen satır sayısını döner.
//
// İlerleme satırı kartın metnini kopya olarak taşır (öğrenilen kelimeler ve
// istatistik listeleri onu gösterir); eşitlenmezse kullanıcı düzeltilmiş
// kartın eski metnini görürdü. Kutu, tarihler ve sayaçlar değişmez: ilerleme
// id'ye bağlıdır ve o karta ait kalır. Tek UPDATE ile yapılır: word_id tek
// başına index'li olmadığından kart başına bir UPDATE tabloyu kart sayısı
// kadar tarardı.
func syncProgressText(tx *gorm.DB, changes []PatchChange) (int64, error) {
	values := make([]string, len(changes))
	args := make([]any, 0, 3*len(changes))
	for i, c := range changes {
		values[i] = "(?, ?, ?)"
		args = append(args, c.ID, c.New.English, c.New.Turkish)
	}
	r := tx.Exec(`UPDATE word_progresses AS wp SET english = v.english, turkish = v.turkish
		FROM (VALUES `+strings.Join(values, ", ")+`) AS v(word_id, english, turkish)
		WHERE wp.word_id = v.word_id AND (wp.english <> v.english OR wp.turkish <> v.turkish)`, args...)
	if r.Error != nil {
		return 0, fmt.Errorf("ilerlemelerdeki kart metinleri eşitlenemedi: %w", r.Error)
	}
	return r.RowsAffected, nil
}

// DictionaryVersion, yayındaki sözlüğün içerik sürümüdür: uygulanmış sözlük
// yaması sayısı (dictionary_patches_applied). Çevrimdışı mod istemcisi kart
// önbelleğini bu sayıyla eşler (GET /sync/snapshot?dictionaryVersion=N).
//
// Yamalar bir kez ve sırayla uygulanır, kayıtları silinmez; sözlüğün içeriği
// yayında yalnızca yamayla değiştiği için her içerik değişikliği sürümü bir
// artırır. Seed boş veritabanında yamalardan önce yüklenir ve yamalar seed'in
// üstüne yine uygulanır (değişiklik olmasa da kaydedilir): aynı yama
// kümesindeki iki kurulumun sürümü aynıdır. Sözlüğü yama dışında değiştirmek
// (elle SQL, import-dictionary) sürümü artırmaz; o durumda istemcinin
// önbelleği yenilenmez (bkz. DEPLOYMENT.md).
func DictionaryVersion(gdb *gorm.DB) (int64, error) {
	var n int64
	err := gdb.Model(&models.DictionaryPatchApplied{}).Count(&n).Error
	return n, err
}
