package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/dto"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Bu dosya "görüldü" kuralını ve açık grup sınırını (services.MaxOpenGroups)
// gerçek PostgreSQL'e karşı sınar: bütün kelimeleri cevaplanan (bilinen ya da
// bilinmeyen) grubun ardındaki grup açılır, aynı anda en fazla üç grup açık
// kalır. Kurulum için usage_integration_test.go başındaki nota bakın;
// TEST_DATABASE_URL yoksa testler atlanır.

func openGroupApp(h *Handlers, userID string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	inject := func(c *fiber.Ctx) error {
		c.Locals("userID", userID)
		return c.Next()
	}
	app.Post("/progress/learn", inject, h.LearnWord)
	app.Post("/progress/unknown", inject, h.MarkUnknown)
	app.Post("/progress/swipe", inject, h.Swipe)
	app.Get("/users/me/usage", inject, h.GetDailyUsage)
	app.Post("/users/me/usage/group-started", inject, h.UnlockGroup(freeGroupColumn))
	app.Post("/users/me/usage/group-unlocked-by-ad", inject, h.UnlockGroup("groups_unlocked_by_ad_today"))
	app.Put("/users/me/current-group", inject, h.SetCurrentGroup)
	app.Post("/users/me/advance-group", inject, h.AdvanceGroup)
	app.Get("/map/levels/:level/groups", inject, h.LevelGroups)
	app.Get("/map/open-groups", inject, h.OpenGroups)
	return app
}

func newOpenGroupTest(t *testing.T, mode config.GateEnforcement) (*gorm.DB, *models.User, *fiber.App) {
	t.Helper()

	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test", GateEnforcement: mode}, nil, nil)
	return gdb, u, openGroupApp(h, u.ID)
}

func unknownRequest(wordID string) *http.Request {
	return jsonRequest("/progress/unknown", fiber.Map{"wordId": wordID})
}

// groupOf, seviyenin haritadaki n. grubudur.
func groupOf(t *testing.T, app *fiber.App, level string, n int) dto.LevelGroupDTO {
	t.Helper()
	for _, g := range levelGroups(t, app, level) {
		if g.GroupNumber == n {
			return g
		}
	}
	t.Fatalf("%s grup %d haritada yok", level, n)
	return dto.LevelGroupDTO{}
}

func openGroups(t *testing.T, app *fiber.App) dto.OpenGroupsDTO {
	t.Helper()
	status, raw := getJSON(t, app, "/map/open-groups")
	var body dto.OpenGroupsDTO
	if status != fiber.StatusOK || json.Unmarshal(raw, &body) != nil {
		t.Fatalf("open-groups = %d %s", status, raw)
	}
	return body
}

// seeGroup, grubun ilk kelimesine "Biliyorum" (learn), kalanlarına
// "Bilmiyorum" (unknown) der: grup görülür ama tamamlanmaz. Gruplar bugün
// açık olmalıdır.
func seeGroup(t *testing.T, app *fiber.App, words []models.DictionaryWord) {
	t.Helper()
	for i, w := range words {
		req := unknownRequest(w.ID)
		if i == 0 {
			req = learnRequest(fiber.Map{"wordId": w.ID})
		}
		if status, body := send(t, app, req); status != fiber.StatusOK {
			t.Fatalf("%s %s status = %d (body: %v)", req.URL.Path, w.ID, status, body)
		}
	}
}

// --- POST /progress/unknown ---

func TestMarkUnknown_Kutu1KaydiYazarVeIdempotent(t *testing.T) {
	gdb, u, app := newOpenGroupTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 2)
	hepsiBugunAcik(t, gdb, u.ID, "A1:1")
	w := a1[0][0]

	for i := 0; i < 2; i++ {
		status, body := send(t, app, unknownRequest(w.ID))
		if status != fiber.StatusOK {
			t.Fatalf("%d. istek status = %d (body: %v)", i+1, status, body)
		}
		if intField(t, body, "box") != 1 || body["wordId"] != w.ID || body["level"] != "A1" || intField(t, body, "group") != 1 {
			t.Errorf("%d. yanıt = %v, beklenen kutu 1 A1:1", i+1, body)
		}
	}
	wp := loadProgress(t, gdb, u.ID, w.ID)
	if wp.Box != 1 || wp.WrongCount != 1 || wp.Learned || wp.English != w.English {
		t.Errorf("kayıt = %+v, beklenen kutu 1, wrongCount 1 (yinelenen istek saymaz)", wp)
	}

	// Kutu 1'deki kelime sonra bilinince learn onu kutu 2'ye alır.
	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": w.ID})); status != fiber.StatusOK || intField(t, body, "box") != 2 {
		t.Fatalf("learn status = %d (body: %v), beklenen kutu 2", status, body)
	}
	// Kaydı olan kelimeye unknown hiçbir şeyi değiştirmez: kutu 1'e geri atmaz.
	if status, body := send(t, app, unknownRequest(w.ID)); status != fiber.StatusOK || intField(t, body, "box") != 2 {
		t.Errorf("ilerlemiş kelimeye unknown status = %d (body: %v), beklenen kutu 2", status, body)
	}
	if got := loadProgress(t, gdb, u.ID, w.ID).Box; got != 2 {
		t.Errorf("kutu = %d, beklenen 2", got)
	}
}

