package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// Bu dosya yayındaki (main dalı) Flutter istemcisinin grup akışını yeni
// sunucuya karşı birebir yeniden oynatır: group_info_bottomsheet.dart gruba
// girmeden önce PUT /users/me/current-group gönderir, learning_swipe_page.dart
// kutu 1'deki ya da yeni kelimeleri gösterir, sağa kaydırmada learn (yeni
// kelime) ya da swipe right (kayıtlı kelime), sola kaydırmada yalnızca kayıtlı
// kelimeye swipe left gönderir; liste boşalınca (sola kaydırılanlar da
// listeden çıkar) gövdesiz POST /users/me/advance-group çağırır.
// Kurulum için usage_integration_test.go başındaki nota bakın.

func eskiIstemciApp(h *Handlers, userID string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	inject := func(c *fiber.Ctx) error {
		c.Locals("userID", userID)
		return c.Next()
	}
	app.Put("/users/me/current-group", inject, h.SetCurrentGroup)
	app.Post("/users/me/advance-group", inject, h.AdvanceGroup)
	app.Get("/users/me/usage", inject, h.GetDailyUsage)
	app.Post("/users/me/usage/group-started", inject, h.UnlockGroup("groups_started_today_count"))
	app.Post("/users/me/usage/group-unlocked-by-ad", inject, h.UnlockGroup("groups_unlocked_by_ad_today"))
	app.Get("/progress/group", inject, h.GetProgressForGroup)
	app.Post("/progress/learn", inject, h.LearnWord)
	app.Post("/progress/swipe", inject, h.Swipe)
	app.Get("/map/levels/:level/groups", inject, h.LevelGroups)
	return app
}

// eskiIstemci, yayındaki istemcinin grup akışıdır.
type eskiIstemci struct {
	t   *testing.T
	app *fiber.App
}

func newEskiIstemciTest(t *testing.T) (*gorm.DB, *models.User, *eskiIstemci) {
	t.Helper()

	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test"}, nil, nil)
	return gdb, u, &eskiIstemci{t: t, app: eskiIstemciApp(h, u.ID)}
}

func (k *eskiIstemci) ok(req *http.Request) map[string]any {
	k.t.Helper()
	status, body := send(k.t, k.app, req)
	if status != fiber.StatusOK {
		k.t.Fatalf("%s %s status = %d (body: %v)", req.Method, req.URL.Path, status, body)
	}
	return body
}

// enterGroup: haritadan gruba giriş (current-group) ve sayfa açılışındaki
// kapı (günün ilk grubu ücretsiz, sonrakiler reklamla).
func (k *eskiIstemci) enterGroup(level string, group int) {
	k.t.Helper()
	k.ok(currentGroupRequest(fiber.Map{"level": level, "group": group}))
	u := k.ok(httptest.NewRequest(http.MethodGet, "/users/me/usage", nil))
	if containsString(unlockedGroups(k.t, u), fmt.Sprintf("%s:%d", level, group)) {
		return
	}
	body := fiber.Map{"level": level, "group": group}
	if intField(k.t, u, "groupsStartedTodayCount") == 0 {
		k.ok(jsonRequest("/users/me/usage/group-started", body))
		return
	}
	k.ok(jsonRequest("/users/me/usage/group-unlocked-by-ad", body))
}

// round: grubun kutu 1'deki ya da yeni kelimeleri tek tek kaydırılır; left'te
// olanlar "Bilmiyorum", diğerleri "Biliyorum". Liste boşalınca gövdesiz
// advance-group gider; yanıtı döner.
func (k *eskiIstemci) round(words []models.DictionaryWord, left map[string]bool) map[string]any {
	k.t.Helper()
	w0 := words[0]
	status, raw := getJSON(k.t, k.app, fmt.Sprintf("/progress/group?level=%s&group=%d", w0.Level, w0.Grp))
	var progress []models.WordProgress
	if status != fiber.StatusOK || json.Unmarshal(raw, &progress) != nil {
		k.t.Fatalf("progress/group = %d %s", status, raw)
	}
	box := map[string]int{}
	for _, p := range progress {
		box[p.WordID] = p.Box
	}
	for _, w := range words {
		b, has := box[w.ID]
		if has && b > 1 {
			continue // kutu 1'de değil: listede gösterilmez
		}
		switch {
		case left[w.ID] && has:
			k.ok(jsonRequest("/progress/swipe", fiber.Map{"wordId": w.ID, "direction": "left"}))
		case left[w.ID]:
			// Yeni kelime sola: istemci hiçbir şey göndermez.
		case has:
			k.ok(jsonRequest("/progress/swipe", fiber.Map{"wordId": w.ID, "direction": "right"}))
		default:
			k.ok(jsonRequest("/progress/learn", fiber.Map{
				"wordId": w.ID, "level": w.Level, "group": w.Grp, "english": w.English, "turkish": w.Turkish,
			}))
		}
	}
	return k.ok(advanceRequest(nil))
}

