package handlers

import (
	"fmt"
	"strconv"

	"kelimeler-backend/internal/dictlang"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
)

// GetWordsForGroup: GET /dictionary/words?level=A1&group=3[&learn=de&know=tr]
// dictionary_repository.getWordsForGroup karşılığı (order'a göre sıralı).
// learn/know verilirse her karta o dil çiftinin metni eklenir (bkz. withPair).
func (h *Handlers) GetWordsForGroup(c *fiber.Ctx) error {
	level := c.Query("level")
	group, _ := strconv.Atoi(c.Query("group"))
	p, err := parsePair(c.Query("learn"), c.Query("know"))
	if err != nil {
		return err
	}

	var words []models.DictionaryWord
	if err := h.DB.Where("level = ? AND grp = ?", level, group).
		Order("word_order").Find(&words).Error; err != nil {
		return err
	}
	if p == nil {
		return c.JSON(words)
	}
	cards, err := h.withPair(words, *p)
	if err != nil {
		return err
	}
	return c.JSON(cards)
}

// maxBatchIDs, POST /dictionary/words/batch'in tek istekte kabul ettiği en
// fazla (tekil, boş olmayan) id sayısıdır. İstemci bir tekrar batch'i kadar
// (reviewBatchSize = 50) gönderir; sınır ona geniş pay bırakır. Sınırsız
// liste, 1 MiB'lık gövdeyle ~100 bin elemanlı bir IN sorgusu demekti
// (PreferSimpleProtocol ile hepsi sorgu metnine gömülür).
const maxBatchIDs = 500

// GetWordsByIds: POST /dictionary/words/batch {ids:[...], learn?, know?}
// dictionary_repository.getWordsByIds karşılığı (Postgres IN — chunk gerekmez).
//
// Boş id'ler yok sayılır, tekrarlar bir kez sorgulanır (istemci yanıtı id'ye
// göre eşler; sıra garanti edilmez). Tekil id sayısı maxBatchIDs'i aşarsa
// 400 invalid_request döner.
func (h *Handlers) GetWordsByIds(c *fiber.Ctx) error {
	var req struct {
		IDs   []string `json:"ids"`
		Learn string   `json:"learn"`
		Know  string   `json:"know"`
	}
	if err := c.BodyParser(&req); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid_request", "ids gerekli")
	}
	p, err := parsePair(req.Learn, req.Know)
	if err != nil {
		return err
	}
	ids, ok := uniqueBatchIDs(req.IDs, maxBatchIDs)
	if !ok {
		return apiErr(fiber.StatusBadRequest, "invalid_request",
			fmt.Sprintf("tek istekte en fazla %d id gönderilebilir", maxBatchIDs))
	}
	if len(ids) == 0 {
		return c.JSON([]models.DictionaryWord{})
	}
	var words []models.DictionaryWord
	if err := h.DB.Where("id IN ?", ids).Find(&words).Error; err != nil {
		return err
	}
	if p == nil {
		return c.JSON(words)
	}
	cards, err := h.withPair(words, *p)
	if err != nil {
		return err
	}
	return c.JSON(cards)
}