func TestMarkUnknown_HataKodlari(t *testing.T) {
	gdb, u, app := newOpenGroupTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 1)

	if status, _ := send(t, app, jsonRequest("/progress/unknown", fiber.Map{})); status != fiber.StatusBadRequest {
		t.Errorf("wordId yok status = %d, beklenen 400", status)
	}
	if status, body := send(t, app, unknownRequest("itest_olmayan")); status != fiber.StatusNotFound || gateCode(body) != "unknown_word" {
		t.Errorf("sözlükte olmayan status = %d (body: %v), beklenen 404 unknown_word", status, body)
	}
	// Grup bugün açılmadı: learn gibi gate_required.
	if status, body := send(t, app, unknownRequest(a1[0][0].ID)); status != fiber.StatusForbidden || gateCode(body) != "gate_required" {
		t.Errorf("açılmamış grup status = %d (body: %v), beklenen 403 gate_required", status, body)
	}
	if got := progressRowCount(t, gdb, u.ID, a1[0][0].ID); got != 0 {
		t.Error("reddedilen unknown kayıt yazdı")
	}
}

func TestSwipe_Kutu1SagGunlukKapidanGecer(t *testing.T) {
	// Kutu 1 kaydı unknown ile yazılabildiği için sağa kaydırması (öğrenme)
	// learn gibi günlük kapıya sorulur: grubu bir gün açıp her kelimeye
	// "Bilmiyorum" diyen istemci ertesi gün grubu açmadan öğrenemez.
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newOpenGroupTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 2)
	w := a1[0][0]
	setProgress(t, gdb, u.ID, w, 1, nil)

	status, body := send(t, app, progressSwipe(w.ID, "right"))
	if status != fiber.StatusForbidden || gateCode(body) != "gate_required" {
		t.Fatalf("açılmamış grupta kutu 1 sağ status = %d (body: %v), beklenen 403 gate_required", status, body)
	}
	if got := loadProgress(t, gdb, u.ID, w.ID).Box; got != 1 {
		t.Errorf("reddedilen swipe kutuyu değiştirdi: %d", got)
	}
	// "Bilmiyorum" kapıya sorulmaz.
	if status, body := send(t, app, progressSwipe(w.ID, "left")); status != fiber.StatusOK {
		t.Errorf("kutu 1 sol status = %d (body: %v)", status, body)
	}
	if l := kayit.ihlaller(); len(l) != 1 || !strings.Contains(l[0], "reason=group_not_unlocked") {
		t.Errorf("ihlal satırları = %v, beklenen tek group_not_unlocked", l)
	}

	hepsiBugunAcik(t, gdb, u.ID, "A1:1")
	if status, body := send(t, app, progressSwipe(w.ID, "right")); status != fiber.StatusOK || intField(t, body, "box") != 2 {
		t.Errorf("açık grupta kutu 1 sağ status = %d (body: %v), beklenen kutu 2", status, body)
	}
}

// --- Görüldü kuralı ---

func TestAcikGrup_GorulenGrupSonrakiniAcar(t *testing.T) {
	gdb, u, app := newOpenGroupTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 2, 2, 2)
	hepsiBugunAcik(t, gdb, u.ID, "A1:1", "A1:2", "A1:3")

	// Grup 1'in yalnızca bir kelimesi cevaplandı: görülmedi, grup 2 kilitli.
	if status, body := send(t, app, unknownRequest(a1[0][0].ID)); status != fiber.StatusOK {
		t.Fatalf("unknown status = %d (body: %v)", status, body)
	}
	g1, g2 := groupOf(t, app, "A1", 1), groupOf(t, app, "A1", 2)
	if g1.IsSeen || g1.SeenWords != 1 || g1.RemainingInBox1 != 2 || !g1.HasStarted {
		t.Errorf("grup 1 = %+v, beklenen görülmemiş, 1 kelime görüldü, 2 kalan", g1)
	}
	if g2.IsUnlocked || g2.LockReason != services.LockReasonPreviousNotSeen {
		t.Errorf("grup 2 = %+v, beklenen kilitli previous_not_seen", g2)
	}
	status, body := send(t, app, learnRequest(fiber.Map{"wordId": a1[1][0].ID}))
	if status != fiber.StatusForbidden || gateCode(body) != "gate_required" {
		t.Fatalf("görülmemiş grubun ardı status = %d (body: %v), beklenen 403 gate_required", status, body)
	}

	// İkinci kelime bilinince grup 1 görülür ama tamamlanmaz (biri kutu 1'de):
	// grup 2 açılır, grup 3 kilitli kalır.
	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": a1[0][1].ID})); status != fiber.StatusOK {
		t.Fatalf("learn status = %d (body: %v)", status, body)
	}
	g1, g2 = groupOf(t, app, "A1", 1), groupOf(t, app, "A1", 2)
	if !g1.IsSeen || g1.IsCompleted || g1.RemainingInBox1 != 1 || g1.SeenWords != 2 {
		t.Errorf("grup 1 = %+v, beklenen görülmüş, tamamlanmamış, 1 kalan", g1)
	}
	if !g2.IsUnlocked || g2.LockReason != "" || g2.HasStarted {
		t.Errorf("grup 2 = %+v, beklenen açık ve başlanmamış", g2)
	}
	if g3 := groupOf(t, app, "A1", 3); g3.IsUnlocked || g3.LockReason != services.LockReasonPreviousNotSeen {
		t.Errorf("grup 3 = %+v, beklenen kilitli previous_not_seen", g3)
	}
	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": a1[1][0].ID})); status != fiber.StatusOK {
		t.Fatalf("görülmüş grubun ardı status = %d (body: %v)", status, body)
	}

	// Seviye tamamlama değişmez: A1 görülmüş ama tamamlanmamış, A2 kilitli.
	testLevel(t, gdb, "A2", 1)
	seeGroup(t, app, a1[1][1:])
	seeGroup(t, app, a1[2])
	if g := groupOf(t, app, "A2", 1); g.IsUnlocked || g.LockReason != services.LockReasonPreviousLevel {
		t.Errorf("A2 grup 1 = %+v, beklenen kilitli previous_level (A1 tamamlanmadı)", g)
	}
}

