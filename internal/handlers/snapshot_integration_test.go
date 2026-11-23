package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/db"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Bu dosya GET /sync/snapshot'ı gerçek rota tablosu ve PostgreSQL'e karşı
// sınar: her bölüm, adı geçen ucun yanıtıyla birebir aynı olmalıdır.
// Kurulum için usage_integration_test.go başındaki nota bakın.

// snapshotTest, gerçek rota tablosunu (auth dahil) kurar.
type snapshotTest struct {
	t   *testing.T
	gdb *gorm.DB
	h   *Handlers
	u   *models.User
	app *fiber.App
}

func newSnapshotTest(t *testing.T) *snapshotTest {
	t.Helper()

	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test", GateEnforcement: config.GateEnforce}, testTokens(), nil)
	return &snapshotTest{t: t, gdb: gdb, h: h, u: u, app: authApp(h)}
}

// get, kullanıcının token'ıyla GET atar ve ham gövdeyi döner.
func (s *snapshotTest) get(path string) []byte {
	s.t.Helper()

	res, err := s.app.Test(authRequest(s.t, s.h, s.u, http.MethodGet, "/api/v1"+path, nil), -1)
	if err != nil {
		s.t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != fiber.StatusOK {
		s.t.Fatalf("GET %s: status = %d (%s)", path, res.StatusCode, body)
	}
	return body
}

// field, JSON nesnesinin bir alanını ham olarak döner.
func field(t *testing.T, raw []byte, key string) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("JSON nesnesi çözülemedi: %v (%s)", err, raw)
	}
	v, ok := m[key]
	if !ok {
		t.Fatalf("%q alanı yok (%s)", key, raw)
	}
	return v
}

func TestSnapshot_BolumlerTekTekUclarlaAyni(t *testing.T) {
	s := newSnapshotTest(t)
	g1 := []models.DictionaryWord{
		testDictWord(t, s.gdb, "itest_snap_1_1", "A1", 1, 901),
		testDictWord(t, s.gdb, "itest_snap_1_2", "A1", 1, 902),
		testDictWord(t, s.gdb, "itest_snap_1_3", "A1", 1, 903),
	}
	testDictWord(t, s.gdb, "itest_snap_2_1", "A1", 2, 901)
	setPointer(t, s.gdb, s.u.ID, "A1", 1)
	yarin := time.Now().Add(24 * time.Hour)
	setProgress(t, s.gdb, s.u.ID, g1[0], 2, &yarin)
	setProgress(t, s.gdb, s.u.ID, g1[1], 1, nil)
	setProgress(t, s.gdb, s.u.ID, g1[2], 6, nil)
	setUsage(t, s.gdb, s.u.ID, map[string]any{"groups_started_today_count": 1, "unlocked_groups_today": "A1:1", "review_answers_today": 4})
	if err := s.gdb.Exec(`INSERT INTO daily_logins (user_id, login_date, created_at) VALUES (?, CAST(? AS date), ?)`,
		s.u.ID, services.UsageDate(dun()), dun().UTC()).Error; err != nil {
		t.Fatal(err)
	}

	// Rozet bildirimi önce tüketilir: anlık görüntü onu hiçbir zaman taşımaz.
	s.get("/stats/summary")
	stats := s.get("/stats/summary")

	snap := s.get("/sync/snapshot")
	esit := func(ad string, got, want []byte) {
		t.Helper()
		if !jsonEsit(t, got, want) {
			t.Errorf("%s bölümü farklı:\n snapshot: %s\n uç:       %s", ad, got, want)
		}
	}
	esit("user", field(t, snap, "user"), field(t, s.get("/auth/me"), "user"))
	esit("usage", field(t, snap, "usage"), s.get("/users/me/usage"))
	esit("learnedCount", field(t, snap, "learnedCount"), field(t, s.get("/home/learned-count"), "learnedCount"))
	esit("openGroups", field(t, snap, "openGroups"), s.get("/map/open-groups"))
	esit("stats", field(t, snap, "stats"), stats)
	esit("loginDates", field(t, snap, "loginDates"), field(t, s.get("/stats/login-dates"), "dates"))

	var levels map[string]json.RawMessage
	if err := json.Unmarshal(field(t, snap, "levels"), &levels); err != nil {
		t.Fatal(err)
	}
	if len(levels) != len(services.Levels) {
		t.Errorf("levels = %d seviye, beklenen %d", len(levels), len(services.Levels))
	}
	for _, level := range services.Levels {
		esit("levels."+level, levels[level], s.get("/map/levels/"+level+"/groups"))
	}

	// progress: kutu 1–5'teki kayıtlar, her biri GET /progress/word/:id ile aynı.
	var progress []json.RawMessage
	if err := json.Unmarshal(field(t, snap, "progress"), &progress); err != nil {
		t.Fatal(err)
	}
	if len(progress) != 2 {
		t.Fatalf("progress = %d kayıt, beklenen 2 (kutu 6 hariç): %s", len(progress), field(t, snap, "progress"))
	}
	for i, id := range []string{g1[0].ID, g1[1].ID} { // wordId sırası
		esit("progress."+id, progress[i], s.get("/progress/word/"+id))
	}

	// cards: ilerlemeli grup (A1:1) ve işaretçi (A1:1) ile sonraki grup (A1:2),
	// GET /dictionary/words biçiminde ve sırasıyla.
	var g1Cards, g2Cards []json.RawMessage
	json.Unmarshal(s.get("/dictionary/words?level=A1&group=1"), &g1Cards)
	json.Unmarshal(s.get("/dictionary/words?level=A1&group=2"), &g2Cards)
	want, _ := json.Marshal(append(g1Cards, g2Cards...))
	esit("cards", field(t, snap, "cards"), want)

	// Üst alanlar.
	var head struct {
		ServerNow         time.Time `json:"serverNow"`
		UsageDate         string    `json:"usageDate"`
		DictionaryVersion int64     `json:"dictionaryVersion"`
	}
	if err := json.Unmarshal(snap, &head); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(head.ServerNow); d < 0 || d > time.Minute {
		t.Errorf("serverNow = %v", head.ServerNow)
	}
	if head.UsageDate != services.UsageDate(time.Now()) {
		t.Errorf("usageDate = %q, beklenen %q", head.UsageDate, services.UsageDate(time.Now()))
	}
	version, err := db.DictionaryVersion(s.gdb)
	if err != nil {
		t.Fatal(err)
	}
	if head.DictionaryVersion != version {
		t.Errorf("dictionaryVersion = %d, beklenen %d", head.DictionaryVersion, version)
	}

	// haveGroups: sürüm güncelse yalnızca istemcinin elindeki gruplar atlanır,
	// gereken diğer grupların kartları her zaman gelir; sürüm farklıysa ya da
	// verilmemişse haveGroups yok sayılır.
	g2Only, _ := json.Marshal(g2Cards)
	v := strconv.FormatInt(version, 10)
	for _, c := range []struct {
		query string
		want  []byte
	}{
		{"?dictionaryVersion=" + v, want},
		{"?dictionaryVersion=" + v + "&haveGroups=A1:1", g2Only},
		{"?dictionaryVersion=" + v + "&haveGroups=A1:1,A1:2,B1:7", []byte("[]")},
		{"?dictionaryVersion=" + v + "&haveGroups=bozuk,A1:x", want},
		{"?dictionaryVersion=" + strconv.FormatInt(version+1, 10) + "&haveGroups=A1:1,A1:2", want},
		{"?haveGroups=A1:1,A1:2", want},
	} {
		esit("cards"+c.query, field(t, s.get("/sync/snapshot"+c.query), "cards"), c.want)
	}
}

