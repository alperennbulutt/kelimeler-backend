package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
)

// POST /dictionary/words/batch testleri. uniqueBatchIDs testleri veritabanı
// istemez; geri kalanı gerçek PostgreSQL ister (bkz. usage_integration_test.go).

func TestUniqueBatchIDs(t *testing.T) {
	cases := []struct {
		ad     string
		raw    []string
		max    int
		want   []string
		wantOK bool
	}{
		{"nil", nil, 3, []string{}, true},
		{"yalnızca boş", []string{"", ""}, 3, []string{}, true},
		{"boşlar atlanır, tekrarlar ilk sırasıyla tekile iner", []string{"b", "", "a", "b", "a", "c"}, 3, []string{"b", "a", "c"}, true},
		{"tekil sayısı tam sınırda", []string{"a", "b", "c"}, 3, []string{"a", "b", "c"}, true},
		{"tekrarlar sınıra sayılmaz", []string{"a", "a", "a", "a", "b", "b"}, 2, []string{"a", "b"}, true},
		{"tekil sayısı sınırı aşar", []string{"a", "b", "c", "d"}, 3, nil, false},
	}
	for _, c := range cases {
		got, ok := uniqueBatchIDs(c.raw, c.max)
		if ok != c.wantOK || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: = (%v, %v), beklenen (%v, %v)", c.ad, got, ok, c.want, c.wantOK)
		}
	}
}

// batchIstegi, kimlikli bir POST /dictionary/words/batch atar. Yanıt 200 ise
// dönen kelime id'lerini sıralı olarak, değilse hata gövdesini döner.
func batchIstegi(t *testing.T, h *Handlers, u *models.User, payload any) (int, []string, map[string]any) {
	t.Helper()

	req := authRequest(t, h, u, http.MethodPost, "/api/v1/dictionary/words/batch", payload)
	res, err := authApp(h).Test(req, -1)
	if err != nil {
		t.Fatalf("istek başarısız: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)

	if res.StatusCode != fiber.StatusOK {
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		return res.StatusCode, nil, body
	}
	var words []models.DictionaryWord
	if err := json.Unmarshal(raw, &words); err != nil {
		t.Fatalf("yanıt kelime listesi değil: %v (%s)", err, raw)
	}
	ids := make([]string, 0, len(words))
	for _, w := range words {
		ids = append(ids, w.ID)
	}
	sort.Strings(ids)
	return res.StatusCode, ids, nil
}

// idListesi, önek_1..önek_n biçiminde n id üretir.
func idListesi(onEk string, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s_%d", onEk, i+1)
	}
	return ids
}

func TestWordsBatch_TekrarVeBosIdlerYokSayilir(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)
	a := testDictWord(t, gdb, "itest_batch_a_"+u.ID, "A1", 1, 1)
	b := testDictWord(t, gdb, "itest_batch_b_"+u.ID, "A1", 1, 2)

	status, ids, body := batchIstegi(t, h, u, fiber.Map{"ids": []string{a.ID, "", b.ID, a.ID, "itest_batch_yok", b.ID, ""}})
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, beklenen 200 (body: %v)", status, body)
	}
	want := []string{a.ID, b.ID}
	sort.Strings(want)
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("dönen id'ler = %v, beklenen her kelime bir kez: %v", ids, want)
	}
}

func TestWordsBatch_BosListeVeYalnizcaBosIdler(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)

	for ad, payload := range map[string]any{
		"ids yok":         fiber.Map{},
		"boş liste":       fiber.Map{"ids": []string{}},
		"yalnızca boşlar": fiber.Map{"ids": []string{"", "", ""}},
	} {
		status, ids, body := batchIstegi(t, h, u, payload)
		if status != fiber.StatusOK || len(ids) != 0 {
			t.Errorf("%s: status = %d, ids = %v, body = %v; beklenen 200 []", ad, status, ids, body)
		}
	}
}

// İstemci tek seferde bir tekrar batch'i kadar (50) id gönderir; sınır onu
// hiçbir zaman reddetmemeli. Sınırın tam değeri kabul, bir fazlası reddedilir.
func TestWordsBatch_IdSayisiSiniri(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)
	gercek := testDictWord(t, gdb, "itest_batch_sinir_"+u.ID, "A1", 1, 1)

	istemciBatch := append(idListesi("itest_batch_istemci", 49), gercek.ID)
	tamSinir := append(idListesi("itest_batch_sinir", maxBatchIDs-1), gercek.ID)
	sinirArti1 := append(idListesi("itest_batch_fazla", maxBatchIDs), gercek.ID)
	// Tekrarlar sınıra sayılmaz: 3 tekil id'nin 600 kopyası kabul edilir.
	cokTekrar := make([]string, 0, 600)
	for i := 0; i < 200; i++ {
		cokTekrar = append(cokTekrar, gercek.ID, "itest_batch_t1", "itest_batch_t2")
	}

	for _, c := range []struct {
		ad     string
		ids    []string
		status int
	}{
		{"istemcinin batch'i (50)", istemciBatch, fiber.StatusOK},
		{"tam sınır", tamSinir, fiber.StatusOK},
		{"çok tekrar, az tekil", cokTekrar, fiber.StatusOK},
		{"sınır + 1", sinirArti1, fiber.StatusBadRequest},
	} {
		status, ids, body := batchIstegi(t, h, u, fiber.Map{"ids": c.ids})
		if status != c.status {
			t.Errorf("%s (%d id): status = %d, beklenen %d (body: %v)", c.ad, len(c.ids), status, c.status, body)
			continue
		}
		if status == fiber.StatusOK && !reflect.DeepEqual(ids, []string{gercek.ID}) {
			t.Errorf("%s: dönen id'ler = %v, beklenen [%s]", c.ad, ids, gercek.ID)
		}
		if status == fiber.StatusBadRequest {
			if body["code"] != "invalid_request" || !strings.Contains(fmt.Sprint(body["error"]), fmt.Sprint(maxBatchIDs)) {
				t.Errorf("%s: gövde = %v, beklenen invalid_request ve sınır değeri", c.ad, body)
			}
		}
	}
}

func TestWordsBatch_BozukGovde400Doner(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, testTokens(), nil)

	status, _, body := batchIstegi(t, h, u, fiber.Map{"ids": "A1_1_1"})
	if status != fiber.StatusBadRequest || body["code"] != "invalid_request" {
		t.Errorf("status = %d, body = %v; beklenen 400 invalid_request", status, body)
	}
}

func TestWordsBatch_VeritabaniHatasi500Doner(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(kopukDB(t), &config.Config{AppEnv: "test"}, testTokens(), nil)

	status, _, body := batchIstegi(t, h, u, fiber.Map{"ids": []string{"A1_1_1"}})
	if status != fiber.StatusInternalServerError {
		t.Errorf("status = %d, beklenen 500 (body: %v)", status, body)
	}
}