// --- Açık grup sınırı ---

// threeOpenGroups, A1'i 2'şer kelimelik dört grupla kurar ve ilk üç grubu
// görülmüş ama tamamlanmamış bırakır (her birinde bir kelime kutu 1'de):
// açık grup sınırı dolar. Dört grup da bugün açıktır; işaretçi A1:3'tedir.
func threeOpenGroups(t *testing.T, gdb *gorm.DB, userID string, app *fiber.App) [][]models.DictionaryWord {
	t.Helper()
	a1 := testLevel(t, gdb, "A1", 2, 2, 2, 2)
	hepsiBugunAcik(t, gdb, userID, "A1:1", "A1:2", "A1:3", "A1:4")
	for g := 0; g < 3; g++ {
		seeGroup(t, app, a1[g])
	}
	setPointer(t, gdb, userID, "A1", 3)
	return a1
}

func TestAcikGrup_UcAcikGrupDorduncuyuKilitler(t *testing.T) {
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newOpenGroupTest(t, config.GateEnforce)
	a1 := threeOpenGroups(t, gdb, u.ID, app)

	g4 := groupOf(t, app, "A1", 4)
	if g4.IsUnlocked || g4.LockReason != services.LockReasonOpenGroupLimit {
		t.Errorf("grup 4 = %+v, beklenen kilitli open_group_limit", g4)
	}
	// Başlanmış gruplar sınırdan etkilenmez.
	for n := 1; n <= 3; n++ {
		if g := groupOf(t, app, "A1", n); !g.IsUnlocked || g.LockReason != "" {
			t.Errorf("açık grup %d = %+v, beklenen açık", n, g)
		}
	}
	og := openGroups(t, app)
	if og.OpenGroupCount != 3 || og.MaxOpenGroups != services.MaxOpenGroups || len(og.OpenGroups) != 3 ||
		og.OpenGroups[0] != (dto.OpenGroupDTO{Level: "A1", Group: 1, TotalWords: 2, RemainingInBox1: 1}) ||
		og.OpenGroups[2].Group != 3 {
		t.Errorf("open-groups = %+v, beklenen A1:1..3 (her birinde 1 kalan), sınır 3", og)
	}

	// Sunucu da reddeder: learn, unknown, grup açılışı, işaretçi.
	for _, req := range []*http.Request{
		learnRequest(fiber.Map{"wordId": a1[3][0].ID}),
		unknownRequest(a1[3][1].ID),
	} {
		path := req.URL.Path
		status, body := send(t, app, req)
		if status != fiber.StatusForbidden || gateCode(body) != "open_group_limit" {
			t.Errorf("%s status = %d (body: %v), beklenen 403 open_group_limit", path, status, body)
		}
	}
	for _, w := range a1[3] {
		if got := progressRowCount(t, gdb, u.ID, w.ID); got != 0 {
			t.Errorf("sınır doluyken %s yazıldı", w.ID)
		}
	}
	if l := kayit.ihlaller(); len(l) != 2 || !strings.Contains(l[0], "reason=open_group_limit") || !strings.Contains(l[1], "reason=open_group_limit") {
		t.Errorf("ihlal satırları = %v, beklenen iki open_group_limit", l)
	}

	status, body := send(t, app, currentGroupRequest(fiber.Map{"level": "A1", "group": 4}))
	if status != fiber.StatusForbidden || gateCode(body) != "open_group_limit" {
		t.Errorf("current-group status = %d (body: %v), beklenen 403 open_group_limit", status, body)
	}
	assertPointer(t, gdb, u.ID, "A1", 3)
	// Geriye ya da aynı gruba yazım sınırdan etkilenmez.
	if status, body := send(t, app, currentGroupRequest(fiber.Map{"level": "A1", "group": 3, "title": "uc"})); status != fiber.StatusOK {
		t.Errorf("aynı grup current-group status = %d (body: %v)", status, body)
	}

	// Grup 3 görüldü, işaretçi ilerletilmek isteniyor: sınır dolu, yerinde kalır.
	status, body = send(t, app, advanceRequest(fiber.Map{"level": "A1", "group": 3}))
	if status != fiber.StatusOK || body["blockedReason"] != "open_group_limit" || body["levelChanged"] != false ||
		body["newLevel"] != "A1" || intField(t, body, "newGroup") != 3 {
		t.Errorf("advance status = %d body = %v, beklenen işaretçi A1:3, blockedReason open_group_limit", status, body)
	}
	assertPointer(t, gdb, u.ID, "A1", 3)

	// Açık gruplardan biri tamamlanınca (kutu 1'deki kelime bilinir) yer açılır.
	if status, body := send(t, app, progressSwipe(a1[0][1].ID, "right")); status != fiber.StatusOK {
		t.Fatalf("swipe status = %d (body: %v)", status, body)
	}
	if g := groupOf(t, app, "A1", 1); !g.IsCompleted {
		t.Fatalf("grup 1 = %+v, beklenen tamamlandı", g)
	}
	if g := groupOf(t, app, "A1", 4); !g.IsUnlocked || g.LockReason != "" {
		t.Errorf("yer açılınca grup 4 = %+v, beklenen açık", g)
	}
	if og := openGroups(t, app); og.OpenGroupCount != 2 {
		t.Errorf("openGroupCount = %d, beklenen 2", og.OpenGroupCount)
	}
	status, body = send(t, app, advanceRequest(fiber.Map{"level": "A1", "group": 3}))
	if status != fiber.StatusOK || body["blockedReason"] != "" || intField(t, body, "newGroup") != 4 {
		t.Errorf("advance status = %d body = %v, beklenen A1:4", status, body)
	}
	assertPointer(t, gdb, u.ID, "A1", 4)
	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": a1[3][0].ID})); status != fiber.StatusOK {
		t.Errorf("yer açılınca grup 4 learn status = %d (body: %v)", status, body)
	}
	// Dört grup açık değil üç: grup 4 başladı, sınır yeniden doldu ama
	// başlanmış grubun sonraki kelimesi kabul edilir.
	if status, body := send(t, app, unknownRequest(a1[3][1].ID)); status != fiber.StatusOK {
		t.Errorf("başlanmış grup 4 unknown status = %d (body: %v)", status, body)
	}
	if og := openGroups(t, app); og.OpenGroupCount != 3 {
		t.Errorf("openGroupCount = %d, beklenen 3", og.OpenGroupCount)
	}
}

