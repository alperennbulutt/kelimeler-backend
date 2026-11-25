package db

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"kelimeler-backend/internal/dictlang"
)

const repoTranslationsDir = "../../seed/dictionary_translations"

func writeTranslation(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadTranslationFile_Gecerli(t *testing.T) {
	dir := t.TempDir()
	p := writeTranslation(t, dir, "ja.json", `{"lang":"ja","entries":{"A1_1_1":{"w":"行く","r":"いく","e":"毎日学校に行きます。"}}}`)
	f, err := ReadTranslationFile(p)
	if err != nil {
		t.Fatalf("hata: %v", err)
	}
	if f.Lang != "ja" || len(f.Checksum) != 64 || f.Entries["A1_1_1"].R != "いく" {
		t.Errorf("= %+v", f)
	}
}

func TestReadTranslationFile_Gecersizler(t *testing.T) {
	cases := map[string]struct{ name, body, want string }{
		"desteklenmeyen dil":        {"xx.json", `{"lang":"xx","entries":{"A":{"w":"a","e":"b"}}}`, "desteklenmeyen"},
		"lang dosya adından farklı": {"de.json", `{"lang":"fr","entries":{"A":{"w":"a","e":"b"}}}`, "dosya adıyla"},
		"bilinmeyen alan":           {"de.json", `{"lang":"de","entries":{"A":{"w":"a","e":"b","x":"c"}}}`, "geçersiz"},
		"boş dosya":                 {"de.json", `{"lang":"de","entries":{}}`, "boş"},
		"örnek eksik":               {"de.json", `{"lang":"de","entries":{"A":{"w":"a"}}}`, "w ve e"},
		"ja okunuşsuz":              {"ja.json", `{"lang":"ja","entries":{"A":{"w":"行く","e":"行きます。"}}}`, "okunuş"},
		"okunuş yalnızca ja":        {"de.json", `{"lang":"de","entries":{"A":{"w":"a","e":"b","r":"c"}}}`, "yalnızca"},
		"en'de kelime olmaz":        {"en.json", `{"lang":"en","entries":{"A":{"w":"a","m":"a (x)"}}}`, "yalnızca m"},
		"satır sonu":                {"de.json", `{"lang":"de","entries":{"A":{"w":"a\nb","e":"b"}}}`, "satır sonu"},
		"sonda boşluk":              {"de.json", `{"lang":"de","entries":{"A":{"w":"a ","e":"b"}}}`, "boşluk"},
	}
	for ad, c := range cases {
		dir := t.TempDir()
		_, err := ReadTranslationFile(writeTranslation(t, dir, c.name, c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: hata = %v, beklenen %q içeren", ad, err, c.want)
		}
	}
}

func TestLoadTranslationFiles_DizinYoksaBos(t *testing.T) {
	files, err := LoadTranslationFiles(filepath.Join(t.TempDir(), "yok"))
	if err != nil || len(files) != 0 {
		t.Errorf("= (%v, %v)", files, err)
	}
}

func TestLoadTranslationFiles_DilSirasiyla(t *testing.T) {
	dir := t.TempDir()
	writeTranslation(t, dir, "ko.json", `{"lang":"ko","entries":{"A":{"w":"가다","e":"학교에 가요."}}}`)
	writeTranslation(t, dir, "de.json", `{"lang":"de","entries":{"A":{"w":"gehen","e":"Ich gehe."}}}`)
	writeTranslation(t, dir, "OKUBENI.txt", "json değil")
	files, err := LoadTranslationFiles(dir)
	if err != nil || len(files) != 2 || files[0].Lang != "de" || files[1].Lang != "ko" {
		t.Errorf("= (%+v, %v)", files, err)
	}
}

// Depodaki çeviri dosyaları: her dil (en hariç) seed'deki her kartı tam bir
// kez taşır, sözlükte olmayan kart yoktur; tr.json'daki m, kartın yamalar
// uygulanmış turkish değeridir.
func TestDepodakiCeviriler(t *testing.T) {
	files, err := LoadTranslationFiles(repoTranslationsDir)
	if err != nil {
		t.Fatalf("çeviriler okunamadı: %v", err)
	}
	if len(files) == 0 {
		t.Skip("depoda çeviri dosyası yok")
	}
	seed, err := os.ReadFile(repoSeedPath)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`VALUES \('([^']+)'`).FindAllStringSubmatch(string(seed), -1) {
		ids[m[1]] = true
	}
	if len(ids) != 9980 {
		t.Fatalf("seed'de %d kart, beklenen 9980", len(ids))
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f.Lang] = true
		for id := range f.Entries {
			if !ids[id] {
				t.Errorf("%s: sözlükte olmayan kart %s", f.Lang, id)
			}
		}
		if f.Lang != "en" && len(f.Entries) != len(ids) {
			t.Errorf("%s: %d kart, beklenen %d", f.Lang, len(f.Entries), len(ids))
		}
	}
	for _, l := range dictlang.All {
		if !got[l.Code] {
			t.Errorf("%s (%s) için çeviri dosyası yok", l.Code, l.Name)
		}
	}
	if !got["tr"] {
		return
	}
	turkish := currentTurkish(t)
	for _, f := range files {
		if f.Lang != "tr" {
			continue
		}
		for id, e := range f.Entries {
			if e.M != turkish[id] {
				t.Errorf("tr %s: m = %q, kartın turkish değeri %q", id, e.M, turkish[id])
			}
		}
	}
}

// currentTurkish, seed'in yamalar uygulanmış turkish değerleridir.
func currentTurkish(t *testing.T) map[string]string {
	t.Helper()
	seed, err := os.ReadFile(repoSeedPath)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	re := regexp.MustCompile(`VALUES \('((?:[^']|'')*)', '(?:[^']|'')*', '((?:[^']|'')*)'`)
	for _, m := range re.FindAllStringSubmatch(string(seed), -1) {
		out[m[1]] = strings.ReplaceAll(m[2], "''", "'")
	}
	patches, err := LoadDictionaryPatches(repoPatchesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range patches {
		for _, c := range p.Changes {
			out[c.ID] = c.New.Turkish
		}
	}
	return out
}