// assertAdvance, gövdesiz advance yanıtının işaretçiyi ve seviye değişimini
// doğru bildirdiğini denetler (istemci "yeni seviye" diyaloğunu user'daki
// seviyeden çıkarır).
func assertAdvance(t *testing.T, body map[string]any, wantLevel string, wantGroup int, wantChanged bool) {
	t.Helper()
	if body["levelChanged"] != wantChanged || body["newLevel"] != wantLevel || intField(t, body, "newGroup") != wantGroup {
		t.Errorf("advance yanıtı = %v, beklenen levelChanged=%v %s:%d", body, wantChanged, wantLevel, wantGroup)
	}
	uf := userField(t, body)
	if uf["currentLevel"] != wantLevel || int(uf["currentGroup"].(float64)) != wantGroup {
		t.Errorf("yanıttaki kullanıcı = %v:%v, beklenen %s:%d", uf["currentLevel"], uf["currentGroup"], wantLevel, wantGroup)
	}
}

// kisaBekleme, gövdesiz advance'in yazım bekleme süresini test boyunca
// kısaltır: işaretçinin ilerlememesi beklenen istekler suiti yavaşlatmasın.
func kisaBekleme(t *testing.T) {
	t.Helper()
	onceki := legacyAdvanceSettle
	legacyAdvanceSettle = 150 * time.Millisecond
	t.Cleanup(func() { legacyAdvanceSettle = onceki })
}

func kelimeKumesi(ws []models.DictionaryWord) map[string]bool {
	m := map[string]bool{}
	for _, w := range ws {
		m[w.ID] = true
	}
	return m
}

func TestEskiIstemci_TamGrupBirIleri(t *testing.T) {
	// Olağan akış: işaretçinin gösterdiği grup tamamen bilinir, gövdesiz
	// advance bir ileri götürür.
	gdb, u, k := newEskiIstemciTest(t)
	a1 := testLevel(t, gdb, "A1", 3, 3, 3)
	testLevel(t, gdb, "A2", 3)

	k.enterGroup("A1", 1)
	assertAdvance(t, k.round(a1[0], nil), "A1", 2, false)
	assertPointer(t, gdb, u.ID, "A1", 2)
}

func TestEskiIstemci_YarimKalanGrupIsaretciyiIlerletmez(t *testing.T) {
	// Tur sonunda "Bilmiyorum" denen kelimeler kutu 1'de kalır; grup
	// tamamlanmadı, işaretçi yerinde kalır. Kalan kelimeler bitince ilerler.
	kisaBekleme(t)
	gdb, u, k := newEskiIstemciTest(t)
	a1 := testLevel(t, gdb, "A1", 3, 3, 3)
	testLevel(t, gdb, "A2", 3)

	k.enterGroup("A1", 1)
	assertAdvance(t, k.round(a1[0], kelimeKumesi(a1[0][1:2])), "A1", 1, false)
	assertPointer(t, gdb, u.ID, "A1", 1)

	// Yanıtı kaybolan advance yinelenir: yine değişmez.
	assertAdvance(t, k.ok(advanceRequest(nil)), "A1", 1, false)

	k.enterGroup("A1", 1)
	assertAdvance(t, k.round(a1[0], nil), "A1", 2, false)
	assertPointer(t, gdb, u.ID, "A1", 2)
}