func TestAcikGrup_SeviyelerBirlikteSayilir(t *testing.T) {
	// Sınır bütün seviyelerin açık gruplarını birlikte sayar; open-groups
	// listesi seviye, sonra grup sırasındadır. A1 rozetle tamamlanmış
	// sayılır ama A1:1'de kutu 1'de bir kelime kalmış (ör. eski kayıt):
	// A1:1 açık grup olarak A2'deki sınırı da doldurur.
	gdb, u, app := newOpenGroupTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 2)
	a2 := testLevel(t, gdb, "A2", 2, 2, 2)
	if err := gdb.Create(&models.EarnedBadge{UserID: u.ID, BadgeID: "level_A1", EarnedAt: time.Now()}).Error; err != nil {
		t.Fatalf("rozet yazılamadı: %v", err)
	}
	setProgress(t, gdb, u.ID, a1[0][0], 2, nil)
	setProgress(t, gdb, u.ID, a1[0][1], 1, nil)
	hepsiBugunAcik(t, gdb, u.ID, "A2:1", "A2:2", "A2:3")
	seeGroup(t, app, a2[0])
	seeGroup(t, app, a2[1])

	if g := groupOf(t, app, "A2", 3); g.IsUnlocked || g.LockReason != services.LockReasonOpenGroupLimit {
		t.Errorf("A2 grup 3 = %+v, beklenen kilitli open_group_limit", g)
	}
	og := openGroups(t, app)
	var got []string
	for _, g := range og.OpenGroups {
		got = append(got, fmt.Sprintf("%s:%d", g.Level, g.Group))
	}
	if og.OpenGroupCount != 3 || strings.Join(got, ",") != "A1:1,A2:1,A2:2" {
		t.Errorf("open-groups = %+v, beklenen A1:1,A2:1,A2:2", og)
	}
	status, body := send(t, app, learnRequest(fiber.Map{"wordId": a2[2][0].ID}))
	if status != fiber.StatusForbidden || gateCode(body) != "open_group_limit" {
		t.Errorf("A2:3 learn status = %d (body: %v), beklenen 403 open_group_limit", status, body)
	}
}

// openGroupKeys, open-groups listesini "A1:1,A2:3" biçiminde yazar.
func openGroupKeys(og dto.OpenGroupsDTO) string {
	var keys []string
	for _, g := range og.OpenGroups {
		keys = append(keys, fmt.Sprintf("%s:%d", g.Level, g.Group))
	}
	return strings.Join(keys, ",")
}

