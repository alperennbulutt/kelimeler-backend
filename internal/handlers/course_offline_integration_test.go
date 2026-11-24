package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Bu dosya çevrimdışı mod uçlarının (POST /progress/sync, GET /sync/snapshot)
// kurs davranışını gerçek PostgreSQL'e karşı sınar. Kurulum için
// usage_integration_test.go başındaki nota bakın.

// kursSync, verilen kursla (boşsa başlıksız) POST /progress/sync gönderir,
// 200 ve her işlem için bir sonuç bekler.
func kursSync(t *testing.T, app *fiber.App, course string, ops ...fiber.Map) []map[string]any {
	t.Helper()

	status, body := send(t, app, kursIstegi(http.MethodPost, "/progress/sync", course, fiber.Map{"ops": ops}))
	if status != fiber.StatusOK {
		t.Fatalf("sync (kurs %q) status = %d, beklenen 200 (body: %v)", course, status, body)
	}
	raw, _ := body["results"].([]any)
	if len(raw) != len(ops) {
		t.Fatalf("sonuç sayısı = %d, beklenen %d (body: %v)", len(raw), len(ops), body)
	}
	out := make([]map[string]any, len(raw))
	for i, r := range raw {
		out[i] = r.(map[string]any)
	}
	return out
}

func kursOp(kind string, fields fiber.Map) fiber.Map {
	op := fiber.Map{"opId": uuid.NewString(), "kind": kind}
	for k, v := range fields {
		op[k] = v
	}
	return op
}

