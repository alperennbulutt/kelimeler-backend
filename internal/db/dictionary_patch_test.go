package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Depodaki seed ve yamaların yolları (testler paket klasöründe çalışır).
const (
	repoSeedPath   = "../../seed/dictionary_words.sql"
	repoPatchesDir = "../../seed/dictionary_patches"
)

// writePatches, verilen adlarla geçici bir yama klasörü kurar.
func writePatches(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("yama yazılamadı: %v", err)
		}
	}
	return dir
}

const tekDegisiklik = `{"description": "deneme", "changes": [
  {"id": "T_1_1", "old": {"english": "a", "turkish": "b", "example": "c"},
   "new": {"english": "a2", "turkish": "b2", "example": "c2"}, "kind": "anlam_duzeltme", "reason": "deneme"}
]}`

func TestLoadDictionaryPatches_SiraVeKlasor(t *testing.T) {
	t.Run("klasor yoksa yama yok", func(t *testing.T) {
		ps, err := LoadDictionaryPatches(filepath.Join(t.TempDir(), "yok"))
		if err != nil || ps != nil {
			t.Fatalf("= (%v, %v), beklenen (nil, nil)", ps, err)
		}
	})

	t.Run("sira numarasina gore ve json disi yok sayilir", func(t *testing.T) {
		dir := writePatches(t, map[string]string{
			"0002_ikinci.json": tekDegisiklik,
			"0001_ilk.json":    tekDegisiklik,
			"README.md":        "yamalar hakkında",
		})
		ps, err := LoadDictionaryPatches(dir)
		if err != nil {
			t.Fatalf("hata: %v", err)
		}
		if len(ps) != 2 || ps[0].Name != "0001_ilk" || ps[1].Name != "0002_ikinci" {
			t.Fatalf("yamalar = %+v, beklenen 0001_ilk, 0002_ikinci", ps)
		}
		if len(ps[0].Checksum) != 64 || ps[0].Checksum != ps[1].Checksum {
			t.Errorf("checksum = %q / %q, aynı içerik için aynı SHA-256 beklenir", ps[0].Checksum, ps[1].Checksum)
		}
		if c := ps[0].Changes[0]; c.ID != "T_1_1" || c.New.English != "a2" || c.Old == nil || c.Old.English != "a" {
			t.Errorf("değişiklik yanlış okundu: %+v", c)
		}
	})
}

func TestLoadDictionaryPatches_GecersizDosyalar(t *testing.T) {
	cases := map[string]map[string]string{
		"ad bicimi":            {"1_kisa.json": tekDegisiklik},
		"buyuk harfli ad":      {"0001_Ilk.json": tekDegisiklik},
		"ayni sira numarasi":   {"0001_a.json": tekDegisiklik, "0001_b.json": tekDegisiklik},
		"bozuk json":           {"0001_a.json": `{"changes": [`},
		"fazladan icerik":      {"0001_a.json": tekDegisiklik + `{}`},
		"bilinmeyen alan":      {"0001_a.json": `{"changes": [{"id": "T_1_1", "new": {"english": "a", "turksh": "b", "example": "c"}}]}`},
		"bos changes":          {"0001_a.json": `{"description": "x", "changes": []}`},
		"id yok":               {"0001_a.json": `{"changes": [{"new": {"english": "a", "turkish": "b", "example": "c"}}]}`},
		"tekrar eden id":       {"0001_a.json": `{"changes": [{"id": "T", "new": {"english": "a", "turkish": "b", "example": "c"}}, {"id": "T", "new": {"english": "a", "turkish": "b", "example": "c"}}]}`},
		"bos metin":            {"0001_a.json": `{"changes": [{"id": "T", "new": {"english": "a", "turkish": " ", "example": "c"}}]}`},
		"eksik metin":          {"0001_a.json": `{"changes": [{"id": "T", "new": {"english": "a", "turkish": "b"}}]}`},
		"bas sonda bosluk":     {"0001_a.json": `{"changes": [{"id": "T", "new": {"english": "a ", "turkish": "b", "example": "c"}}]}`},
		"seviye degistirilmez": {"0001_a.json": `{"changes": [{"id": "T", "new": {"english": "a", "turkish": "b", "example": "c"}, "level": "A2"}]}`},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			if ps, err := LoadDictionaryPatches(writePatches(t, files)); err == nil {
				t.Fatalf("hata dönmedi: %+v", ps)
			}
		})
	}
}

// Depodaki yamalar geçerli olmalı ve seed/dictionary_words.sql her yamanın yeni
// değerlerini zaten taşımalı: yeni kurulan veritabanı doğrudan düzeltilmiş
// sözlükle açılır, yama orada hiçbir kartı değiştirmez. Seed'deki satır
// pg_dump biçimindedir: VALUES ('id', 'english', 'turkish', 'example', ...).
func TestRepoYamalari_SeedIleUyumlu(t *testing.T) {
	patches, err := LoadDictionaryPatches(repoPatchesDir)
	if err != nil {
		t.Fatalf("depodaki yamalar geçersiz: %v", err)
	}
	if len(patches) == 0 {
		t.Fatal("depoda yama bulunamadı")
	}
	data, err := os.ReadFile(repoSeedPath)
	if err != nil {
		t.Fatalf("seed okunamadı: %v", err)
	}
	seed := string(data)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

	// Aynı kart birden çok yamada geçerse seed son yamanın değerini taşır.
	final := map[string]WordText{}
	for _, p := range patches {
		for _, c := range p.Changes {
			final[c.ID] = c.New
		}
	}
	for id, w := range final {
		if !strings.Contains(seed, "VALUES ("+quote(id)+", ") {
			t.Errorf("%s seed'de yok", id)
			continue
		}
		row := "VALUES (" + strings.Join([]string{quote(id), quote(w.English), quote(w.Turkish), quote(w.Example)}, ", ") + ", "
		if !strings.Contains(seed, row) {
			t.Errorf("%s: seed yamanın yeni değerini taşımıyor (beklenen %s)", id, row)
		}
	}
}