func TestAcikGrup_OncekiSeviyeKilitliGruplarSayilmaz(t *testing.T) {
	// Taşınmış veri: A1 bitmeden A2'nin üç grubunda kayıt var (her birinde
	// bir kelime kutu 1'de). Bu gruplar previous_level ile kilitli; sınıra
	// sayılsalardı A1'in yeni grubu da open_group_limit ile kilitlenir,
	// kullanıcı hiçbir gruba giremezdi.
	kapiRaporunuYakala(t)
	gdb, u, app := newOpenGroupTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 2, 2, 2, 2)
	a2 := testLevel(t, gdb, "A2", 2, 2, 2, 2)
	for g := 0; g < 3; g++ {
		setProgress(t, gdb, u.ID, a2[g][0], 2, nil)
		setProgress(t, gdb, u.ID, a2[g][1], 1, nil)
	}
	hepsiBugunAcik(t, gdb, u.ID, "A1:1", "A1:2", "A1:3", "A1:4", "A2:4")

	for n := 1; n <= 3; n++ {
		if g := groupOf(t, app, "A2", n); g.IsUnlocked || !g.HasStarted || g.LockReason != services.LockReasonPreviousLevel {
			t.Errorf("A2 grup %d = %+v, beklenen başlanmış ve kilitli previous_level", n, g)
		}
	}
	if og := openGroups(t, app); og.OpenGroupCount != 0 || len(og.OpenGroups) != 0 {
		t.Errorf("open-groups = %+v, beklenen boş (A2 grupları sayılmaz)", og)
	}

	// A1'de sınır yalnızca A1 gruplarıyla dolar.
	seeGroup(t, app, a1[0])
	seeGroup(t, app, a1[1])
	if g := groupOf(t, app, "A1", 3); !g.IsUnlocked || g.LockReason != "" {
		t.Errorf("A1 grup 3 = %+v, beklenen açık", g)
	}
	seeGroup(t, app, a1[2])
	if g := groupOf(t, app, "A1", 4); g.IsUnlocked || g.LockReason != services.LockReasonOpenGroupLimit {
		t.Errorf("A1 grup 4 = %+v, beklenen kilitli open_group_limit", g)
	}
	og := openGroups(t, app)
	if og.OpenGroupCount != 3 || openGroupKeys(og) != "A1:1,A1:2,A1:3" {
		t.Errorf("open-groups = %+v, beklenen A1:1,A1:2,A1:3", og)
	}
	// Önceki seviyesi bitmemiş başlanmamış grup başlasa da sayılmaz: sınır
	// ona uygulanmaz, ret nedeni haritadaki gibi önceki seviyedir.
	status, body := send(t, app, learnRequest(fiber.Map{"wordId": a2[3][0].ID}))
	if status != fiber.StatusForbidden || gateCode(body) != "gate_required" {
		t.Errorf("A2:4 learn status = %d (body: %v), beklenen 403 gate_required (map_locked)", status, body)
	}

	// A1 tamamlanınca A2'nin başlanmış grupları açılır ve sayılır.
	if err := gdb.Model(&models.WordProgress{}).Where("user_id = ? AND level = ?", u.ID, "A1").
		Update("box", 2).Error; err != nil {
		t.Fatalf("A1 kayıtları güncellenemedi: %v", err)
	}
	for _, w := range a1[3] {
		setProgress(t, gdb, u.ID, w, 2, nil)
	}
	og = openGroups(t, app)
	if og.OpenGroupCount != 3 || openGroupKeys(og) != "A2:1,A2:2,A2:3" {
		t.Errorf("A1 bitince open-groups = %+v, beklenen A2:1,A2:2,A2:3", og)
	}
	if g := groupOf(t, app, "A2", 1); !g.IsUnlocked || g.LockReason != "" {
		t.Errorf("A1 bitince A2 grup 1 = %+v, beklenen açık", g)
	}
	if g := groupOf(t, app, "A2", 4); g.IsUnlocked || g.LockReason != services.LockReasonOpenGroupLimit {
		t.Errorf("A1 bitince A2 grup 4 = %+v, beklenen kilitli open_group_limit", g)
	}
}