// Anlık görüntü rozet bildirimini tüketmez: yeni rozet yazılmaz, bir sonraki
// GET /stats/summary'de bildirilir.
func TestSnapshot_RozetBildiriminiTuketmez(t *testing.T) {
	s := newSnapshotTest(t)

	var snap struct {
		Stats struct {
			Badges []struct {
				ID       string `json:"id"`
				IsEarned bool   `json:"isEarned"`
			} `json:"badges"`
			NewlyEarnedBadges []any `json:"newlyEarnedBadges"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(s.get("/sync/snapshot"), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Stats.NewlyEarnedBadges == nil || len(snap.Stats.NewlyEarnedBadges) != 0 {
		t.Errorf("newlyEarnedBadges = %v, beklenen []", snap.Stats.NewlyEarnedBadges)
	}
	for _, b := range snap.Stats.Badges {
		if b.ID == "first_day" && !b.IsEarned {
			t.Error("first_day anlık görüntüde kazanılmamış görünüyor")
		}
	}
	var n int64
	s.gdb.Model(&models.EarnedBadge{}).Where("user_id = ?", s.u.ID).Count(&n)
	if n != 0 {
		t.Errorf("anlık görüntü %d rozet yazdı", n)
	}
	if got := yeniRozetler(t, mapOf(t, s.get("/stats/summary"))); len(got) == 0 {
		t.Error("anlık görüntüden sonra özet yeni rozeti bildirmedi")
	}
}

// İşaretçi "tümü tamamlandı"da da kartlar yalnızca ilerlemeli gruplardan gelir.
func TestSnapshot_KartlarIsaretciVeSonrakiGrup(t *testing.T) {
	s := newSnapshotTest(t)
	w := testDictWord(t, s.gdb, "itest_snap_c2_last", "C2", 977, 1)
	setPointer(t, s.gdb, s.u.ID, "C2", 977)

	var snap struct {
		Cards []struct {
			ID string `json:"id"`
		} `json:"cards"`
	}
	if err := json.Unmarshal(s.get("/sync/snapshot"), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Cards) != 1 || snap.Cards[0].ID != w.ID {
		t.Errorf("cards = %v, beklenen yalnızca işaretçideki grubun kartı", snap.Cards)
	}

	setPointer(t, s.gdb, s.u.ID, "C2", 0) // tümü tamamlandı
	if err := json.Unmarshal(s.get("/sync/snapshot"), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Cards) != 0 {
		t.Errorf("cards = %v, beklenen boş (ilerleme yok, işaretçi sonda)", snap.Cards)
	}
}

func mapOf(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