func TestEskiIstemci_GeridekiGrubuTekrarlamakIsaretciyiKaydirmaz(t *testing.T) {
	// İnceleme senaryosu: eski sunucunun koşulsuz advance'i işaretçiyi A1:5
	// yarımken 6'ya almıştı (burada A1:2 yarımken 3). Kullanıcı kalan
	// kelimeler için geri gruba döner: current-group geri yazmaz, gövdesiz
	// advance da işaretçideki grup (3) tamamlanmadığı için işaretçiye dokunmaz.
	// Eskiden her tekrar turu işaretçiyi bir grup, seviye sonunda bir seviye
	// ileri atıyordu.
	kisaBekleme(t)
	gdb, u, k := newEskiIstemciTest(t)
	a1 := testLevel(t, gdb, "A1", 3, 3, 3)
	testLevel(t, gdb, "A2", 3)
	completeGroup(t, gdb, u.ID, a1[0])
	setProgress(t, gdb, u.ID, a1[1][0], 2, nil)
	setProgress(t, gdb, u.ID, a1[1][1], 1, nil)
	setPointer(t, gdb, u.ID, "A1", 3)

	// İlk tekrar turu yine "Bilmiyorum"la biter.
	k.enterGroup("A1", 2)
	assertPointer(t, gdb, u.ID, "A1", 3)
	assertAdvance(t, k.round(a1[1], kelimeKumesi(a1[1][1:2])), "A1", 3, false)
	assertPointer(t, gdb, u.ID, "A1", 3)

	// İkinci tekrar turu grubu tamamlar: işaretçi yine 3'te kalır.
	k.enterGroup("A1", 2)
	assertAdvance(t, k.round(a1[1], nil), "A1", 3, false)
	assertPointer(t, gdb, u.ID, "A1", 3)

	// Harita ve işaretçi uyumlu: grup 2 tamam, grup 3 açık.
	groups := levelGroups(t, k.app, "A1")
	if !groups[1].IsCompleted || !groups[2].IsUnlocked {
		t.Errorf("harita = %+v, beklenen grup 2 tamam, grup 3 açık", groups)
	}
}

func TestEskiIstemci_SeviyeSonuYalnizcaSonGrupBitinceSonrakiSeviye(t *testing.T) {
	kisaBekleme(t)
	gdb, u, k := newEskiIstemciTest(t)
	a1 := testLevel(t, gdb, "A1", 3, 3)
	testLevel(t, gdb, "A2", 3)
	completeGroup(t, gdb, u.ID, a1[0])
	setPointer(t, gdb, u.ID, "A1", 2)

	// Son grup yarım: sahte "seviye atladın" yok, işaretçi A1'de.
	k.enterGroup("A1", 2)
	assertAdvance(t, k.round(a1[1], kelimeKumesi(a1[1][:1])), "A1", 2, false)
	assertPointer(t, gdb, u.ID, "A1", 2)

	// Son grup tamamlanınca sonraki seviye.
	k.enterGroup("A1", 2)
	assertAdvance(t, k.round(a1[1], nil), "A2", 1, true)
	assertPointer(t, gdb, u.ID, "A2", 1)
}

func TestAdvanceGroup_GovdesizC2SonGrubuBitmedenDegismez(t *testing.T) {
	kisaBekleme(t)
	gdb, u, app := newGroupTest(t, nil)
	levels := seedGroupLevels(t, gdb)
	setPointer(t, gdb, u.ID, "C2", 2)
	setProgress(t, gdb, u.ID, levels["C2"][1][0], 2, nil)

	status, body := send(t, app, advanceRequest(nil))
	if status != fiber.StatusOK {
		t.Fatalf("status = %d (body: %v)", status, body)
	}
	assertAdvance(t, body, "C2", 2, false)
	assertPointer(t, gdb, u.ID, "C2", 2)
}

func TestEskiIstemci_SonKelimeninYazimiAdvancedanSonraGelse(t *testing.T) {
	// Eski istemci son kartın learn kaydını beklemeden advance'i de gönderir;
	// advance önce varırsa grup bir an tamamlanmamış görünür. Sunucu yazımın
	// commit olmasını kısa bir süre bekler.
	gdb, u, k := newEskiIstemciTest(t)
	a1 := testLevel(t, gdb, "A1", 2, 2)
	k.enterGroup("A1", 1)
	k.ok(learnRequest(fiber.Map{"wordId": a1[0][0].ID}))

	done := make(chan map[string]any, 1)
	go func() {
		res, err := k.app.Test(advanceRequest(nil), -1)
		if err != nil {
			done <- nil
			return
		}
		defer res.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(res.Body).Decode(&body)
		done <- body
	}()
	time.Sleep(200 * time.Millisecond)
	k.ok(learnRequest(fiber.Map{"wordId": a1[0][1].ID}))

	body := <-done
	if body == nil {
		t.Fatal("advance isteği başarısız")
	}
	assertAdvance(t, body, "A1", 2, false)
	assertPointer(t, gdb, u.ID, "A1", 2)
}