func TestAcikGrup_SozlugeEklenenGrupKullaniciyiTakmaz(t *testing.T) {
	// A1 tamamlanmış (rozet yok), A2'de üç açık grup var: sınır dolu. Sözlüğe
	// sonradan A1'e bir grup eklenince A1 yeniden tamamlanmamış sayılır ve
	// A2 grupları previous_level ile kilitlenir. Sayılmadıkları için A1'in
	// yeni grubu açılır; kullanıcı onu bitirince A2'ye döner.
	gdb, u, app := newOpenGroupTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 2)
	a2 := testLevel(t, gdb, "A2", 2, 2, 2)
	for _, w := range a1[0] {
		setProgress(t, gdb, u.ID, w, 2, nil)
	}
	for g := 0; g < 3; g++ {
		setProgress(t, gdb, u.ID, a2[g][0], 2, nil)
		setProgress(t, gdb, u.ID, a2[g][1], 1, nil)
	}
	hepsiBugunAcik(t, gdb, u.ID, "A1:2")
	if og := openGroups(t, app); og.OpenGroupCount != 3 {
		t.Fatalf("kurulum: openGroupCount = %d, beklenen 3", og.OpenGroupCount)
	}

	added := []models.DictionaryWord{
		testDictWord(t, gdb, "itest_A1_2_1", "A1", 2, 1),
		testDictWord(t, gdb, "itest_A1_2_2", "A1", 2, 2),
	}
	if g := groupOf(t, app, "A2", 1); g.IsUnlocked || g.LockReason != services.LockReasonPreviousLevel {
		t.Errorf("A2 grup 1 = %+v, beklenen kilitli previous_level", g)
	}
	if og := openGroups(t, app); og.OpenGroupCount != 0 || len(og.OpenGroups) != 0 {
		t.Errorf("open-groups = %+v, beklenen boş", og)
	}
	if g := groupOf(t, app, "A1", 2); !g.IsUnlocked || g.LockReason != "" {
		t.Errorf("eklenen A1 grup 2 = %+v, beklenen açık", g)
	}
	for _, w := range added {
		if status, body := send(t, app, learnRequest(fiber.Map{"wordId": w.ID})); status != fiber.StatusOK {
			t.Fatalf("eklenen grup learn %s status = %d (body: %v)", w.ID, status, body)
		}
	}
	og := openGroups(t, app)
	if og.OpenGroupCount != 3 || openGroupKeys(og) != "A2:1,A2:2,A2:3" {
		t.Errorf("A1 yeniden bitince open-groups = %+v, beklenen A2:1,A2:2,A2:3", og)
	}
}

func TestAcikGrup_GrupAcilisiSinirDoluykenHakYakmaz(t *testing.T) {
	gdb, u, app := newOpenGroupTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 2, 2, 2, 2)
	// Grup 1..3 dün açılıp görüldü; bugün hiçbir grup açılmadı.
	hepsiBugunAcik(t, gdb, u.ID, "A1:1", "A1:2", "A1:3")
	for g := 0; g < 3; g++ {
		seeGroup(t, app, a1[g])
	}
	setUsage(t, gdb, u.ID, map[string]any{
		"groups_started_today_count":  0,
		"groups_unlocked_by_ad_today": 0,
		"unlocked_groups_today":       "",
	})

	for _, path := range []string{"/users/me/usage/group-started", "/users/me/usage/group-unlocked-by-ad"} {
		status, body := send(t, app, jsonRequest(path, fiber.Map{"level": "A1", "group": 4}))
		if status != fiber.StatusForbidden || gateCode(body) != "open_group_limit" {
			t.Errorf("%s status = %d (body: %v), beklenen 403 open_group_limit", path, status, body)
		}
	}
	usr := loadUser(t, gdb, u.ID)
	if usr.GroupsStartedTodayCount != 0 || usr.GroupsUnlockedByAdToday != 0 || usr.UnlockedGroupsToday != "" {
		t.Errorf("reddedilen açılış sayaç yazdı: started=%d ad=%d set=%q",
			usr.GroupsStartedTodayCount, usr.GroupsUnlockedByAdToday, usr.UnlockedGroupsToday)
	}
	// Açık bir gruba yeniden giriş (bugünün ücretsiz hakkı) sınırdan etkilenmez.
	if status, body := send(t, app, jsonRequest("/users/me/usage/group-started", fiber.Map{"level": "A1", "group": 2})); status != fiber.StatusOK {
		t.Errorf("açık gruba group-started status = %d (body: %v)", status, body)
	}
}

func TestAcikGrup_LogKipindeKabulVeTekSatir(t *testing.T) {
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newOpenGroupTest(t, config.GateLog)
	a1 := threeOpenGroups(t, gdb, u.ID, app)

	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": a1[3][0].ID})); status != fiber.StatusOK {
		t.Fatalf("log kipinde status = %d (body: %v)", status, body)
	}
	if l := kayit.ihlaller(); len(l) != 1 || !strings.Contains(l[0], "reason=open_group_limit") {
		t.Errorf("ihlal satırları = %v, beklenen tek open_group_limit", l)
	}
}

func TestAcikGrup_TestModuSinirUygulamaz(t *testing.T) {
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test", GateEnforcement: config.GateEnforce, TestModeUnlockAll: true}, nil, nil)
	app := openGroupApp(h, u.ID)
	a1 := threeOpenGroups(t, gdb, u.ID, app)

	if g := groupOf(t, app, "A1", 4); !g.IsUnlocked || g.LockReason != "" {
		t.Errorf("test modunda grup 4 = %+v, beklenen açık", g)
	}
	if status, body := send(t, app, learnRequest(fiber.Map{"wordId": a1[3][0].ID})); status != fiber.StatusOK {
		t.Errorf("test modunda learn status = %d (body: %v)", status, body)
	}
	// Sayaç yine doğru söylenir.
	if og := openGroups(t, app); og.OpenGroupCount != 4 {
		t.Errorf("openGroupCount = %d, beklenen 4", og.OpenGroupCount)
	}
}

