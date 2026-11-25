package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Dil çifti (learn/know) ile kart isteme testleri; gerçek PostgreSQL ister.

func testTranslation(t *testing.T, gdb *gorm.DB, tr models.DictionaryTranslation) {
	t.Helper()
	if err := gdb.Create(&tr).Error; err != nil {
		t.Fatalf("çeviri oluşturulamadı: %v", err)
	}
	t.Cleanup(func() {
		gdb.Delete(&models.DictionaryTranslation{}, "word_id = ? AND lang = ?", tr.WordID, tr.Lang)
	})
}

func pairRequest(t *testing.T, h *Handlers, u *models.User, method, path string, payload any) (int, []byte) {
	t.Helper()
	res, err := authApp(h).Test(authRequest(t, h, u, method, path, payload), -1)
	if err != nil {
		t.Fatalf("istek başarısız: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw
}

type pairCard struct {
	ID      string    `json:"id"`
	English string    `json:"english"`
	Turkish string    `json:"turkish"`
	Learn   *CardSide `json:"learn"`
	Know    *CardSide `json:"know"`
}

func TestWordsPair_GrupVeBatch(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)
	// Grup numarası kullanıcıya özgü olsun ki paylaşılan sözlükle karışmasın.
	grp := 90000 + int(u.CreatedAt.UnixNano()%9000)
	a := testDictWord(t, gdb, "itest_pair_a_"+u.ID, "A1", grp, 1)
	b := testDictWord(t, gdb, "itest_pair_b_"+u.ID, "A1", grp, 2)
	testTranslation(t, gdb, models.DictionaryTranslation{WordID: a.ID, Lang: "ja", Word: "学校", Reading: "がっこう", Example: "学校は大きいです。"})
	testTranslation(t, gdb, models.DictionaryTranslation{WordID: a.ID, Lang: "de", Word: "die Schule", Example: "Die Schule ist groß."})
	testTranslation(t, gdb, models.DictionaryTranslation{WordID: a.ID, Lang: "tr", Word: "okul", Meaning: a.Turkish, Example: "Okul büyük."})
	testTranslation(t, gdb, models.DictionaryTranslation{WordID: a.ID, Lang: "en", Meaning: a.English + " (building)"})
	testTranslation(t, gdb, models.DictionaryTranslation{WordID: b.ID, Lang: "de", Word: "die Pause", Meaning: "die Pause (Schule)", Example: "Wir haben jetzt Pause."})

	status, raw := pairRequest(t, h, u, http.MethodGet, "/api/v1/dictionary/words?level=A1&group="+itoa(grp)+"&learn=ja&know=de", nil)
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (%s)", status, raw)
	}
	var cards []pairCard
	if err := json.Unmarshal(raw, &cards); err != nil || len(cards) != 2 {
		t.Fatalf("yanıt = %s (%v)", raw, err)
	}
	if cards[0].ID != a.ID || cards[0].English != a.English || cards[0].Turkish != a.Turkish {
		t.Errorf("eski alanlar korunmalı: %+v", cards[0])
	}
	if l := cards[0].Learn; l == nil || l.Lang != "ja" || l.Word != "学校" || l.Reading != "がっこう" || l.Example == "" {
		t.Errorf("learn = %+v", l)
	}
	if k := cards[0].Know; k == nil || k.Word != "die Schule" || k.Meaning != "die Schule" {
		t.Errorf("know (anlam notu yoksa kelimenin kendisi) = %+v", k)
	}
	if cards[1].Learn != nil {
		t.Errorf("ja çevirisi olmayan kartta learn null olmalı: %+v", cards[1].Learn)
	}
	if k := cards[1].Know; k == nil || k.Meaning != "die Pause (Schule)" {
		t.Errorf("know anlam notu = %+v", k)
	}

	// İngilizce ve Türkçe dictionary_words'ten gelir; anlam notu ve Türkçe örnek çeviriden.
	status, raw = pairRequest(t, h, u, http.MethodPost, "/api/v1/dictionary/words/batch",
		fiber.Map{"ids": []string{a.ID, b.ID}, "learn": "en", "know": "tr"})
	if status != fiber.StatusOK {
		t.Fatalf("batch status = %d (%s)", status, raw)
	}
	cards = nil
	if err := json.Unmarshal(raw, &cards); err != nil || len(cards) != 2 {
		t.Fatalf("batch yanıtı = %s (%v)", raw, err)
	}
	byID := map[string]pairCard{}
	for _, c := range cards {
		byID[c.ID] = c
	}
	ca, cb := byID[a.ID], byID[b.ID]
	if ca.Learn == nil || ca.Learn.Word != a.English || ca.Learn.Meaning != a.English+" (building)" {
		t.Errorf("en learn = %+v", ca.Learn)
	}
	if ca.Know == nil || ca.Know.Word != "okul" || ca.Know.Meaning != a.Turkish || ca.Know.Example != "Okul büyük." {
		t.Errorf("tr know = %+v", ca.Know)
	}
	// Türkçe çevirisi olmayan kart: kelime ve anlam turkish'ten, örnek boş.
	if cb.Know == nil || cb.Know.Word != b.Turkish || cb.Know.Example != "" {
		t.Errorf("çevirisiz tr know = %+v", cb.Know)
	}

	// Dil çifti verilmezse yanıt eskisi gibidir (learn/know alanı yok).
	status, raw = pairRequest(t, h, u, http.MethodGet, "/api/v1/dictionary/words?level=A1&group="+itoa(grp), nil)
	var plain []map[string]any
	if status != fiber.StatusOK || json.Unmarshal(raw, &plain) != nil || len(plain) != 2 {
		t.Fatalf("eski istek = %d %s", status, raw)
	}
	if _, ok := plain[0]["learn"]; ok {
		t.Errorf("dil çifti istenmeden learn dönmemeli: %s", raw)
	}
}