// uniqueBatchIDs, boş id'leri atar ve tekrarları ilk görülme sırasıyla tekile
// indirir. Tekil id sayısı max'ı aşarsa ok=false döner.
func uniqueBatchIDs(raw []string, max int) (ids []string, ok bool) {
	seen := make(map[string]struct{}, min(len(raw), max+1))
	ids = make([]string, 0, min(len(raw), max))
	for _, id := range raw {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		if len(ids) == max {
			return nil, false
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, true
}

// languagePair, kartın hangi dilden hangi dile sunulacağıdır.
type languagePair struct {
	Learn string // öğrenilen dil: kelime ve örnek bu dilde
	Know  string // bilinen dil: anlam bu dilde
}

// parsePair, learn/know parametrelerini okur. İkisi de boşsa nil döner (eski
// istemci: yalnızca english/turkish). Biri eksikse, dil desteklenmiyorsa ya da
// ikisi aynıysa 400 invalid_language döner.
func parsePair(learn, know string) (*languagePair, error) {
	if learn == "" && know == "" {
		return nil, nil
	}
	if learn == "" || know == "" {
		return nil, apiErr(fiber.StatusBadRequest, "invalid_language", "learn ve know birlikte verilmeli")
	}
	for _, l := range []string{learn, know} {
		if !dictlang.Supported(l) {
			return nil, apiErr(fiber.StatusBadRequest, "invalid_language", fmt.Sprintf("desteklenmeyen dil: %q", l))
		}
	}
	if learn == know {
		return nil, apiErr(fiber.StatusBadRequest, "invalid_language", "learn ve know farklı olmalı")
	}
	return &languagePair{Learn: learn, Know: know}, nil
}

// CardSide, kartın bir dildeki yüzüdür.
type CardSide struct {
	Lang string `json:"lang"`
	// Word, o dildeki yalın kelimedir (öğrenilen dilde ezberlenecek olan).
	Word string `json:"word"`
	// Meaning, kart o dili bilene gösterilirken anlam olarak kullanılacak
	// metindir: gerekiyorsa kısa anlam notuyla, değilse Word'ün aynısı.
	Meaning string `json:"meaning"`
	// Reading, okunuştur (Japonca: hiragana); yoksa alan gönderilmez.
	Reading string `json:"reading,omitempty"`
	// Example, o dilde örnek cümledir (bazı eski kartlarda boş olabilir).
	Example string `json:"example"`
}

// wordCard, dil çifti istenince dönen karttır: eski alanların hepsi (english,
// turkish, example, level, group, order...) aynen durur, yanına learn ve know
// eklenir. Kartın o dilde metni yoksa ilgili alan null'dur.
type wordCard struct {
	models.DictionaryWord
	Learn *CardSide `json:"learn"`
	Know  *CardSide `json:"know"`
}

// withPair, kartlara dil çiftinin metnini ekler (tek sorguyla).
func (h *Handlers) withPair(words []models.DictionaryWord, p languagePair) ([]wordCard, error) {
	cards := make([]wordCard, len(words))
	if len(words) == 0 {
		return cards, nil
	}
	ids := make([]string, len(words))
	for i, w := range words {
		ids[i] = w.ID
	}
	var rows []models.DictionaryTranslation
	if err := h.DB.Where("word_id IN ? AND lang IN ?", ids, []string{p.Learn, p.Know}).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	tr := make(map[string]models.DictionaryTranslation, len(rows))
	for _, r := range rows {
		tr[r.Lang+"\x00"+r.WordID] = r
	}
	side := func(lang string, w models.DictionaryWord) *CardSide {
		t, ok := tr[lang+"\x00"+w.ID]
		switch lang {
		case "en":
			// Kelime ve örnek yamalarla güncel tutulan dictionary_words'ten.
			s := &CardSide{Lang: lang, Word: w.English, Meaning: w.English, Example: w.Example}
			if ok && t.Meaning != "" {
				s.Meaning = t.Meaning
			}
			return s
		case "tr":
			// Anlam her zaman dictionary_words.turkish (yamalar orayı düzeltir).
			s := &CardSide{Lang: lang, Word: w.Turkish, Meaning: w.Turkish}
			if ok {
				s.Word, s.Example = t.Word, t.Example
			}
			return s
		}
		if !ok {
			return nil
		}
		s := &CardSide{Lang: lang, Word: t.Word, Meaning: t.Meaning, Reading: t.Reading, Example: t.Example}
		if s.Meaning == "" {
			s.Meaning = t.Word
		}
		return s
	}
	for i, w := range words {
		cards[i] = wordCard{DictionaryWord: w, Learn: side(p.Learn, w), Know: side(p.Know, w)}
	}
	return cards, nil
}

// languageInfo, GET /dictionary/languages'taki tek dildir.
type languageInfo struct {
	dictlang.Language
	// Words, bu dilde metni olan kart sayısıdır.
	Words int64 `json:"words"`
	// Complete, sözlüğün tüm kartları bu dilde varsa true'dur; istemci
	// yalnızca tamamlanmış dilleri seçtirmelidir.
	Complete bool `json:"complete"`
}

// GetLanguages: GET /dictionary/languages
// Desteklenen dilleri, her dilde kaç kartın hazır olduğuyla döner.
func (h *Handlers) GetLanguages(c *fiber.Ctx) error {
	var total int64
	if err := h.DB.Model(&models.DictionaryWord{}).Count(&total).Error; err != nil {
		return err
	}
	var counts []struct {
		Lang string
		N    int64
	}
	// Türkçede kelime dictionary_words.turkish'ten de gelebilir ama örnek
	// cümle yalnızca çeviride vardır; tamamlık çeviri satırıyla ölçülür.
	if err := h.DB.Raw(`SELECT t.lang, COUNT(*) AS n FROM dictionary_translations t
		JOIN dictionary_words w ON w.id = t.word_id GROUP BY t.lang`).Scan(&counts).Error; err != nil {
		return err
	}
	byLang := make(map[string]int64, len(counts))
	for _, r := range counts {
		byLang[r.Lang] = r.N
	}
	out := make([]languageInfo, len(dictlang.All))
	for i, l := range dictlang.All {
		n := byLang[l.Code]
		if l.Code == "en" {
			n = total // İngilizce kelime ve örnek her kartta vardır
		}
		out[i] = languageInfo{Language: l, Words: n, Complete: total > 0 && n == total}
	}
	return c.JSON(fiber.Map{"totalWords": total, "languages": out})
}