func TestAcikGrup_EszamanliIkiYeniGrupSiniriAsamaz(t *testing.T) {
	// Eski kayıtlarla iki başlanmamış grup aynı anda haritada açık olabilir:
	// A1:1 ve A1:3 görülmüş açık gruplar (2), A1:2 ve A1:4 başlanmamış ve
	// açık. İkisine aynı anda gelen ilk kelimelerden yalnızca biri kabul
	// edilir; diğeri dördüncü açık grubu açardı.
	kapiRaporunuYakala(t)
	gdb, u, app := newOpenGroupTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 2, 2, 2, 2)
	hepsiBugunAcik(t, gdb, u.ID, "A1:1", "A1:2", "A1:3", "A1:4")
	for _, g := range []int{0, 2} {
		setProgress(t, gdb, u.ID, a1[g][0], 2, nil)
		setProgress(t, gdb, u.ID, a1[g][1], 1, nil)
	}
	for _, n := range []int{2, 4} {
		if g := groupOf(t, app, "A1", n); !g.IsUnlocked {
			t.Fatalf("kurulum: grup %d = %+v, beklenen açık", n, g)
		}
	}

	targets := []models.DictionaryWord{a1[1][0], a1[3][0]}
	statuses := kilitAltindaEszamanli(t, gdb, app, u.ID, len(targets), func(i int) *http.Request {
		return learnRequest(fiber.Map{"wordId": targets[i].ID})
	})
	ok, limited := 0, 0
	for _, s := range statuses {
		switch s {
		case fiber.StatusOK:
			ok++
		case fiber.StatusForbidden:
			limited++
		}
	}
	if ok != 1 || limited != 1 {
		t.Errorf("durumlar = %v, beklenen bir 200 ve bir 403", statuses)
	}
	if og := openGroups(t, app); og.OpenGroupCount != services.MaxOpenGroups {
		t.Errorf("openGroupCount = %d, beklenen %d", og.OpenGroupCount, services.MaxOpenGroups)
	}
}

// --- Yeni istemci yolculuğu ---

func TestAcikGrup_YeniIstemciBilmiyorumlaIlerler(t *testing.T) {
	// Yeni istemci: gruba girer (kapı + current-group), her kelimeyi bir kez
	// cevaplar ("Bilmiyorum" yeni kelimede unknown), grup görülünce gövdeli
	// advance ile sonraki gruba geçer. Üç açık grupta dördüncüye geçemez;
	// geri dönüp bir grubu bitirince geçer. Hiç ihlal üretmez.
	kayit := kapiRaporunuYakala(t)
	gdb, u, app := newOpenGroupTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 2, 2, 2, 2)
	setPointer(t, gdb, u.ID, "A1", 1)

	enter := func(group int) {
		t.Helper()
		status, usage := send(t, app, httptest.NewRequest(http.MethodGet, "/users/me/usage", nil))
		if status != fiber.StatusOK {
			t.Fatalf("usage status = %d (body: %v)", status, usage)
		}
		key := fmt.Sprintf("A1:%d", group)
		if !containsString(unlockedGroups(t, usage), key) {
			path := "/users/me/usage/group-unlocked-by-ad"
			if intField(t, usage, "groupsStartedTodayCount") == 0 {
				path = "/users/me/usage/group-started"
			}
			if status, body := send(t, app, jsonRequest(path, fiber.Map{"level": "A1", "group": group})); status != fiber.StatusOK {
				t.Fatalf("%s status = %d (body: %v)", path, status, body)
			}
		}
		if status, body := send(t, app, currentGroupRequest(fiber.Map{"level": "A1", "group": group})); status != fiber.StatusOK {
			t.Fatalf("current-group status = %d (body: %v)", status, body)
		}
	}
	advance := func(group int) map[string]any {
		t.Helper()
		status, body := send(t, app, advanceRequest(fiber.Map{"level": "A1", "group": group}))
		if status != fiber.StatusOK {
			t.Fatalf("advance status = %d (body: %v)", status, body)
		}
		return body
	}

	for group := 1; group <= 3; group++ {
		if g := groupOf(t, app, "A1", group); !g.IsUnlocked {
			t.Fatalf("grup %d = %+v, beklenen açık", group, g)
		}
		enter(group)
		seeGroup(t, app, a1[group-1])
		body := advance(group)
		if group < 3 && body["blockedReason"] != "" {
			t.Fatalf("grup %d sonrası advance = %v", group, body)
		}
		// Üçüncü grup görülünce üç açık grup var: işaretçi grup 4'e geçmez,
		// istemci "önce açık gruplarını bitir" der.
		if group == 3 && (body["blockedReason"] != "open_group_limit" || intField(t, body, "newGroup") != 3) {
			t.Fatalf("grup 3 sonrası advance = %v, beklenen blockedReason open_group_limit, A1:3", body)
		}
	}
	assertPointer(t, gdb, u.ID, "A1", 3)
	if g := groupOf(t, app, "A1", 4); g.IsUnlocked || g.LockReason != services.LockReasonOpenGroupLimit {
		t.Fatalf("grup 4 = %+v, beklenen open_group_limit", g)
	}

	// Kullanıcı grup 2'ye döner ve kalan kelimeyi bilir: grup 2 tamamlanır.
	// Geride bir gruba giriş işaretçiyi geri almaz.
	enter(2)
	if status, body := send(t, app, progressSwipe(a1[1][1].ID, "right")); status != fiber.StatusOK {
		t.Fatalf("swipe status = %d (body: %v)", status, body)
	}
	assertPointer(t, gdb, u.ID, "A1", 3)
	if g := groupOf(t, app, "A1", 4); !g.IsUnlocked {
		t.Fatalf("grup 2 bitince grup 4 = %+v, beklenen açık", g)
	}
	// Yer açıldı: aynı advance isteği işaretçiyi şimdi ilerletir.
	if body := advance(3); body["blockedReason"] != "" || intField(t, body, "newGroup") != 4 {
		t.Fatalf("yer açılınca advance = %v, beklenen A1:4", body)
	}
	enter(4)
	seeGroup(t, app, a1[3])
	if l := kayit.ihlaller(); len(l) != 0 {
		t.Errorf("dürüst yeni istemci ihlal üretti: %v", l)
	}
}