func TestWordsPair_GecersizDil400(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)
	for _, q := range []string{"learn=de", "know=tr", "learn=xx&know=tr", "learn=tr&know=tr"} {
		status, raw := pairRequest(t, h, u, http.MethodGet, "/api/v1/dictionary/words?level=A1&group=1&"+q, nil)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if status != fiber.StatusBadRequest || body["code"] != "invalid_language" {
			t.Errorf("%s: = %d %s, beklenen 400 invalid_language", q, status, raw)
		}
	}
	status, raw := pairRequest(t, h, u, http.MethodPost, "/api/v1/dictionary/words/batch",
		fiber.Map{"ids": []string{"A1_1_1"}, "learn": "de"})
	if status != fiber.StatusBadRequest {
		t.Errorf("batch tek dil = %d %s, beklenen 400", status, raw)
	}
}

func TestLanguages(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)
	status, raw := pairRequest(t, h, u, http.MethodGet, "/api/v1/dictionary/languages", nil)
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (%s)", status, raw)
	}
	var body struct {
		TotalWords int64 `json:"totalWords"`
		Languages  []struct {
			Code     string `json:"code"`
			Name     string `json:"name"`
			RTL      bool   `json:"rtl"`
			Words    int64  `json:"words"`
			Complete bool   `json:"complete"`
		} `json:"languages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Languages) != 14 {
		t.Fatalf("yanıt = %s (%v)", raw, err)
	}
	for _, l := range body.Languages {
		if l.Name == "" || l.Words > body.TotalWords || l.Complete != (body.TotalWords > 0 && l.Words == body.TotalWords) {
			t.Errorf("dil = %+v (toplam %d)", l, body.TotalWords)
		}
		if l.Code == "en" && l.Words != body.TotalWords {
			t.Errorf("en her kartta vardır: %+v", l)
		}
		if l.RTL != (l.Code == "ar") {
			t.Errorf("rtl = %+v", l)
		}
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