// gunlukAcilisSatirlari, kullanıcının daily_group_unlocks satırlarıdır
// ("gün kurs seviye:grup via").
func gunlukAcilisSatirlari(t *testing.T, gdb *gorm.DB, userID string) []string {
	t.Helper()
	var rows []string
	if err := gdb.Raw(`SELECT to_char(usage_date, 'YYYY-MM-DD') || ' ' || course || ' ' || level || ':' || grp || ' ' || via
		FROM daily_group_unlocks WHERE user_id = ? ORDER BY usage_date, course, level, grp`, userID).
		Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// Kelime işlemleri istekteki kursta uygulanır; aynı kelime başka kursta
// bağımsızdır. Desteklenmeyen kurs bütün isteği 400 ile reddeder.
func TestKurs_SyncKelimeIslemleriKursta(t *testing.T) {
	gdb, u, app := newCourseTest(t, config.GateOff)
	g := testLevel(t, gdb, "A1", 2)[0]
	now := time.Now()

	res := kursSync(t, app, "de",
		kursOp("learn", fiber.Map{"wordId": g[0].ID, "answeredAt": rfc(now)}),
		kursOp("unknown", fiber.Map{"wordId": g[1].ID, "answeredAt": rfc(now)}),
	)
	sonucBekle(t, res[0], "applied", "")
	sonucBekle(t, res[1], "applied", "")
	if de, ok := kursSatiri(t, gdb, u.ID, "de", g[0].ID); !ok || de.Box != 2 {
		t.Fatalf("de learn satırı = %+v (%v), beklenen kutu 2", de, ok)
	}
	if de, ok := kursSatiri(t, gdb, u.ID, "de", g[1].ID); !ok || de.Box != 1 {
		t.Fatalf("de unknown satırı = %+v (%v), beklenen kutu 1", de, ok)
	}
	for _, w := range g {
		if _, ok := kursSatiri(t, gdb, u.ID, "en", w.ID); ok {
			t.Errorf("de sync'i en satırı yazdı: %s", w.ID)
		}
	}

	// Başlıksız sync İngilizce kursudur.
	enLearn := kursOp("learn", fiber.Map{"wordId": g[0].ID, "answeredAt": rfc(now)})
	sonucBekle(t, kursSync(t, app, "", enLearn)[0], "applied", "")
	en, ok := kursSatiri(t, gdb, u.ID, "en", g[0].ID)
	if !ok || en.Box != 2 {
		t.Fatalf("en satırı = %+v (%v), beklenen kutu 2", en, ok)
	}

	// de'de doğru cevap yalnızca de satırını ilerletir.
	if err := gdb.Model(&models.WordProgress{}).Where("user_id = ? AND course = 'de' AND word_id = ?", u.ID, g[0].ID).
		Update("next_review_date", nil).Error; err != nil {
		t.Fatal(err)
	}
	res = kursSync(t, app, "de", kursOp("swipe", fiber.Map{"wordId": g[0].ID, "direction": "right", "answeredAt": rfc(now)}))
	sonucBekle(t, res[0], "applied", "")
	var prog models.WordProgress
	if err := json.Unmarshal(mustJSON(t, res[0]["progress"]), &prog); err != nil || prog.Box != 3 {
		t.Errorf("de swipe progress = %+v (%v), beklenen kutu 3", prog, err)
	}
	if got, _ := kursSatiri(t, gdb, u.ID, "en", g[0].ID); got.Box != 2 || got.UpdatedAt != en.UpdatedAt {
		t.Errorf("de swipe en satırını değiştirdi: önce %+v, sonra %+v", en, got)
	}

	// Kursta kaydı olmayan kelimenin cevabı reddedilir; progress gönderilmez.
	res = kursSync(t, app, "fr", kursOp("swipe", fiber.Map{"wordId": g[0].ID, "direction": "right", "answeredAt": rfc(now)}))
	sonucBekle(t, res[0], "rejected", "not_found")
	if _, ok := res[0]["progress"]; ok {
		t.Errorf("fr reddinde başka kursun kaydı döndü: %v", res[0])
	}

	// Aynı opId başka kursla yeniden gelirse saklı sonuç döner, ikinci kursta
	// hiçbir şey uygulanmaz.
	res = kursSync(t, app, "fr", enLearn)
	sonucBekle(t, res[0], "applied", "")
	if _, ok := kursSatiri(t, gdb, u.ID, "fr", g[0].ID); ok {
		t.Error("yinelenen opId fr kursunda yeniden uygulandı")
	}

	// Desteklenmeyen kurs: 400 invalid_course, hiçbir işlem uygulanmaz.
	op := kursOp("learn", fiber.Map{"wordId": g[1].ID, "answeredAt": rfc(now)})
	status, body := send(t, app, kursIstegi(http.MethodPost, "/progress/sync", "xx", fiber.Map{"ops": []fiber.Map{op}}))
	if status != fiber.StatusBadRequest || body["code"] != "invalid_course" {
		t.Errorf("xx sync: status = %d code = %v, beklenen 400 invalid_course", status, body["code"])
	}
	var n int64
	gdb.Model(&models.AppliedOp{}).Where("user_id = ? AND op_id = ?", u.ID, op["opId"]).Count(&n)
	if n != 0 {
		t.Error("reddedilen istekteki işlem kaydedildi")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// İşaretçi işlemleri İngilizce dışı kursta işaretçiye dokunmadan noop
// (course_not_supported) olarak onaylanır ve kaydedilir; İngilizce kursunda
// eskisi gibi uygulanır.
func TestKurs_SyncIsaretciIslemleriIngilizceDisindaNoop(t *testing.T) {
	gdb, u, app := newCourseTest(t, config.GateOff)
	g := testLevel(t, gdb, "A1", 1, 1)
	setCourseProgress(t, gdb, u.ID, "de", g[0][0], 2)
	setPointer(t, gdb, u.ID, "A1", 1)

	set := kursOp("set_current_group", fiber.Map{"level": "A1", "group": 2})
	adv := kursOp("advance_group", fiber.Map{"level": "A1", "group": 1})
	for i := 0; i < 2; i++ { // ikinci gönderim: saklı sonuç
		res := kursSync(t, app, "de", set, adv)
		sonucBekle(t, res[0], "noop", "course_not_supported")
		sonucBekle(t, res[1], "noop", "course_not_supported")
	}
	assertPointer(t, gdb, u.ID, "A1", 1)
	var n int64
	gdb.Model(&models.AppliedOp{}).Where("user_id = ? AND op_id IN ?", u.ID, []any{set["opId"], adv["opId"]}).Count(&n)
	if n != 2 {
		t.Errorf("onaylanan işaretçi işlemi kaydı = %d, beklenen 2", n)
	}
	// Geçersiz grup yine reddedilir.
	sonucBekle(t, kursSync(t, app, "de", kursOp("set_current_group", fiber.Map{"level": "Z9", "group": 1}))[0], "rejected", "invalid_group")

	// İngilizce kursunda işaretçi ileri yazılır.
	sonucBekle(t, kursSync(t, app, "", kursOp("set_current_group", fiber.Map{"level": "A1", "group": 2}))[0], "applied", "")
	assertPointer(t, gdb, u.ID, "A1", 2)
}

// group_started açılışı kurslu anahtarla ve kurslu günlük geçmişle yazılır;
// geçmiş günün öğrenme cevabı o kursun açılışına göre denetlenir; günün
// ücretsiz hakkı kullanıcı başınadır.
func TestKurs_SyncGroupStartedVeGecmisGunKapisiKursBasina(t *testing.T) {
	gdb, u, app := newCourseTest(t, config.GateEnforce)
	g := testLevel(t, gdb, "A1", 3)[0]
	bugun := services.UsageDate(time.Now())
	dunGun := services.UsageDate(dun())

	sonucBekle(t, kursSync(t, app, "de", kursOp("group_started", fiber.Map{"level": "A1", "group": 1, "usageDate": bugun}))[0], "applied", "")
	var usage dto.DailyUsageDTO
	kursGet(t, app, "/users/me/usage", "", &usage)
	if !slices.Equal(usage.UnlockedGroupsToday, []string{"de/A1:1"}) {
		t.Errorf("unlockedGroupsToday = %v, beklenen [de/A1:1]", usage.UnlockedGroupsToday)
	}

	sonucBekle(t, kursSync(t, app, "de", kursOp("group_started", fiber.Map{"level": "A1", "group": 1, "usageDate": dunGun}))[0], "applied", "")
	want := []string{dunGun + " de A1:1 offline_free", bugun + " de A1:1 free"}
	if got := gunlukAcilisSatirlari(t, gdb, u.ID); !slices.Equal(got, want) {
		t.Fatalf("günlük açılışlar = %v, beklenen %v", got, want)
	}

	// Dünkü öğrenme: de'de açık, en'de değil.
	sonucBekle(t, kursSync(t, app, "de", learnOp(g[0].ID, dun()))[0], "applied", "")
	sonucBekle(t, kursSync(t, app, "", learnOp(g[1].ID, dun()))[0], "rejected", "gate_required")
	if _, ok := kursSatiri(t, gdb, u.ID, "en", g[1].ID); ok {
		t.Error("reddedilen en öğrenmesi yazıldı")
	}

	// Dünün ücretsiz hakkı de'de kullanıldı: en'de aynı grubun ücretsiz
	// açılışı yine yazılır ama çakışma olarak işaretlenir.
	sonucBekle(t, kursSync(t, app, "", kursOp("group_started", fiber.Map{"level": "A1", "group": 1, "usageDate": dunGun}))[0], "applied", "free_group_conflict")
	sonucBekle(t, kursSync(t, app, "", learnOp(g[1].ID, dun()))[0], "applied", "")
}

// Anlık görüntünün kursa ait bölümleri istekteki kursundur; kartlar kursun
// ilerlemesine göre seçilir ve learn/know verilirse dil çiftiyle zenginleşir.
func TestKurs_SnapshotKursBasinaVeDilCifti(t *testing.T) {
	gdb, u, app := newCourseTest(t, config.GateOff)
	g := testLevel(t, gdb, "A1", 2, 2, 2)
	services.ResetLevelTotalsCache()
	t.Cleanup(services.ResetLevelTotalsCache)
	setPointer(t, gdb, u.ID, "A1", 1)
	// de: 1. grup tamamlandı (biri kutu 2, biri kutu 6).
	setCourseProgress(t, gdb, u.ID, "de", g[0][0], 2)
	setCourseProgress(t, gdb, u.ID, "de", g[0][1], 6)
	testTranslation(t, gdb, models.DictionaryTranslation{WordID: g[0][0].ID, Lang: "de", Word: "die Schule", Example: "Die Schule ist groß."})

	type kart struct {
		ID    string          `json:"id"`
		Learn json.RawMessage `json:"learn"`
		Know  json.RawMessage `json:"know"`
	}
	type goruntu struct {
		Progress       []map[string]any               `json:"progress"`
		LearnedCount   int                            `json:"learnedCount"`
		LearnedWordIDs []string                       `json:"learnedWordIds"`
		Levels         map[string][]dto.LevelGroupDTO `json:"levels"`
		Cards          []kart                         `json:"cards"`
		Stats          dto.StatsSummaryDTO            `json:"stats"`
	}
	ids := func(cards []kart) []string {
		out := make([]string, len(cards))
		for i, c := range cards {
			out[i] = c.ID
		}
		return out
	}

	var de, en goruntu
	kursGet(t, app, "/sync/snapshot", "de", &de)
	kursGet(t, app, "/sync/snapshot", "", &en)

	if len(de.Progress) != 1 || de.Progress[0]["wordId"] != g[0][0].ID || len(en.Progress) != 0 {
		t.Errorf("progress de=%v en=%v", de.Progress, en.Progress)
	}
	if de.LearnedCount != 1 || !slices.Equal(de.LearnedWordIDs, []string{g[0][1].ID}) || en.LearnedCount != 0 || len(en.LearnedWordIDs) != 0 {
		t.Errorf("öğrenilenler de=(%d %v) en=(%d %v)", de.LearnedCount, de.LearnedWordIDs, en.LearnedCount, en.LearnedWordIDs)
	}
	if !de.Levels["A1"][0].IsCompleted || en.Levels["A1"][0].IsCompleted {
		t.Errorf("harita A1/1 de=%+v en=%+v", de.Levels["A1"][0], en.Levels["A1"][0])
	}
	if de.Stats.TotalLearned != 1 || en.Stats.TotalLearned != 0 {
		t.Errorf("stats.totalLearned de=%d en=%d", de.Stats.TotalLearned, en.Stats.TotalLearned)
	}
	// en: işaretçideki grup ve sonraki (1, 2). de: ilerlemeli grup (1) ve
	// haritada açık ilk başlanmamış grup ile sonrası (2, 3).
	wantEn := []string{g[0][0].ID, g[0][1].ID, g[1][0].ID, g[1][1].ID}
	wantDe := append(slices.Clone(wantEn), g[2][0].ID, g[2][1].ID)
	if got := ids(en.Cards); !slices.Equal(got, wantEn) {
		t.Errorf("en kartları = %v, beklenen %v", got, wantEn)
	}
	if got := ids(de.Cards); !slices.Equal(got, wantDe) {
		t.Errorf("de kartları = %v, beklenen %v", got, wantDe)
	}
	for _, c := range de.Cards {
		if c.Learn != nil || c.Know != nil {
			t.Fatalf("learn/know verilmeden kartta learn/know var: %+v", c)
		}
	}

	// learn/know: kartlara dil çifti eklenir, diğer bölümler aynı kalır.
	raw := getRaw(t, app, "/sync/snapshot", "de")
	rawPair := getRaw(t, app, "/sync/snapshot?learn=de&know=tr", "de")
	var plainMap, pairMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &plainMap); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawPair, &pairMap); err != nil {
		t.Fatal(err)
	}
	for k := range plainMap {
		if k == "serverNow" || k == "cards" {
			continue
		}
		if !jsonEsit(t, plainMap[k], pairMap[k]) {
			t.Errorf("learn/know %q bölümünü değiştirdi:\n%s\n%s", k, plainMap[k], pairMap[k])
		}
	}
	if len(pairMap) != len(plainMap) {
		t.Errorf("alan sayısı düz=%d çiftli=%d", len(plainMap), len(pairMap))
	}
	var paired goruntu
	if err := json.Unmarshal(rawPair, &paired); err != nil {
		t.Fatal(err)
	}
	if got := ids(paired.Cards); !slices.Equal(got, wantDe) {
		t.Fatalf("çiftli kartlar = %v, beklenen %v", got, wantDe)
	}
	var learn, know CardSide
	if err := json.Unmarshal(paired.Cards[0].Learn, &learn); err != nil || learn.Word != "die Schule" || learn.Lang != "de" {
		t.Errorf("1. kartın learn'ü = %s (%v)", paired.Cards[0].Learn, err)
	}
	if err := json.Unmarshal(paired.Cards[0].Know, &know); err != nil || know.Lang != "tr" || know.Meaning != g[0][0].Turkish {
		t.Errorf("1. kartın know'u = %s (%v)", paired.Cards[0].Know, err)
	}
	if string(paired.Cards[1].Learn) != "null" {
		t.Errorf("çevirisi olmayan kartın learn'ü = %s, beklenen null", paired.Cards[1].Learn)
	}

	// Geçersiz dil çifti 400 invalid_language.
	for _, q := range []string{"?learn=de", "?learn=de&know=de", "?learn=xx&know=tr"} {
		status, body := send(t, app, kursIstegi(http.MethodGet, "/sync/snapshot"+q, "de", nil))
		if status != fiber.StatusBadRequest || body["code"] != "invalid_language" {
			t.Errorf("%s: status = %d code = %v, beklenen 400 invalid_language", q, status, body["code"])
		}
	}
}

// getRaw, kursla GET atar, 200 bekler ve ham gövdeyi döner.
func getRaw(t *testing.T, app *fiber.App, path, course string) []byte {
	t.Helper()
	res, err := app.Test(kursIstegi(http.MethodGet, path, course, nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != fiber.StatusOK {
		t.Fatalf("GET %s status = %d (%s)", path, res.StatusCode, raw)
	}
	return raw
}