func TestAdvanceGroup_GovdesizGorulenGrupIlerletmez(t *testing.T) {
	// Eski istemci "Bilmiyorum"u yazmaz; görülmüş ama tamamlanmamış grup
	// ancak eski kayıtlarla oluşur. Gövdesiz advance eskisi gibi yalnızca
	// tamamlanmış grupta ilerler.
	kisaBekleme(t)
	gdb, u, app := newOpenGroupTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 2, 2)
	setProgress(t, gdb, u.ID, a1[0][0], 2, nil)
	setProgress(t, gdb, u.ID, a1[0][1], 1, nil)
	setPointer(t, gdb, u.ID, "A1", 1)

	status, body := send(t, app, httptest.NewRequest(http.MethodPost, "/users/me/advance-group", nil))
	if status != fiber.StatusOK || body["blockedReason"] != "" {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	assertPointer(t, gdb, u.ID, "A1", 1)
}

func TestAcikGrup_OncekiSeviyeBitinceSinirAsilirAdvanceBekler(t *testing.T) {
	// Ürün kararı: önceki seviyesi bitmemiş seviyede sınır uygulanmaz.
	// Taşınmış veri: A1 bitmeden A2'nin beş grubunda kayıt var (görülmüş, her
	// birinde bir kelime kutu 1'de). A1 bitmemişken gövdeli advance A2'de
	// sınıra takılmaz. A1 tamamlanınca beş grup birden sayılır (5/3, kabul
	// edilen durum); sayı 3'ün altına inene kadar sunucu yeni grup açtırmaz.
	gdb, u, app := newOpenGroupTest(t, config.GateEnforce)
	a1 := testLevel(t, gdb, "A1", 2)
	a2 := testLevel(t, gdb, "A2", 2, 2, 2, 2, 2, 2)
	for g := 0; g < 5; g++ {
		setProgress(t, gdb, u.ID, a2[g][0], 2, nil)
		setProgress(t, gdb, u.ID, a2[g][1], 1, nil)
	}
	setPointer(t, gdb, u.ID, "A2", 5)
	body := fiber.Map{"level": "A2", "group": 5}

	status, resp := send(t, app, advanceRequest(body))
	assertAdvanceResult(t, gdb, u.ID, status, resp, "A2", 6, false, "")

	for _, w := range a1[0] {
		setProgress(t, gdb, u.ID, w, 2, nil)
	}
	setPointer(t, gdb, u.ID, "A2", 5)
	if og := openGroups(t, app); og.OpenGroupCount != 5 || og.MaxOpenGroups != services.MaxOpenGroups {
		t.Fatalf("A1 bitince open-groups = %+v, beklenen 5/3", og)
	}
	if g := groupOf(t, app, "A2", 6); g.IsUnlocked || g.LockReason != services.LockReasonOpenGroupLimit {
		t.Errorf("A2 grup 6 = %+v, beklenen kilitli open_group_limit", g)
	}

	// Açık gruplar birer birer tamamlanır: 4/3 ve 3/3'te de işaretçi yerinde.
	for g := 0; g < 3; g++ {
		status, resp = send(t, app, advanceRequest(body))
		assertAdvanceResult(t, gdb, u.ID, status, resp, "A2", 5, false, "open_group_limit")
		if err := gdb.Model(&models.WordProgress{}).Where("user_id = ? AND word_id = ?", u.ID, a2[g][1].ID).
			Update("box", 2).Error; err != nil {
			t.Fatalf("kayıt güncellenemedi: %v", err)
		}
	}
	if og := openGroups(t, app); og.OpenGroupCount != 2 {
		t.Fatalf("openGroupCount = %d, beklenen 2", og.OpenGroupCount)
	}
	status, resp = send(t, app, advanceRequest(body))
	assertAdvanceResult(t, gdb, u.ID, status, resp, "A2", 6, false, "")
}
