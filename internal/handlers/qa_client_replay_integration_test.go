package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/middleware"
	"kelimeler-backend/internal/models"
	"kelimeler-backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// QA: yayındaki (main) ve yeni (ekip/sprint-1) Flutter istemcisinin grup
// akışlarını, kapının üç kipinde uzun bir yolculuk boyunca yeniden oynatır.
// Her adımda işaretçinin beklenen yerde olduğu denetlenir: işaretçi asla
// tamamlanmamış bir grubun ötesine atlamaz, başa sarmaz, seviye yalnızca son
// grup gerçekten bitince değişir. Dürüst istemci hiç 403 ya da kapı ihlali
// (free_group_reused dahil) üretmez.
//
// Eski istemci (main): group_info_bottomsheet.dart gruba girmeden önce
// PUT current-group; learning_swipe_page.dart GET usage → group-started
// (yalnızca groupsStartedTodayCount == 0) ya da reklam; sola kaydırılan yeni
// kelime hiçbir şey göndermez ve listeden çıkar; liste boşalınca gövdesiz
// advance-group.
//
// Yeni istemci (ekip/sprint-1): GET usage → kapı; işaretçi yalnızca grup
// ilerideyse PUT current-group; sola kaydırılan kelime destenin sonuna döner
// (grup ancak hepsi bilinince biter); işaretçinin gerisindeki grupta advance
// gönderilmez, aksi halde gövdeli advance {level, group}.

type qaIstemci struct {
	t          *testing.T
	app        *fiber.App
	gdb        *gorm.DB
	userID     string
	yeni       bool
	rejections int
}

func qaIstemciApp(h *Handlers, userID string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	inject := func(c *fiber.Ctx) error {
		c.Locals("userID", userID)
		return c.Next()
	}
	app.Put("/users/me/current-group", inject, h.SetCurrentGroup)
	app.Post("/users/me/advance-group", inject, h.AdvanceGroup)
	app.Get("/users/me/usage", inject, h.GetDailyUsage)
	app.Post("/users/me/usage/group-started", inject, h.UnlockGroup(freeGroupColumn))
	app.Post("/users/me/usage/group-unlocked-by-ad", inject, h.UnlockGroup("groups_unlocked_by_ad_today"))
	app.Get("/progress/group", inject, h.GetProgressForGroup)
	app.Post("/progress/learn", inject, h.LearnWord)
	app.Post("/progress/swipe", inject, h.Swipe)
	app.Get("/map/levels/:level/groups", inject, h.LevelGroups)
	return app
}

func newQAIstemci(t *testing.T, mode config.GateEnforcement, yeni bool) (*gorm.DB, *qaIstemci, *kapiKaydi) {
	t.Helper()
	kisaBekleme(t)
	kayit := kapiRaporunuYakala(t)
	gdb := testDB(t)
	u := testUser(t, gdb)
	h := New(gdb, &config.Config{AppEnv: "test", GateEnforcement: mode}, nil, nil)
	return gdb, &qaIstemci{t: t, app: qaIstemciApp(h, u.ID), gdb: gdb, userID: u.ID, yeni: yeni}, kayit
}

func (k *qaIstemci) ok(req *http.Request) map[string]any {
	k.t.Helper()
	status, body := send(k.t, k.app, req)
	if status == fiber.StatusForbidden {
		k.rejections++
	}
	if status != fiber.StatusOK {
		k.t.Fatalf("%s %s status = %d (body: %v)", req.Method, req.URL.Path, status, body)
	}
	return body
}

// pointer, istemcinin bildiği işaretçidir (oturumdaki AppUser; testte
// sunucudaki değerle aynıdır).
func (k *qaIstemci) pointer() (string, int) {
	u := loadUser(k.t, k.gdb, k.userID)
	return u.CurrentLevel, u.CurrentGroup
}

func (k *qaIstemci) enter(level string, group int) {
	k.t.Helper()
	if !k.yeni {
		k.ok(currentGroupRequest(fiber.Map{"level": level, "group": group}))
	}
	u := k.ok(httptest.NewRequest(http.MethodGet, "/users/me/usage", nil))
	key := fmt.Sprintf("%s:%d", level, group)
	if !containsString(unlockedGroups(k.t, u), key) {
		body := fiber.Map{"level": level, "group": group}
		if intField(k.t, u, "groupsStartedTodayCount") == 0 {
			k.ok(jsonRequest("/users/me/usage/group-started", body))
		} else {
			k.ok(jsonRequest("/users/me/usage/group-unlocked-by-ad", body))
		}
	}
	if k.yeni {
		pl, pg := k.pointer()
		if services.CompareGroupPositions(level, group, pl, pg) > 0 {
			k.ok(currentGroupRequest(fiber.Map{"level": level, "group": group}))
		}
	}
}

// round, grubun kutu 1'deki ya da yeni kelimelerini kaydırır (left: önce
// "Bilmiyorum" denenler) ve tur sonundaki advance yanıtını döner; yeni
// istemci advance göndermediyse nil.
func (k *qaIstemci) round(words []models.DictionaryWord, left map[string]bool) map[string]any {
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
	right := func(w models.DictionaryWord, has bool) {
		if has {
			k.ok(jsonRequest("/progress/swipe", fiber.Map{"wordId": w.ID, "direction": "right"}))
			return
		}
		k.ok(jsonRequest("/progress/learn", fiber.Map{
			"wordId": w.ID, "level": w.Level, "group": w.Grp, "english": w.English, "turkish": w.Turkish,
		}))
	}
	var deck []models.DictionaryWord
	for _, w := range words {
		if b, has := box[w.ID]; !has || b <= 1 {
			deck = append(deck, w)
		}
	}
	var again []models.DictionaryWord
	for _, w := range deck {
		_, has := box[w.ID]
		if !left[w.ID] {
			right(w, has)
			continue
		}
		if has {
			k.ok(jsonRequest("/progress/swipe", fiber.Map{"wordId": w.ID, "direction": "left"}))
		}
		again = append(again, w)
	}
	if !k.yeni {
		return k.ok(advanceRequest(nil))
	}
	// Yeni istemci: bilinmeyen kelime destenin sonuna döner, bu kez bilinir.
	for _, w := range again {
		_, has := box[w.ID]
		right(w, has)
	}
	pl, pg := k.pointer()
	if services.CompareGroupPositions(w0.Level, w0.Grp, pl, pg) < 0 {
		return nil
	}
	return k.ok(advanceRequest(fiber.Map{"level": w0.Level, "group": w0.Grp}))
}

func (k *qaIstemci) expect(body map[string]any, level string, group int, changed bool) {
	k.t.Helper()
	if body != nil {
		assertAdvance(k.t, body, level, group, changed)
	}
	assertPointer(k.t, k.gdb, k.userID, level, group)
}

func TestQA_IstemciYolculugu_IsaretciAtlamazVeIhlalYok(t *testing.T) {
	for _, yeni := range []bool{false, true} {
		for _, mode := range []config.GateEnforcement{config.GateOff, config.GateLog, config.GateEnforce} {
			name := "eski"
			if yeni {
				name = "yeni"
			}
			t.Run(name+"/"+string(mode), func(t *testing.T) {
				gdb, k, kayit := newQAIstemci(t, mode, yeni)
				a1 := testLevel(t, gdb, "A1", 3, 3, 3)
				a2 := testLevel(t, gdb, "A2", 2, 2)

				// 1. Günün ücretsiz grubu; bir yeni kelime "Bilmiyorum".
				k.enter("A1", 1)
				b := k.round(a1[0], kelimeKumesi(a1[0][1:2]))
				if yeni {
					// Yeni istemci grubu ancak hepsi bilinince bitirir.
					k.expect(b, "A1", 2, false)
				} else {
					k.expect(b, "A1", 1, false)
					if g := levelGroups(t, k.app, "A1"); g[0].IsCompleted || g[1].IsUnlocked {
						t.Errorf("yarım grup 1'den sonra harita = %+v", g)
					}
					// Yanıtı kaybolan advance yinelenir: değişmez.
					k.expect(k.ok(advanceRequest(nil)), "A1", 1, false)
					// Aynı gün aynı gruba yeniden giriş: yeni kayıt yok.
					k.enter("A1", 1)
					k.expect(k.round(a1[0], nil), "A1", 2, false)
				}

				// 2. İkinci grup reklamla; yarım tur, sonra tam tur.
				k.enter("A1", 2)
				if !yeni {
					k.expect(k.round(a1[1], kelimeKumesi(a1[1][:1])), "A1", 2, false)
					k.enter("A1", 2)
				}
				k.expect(k.round(a1[1], nil), "A1", 3, false)

				// 3. Firestore'dan kalma kutu 1 kaydı geride bir grubu yeniden
				// açar; kullanıcı onu tekrarlar. İşaretçi (A1:3, başlanmamış)
				// kıpırdamaz; current-group geri yazmaz.
				if err := gdb.Model(&models.WordProgress{}).
					Where("user_id = ? AND word_id = ?", k.userID, a1[0][2].ID).Update("box", 1).Error; err != nil {
					t.Fatalf("kutu yazılamadı: %v", err)
				}
				k.enter("A1", 1)
				k.expect(nil, "A1", 3, false)
				if !yeni {
					k.expect(k.round(a1[0], kelimeKumesi(a1[0][2:3])), "A1", 3, false)
					k.enter("A1", 1)
				}
				k.expect(k.round(a1[0], nil), "A1", 3, false)
				if yeni {
					// Gövdeli advance geride bir grup için gelse de değişmez.
					k.expect(k.ok(advanceRequest(fiber.Map{"level": "A1", "group": 1})), "A1", 3, false)
				}

				// 4. Seviyenin son grubu: yarımken seviye değişmez.
				k.enter("A1", 3)
				if !yeni {
					k.expect(k.round(a1[2], kelimeKumesi(a1[2][2:3])), "A1", 3, false)
					k.enter("A1", 3)
				}
				k.expect(k.round(a1[2], nil), "A2", 1, true)

				// 5. Sonraki seviye; kaybolan advance yinelenince grup atlanmaz.
				k.enter("A2", 1)
				k.expect(k.round(a2[0], nil), "A2", 2, false)
				if yeni {
					k.expect(k.ok(advanceRequest(fiber.Map{"level": "A2", "group": 1})), "A2", 2, false)
				} else {
					k.expect(k.ok(advanceRequest(nil)), "A2", 2, false)
				}

				if k.rejections != 0 {
					t.Errorf("dürüst istemci %d kez 403 aldı", k.rejections)
				}
				if l := kayit.ihlaller(); len(l) != 0 {
					t.Errorf("dürüst istemci ihlal üretti:\n%s", strings.Join(l, "\n"))
				}
				if s := kayit.sentry(); len(s) != 0 {
					t.Errorf("Sentry'ye gitti: %v", s)
				}
			})
		}
	}
}

func TestQA_EskiSunucununIleriAttigiIsaretciGeriAlinmazAtlamaz(t *testing.T) {
	// Eski sunucunun koşulsuz advance'i işaretçiyi A1 yarımken A2:1'e almış.
	// Kullanıcı eski istemciyle A1'in yarım son grubunu iki turda bitirir:
	// işaretçi A2:1'de kalır (A2:2'ye ya da A3'e kaymaz).
	gdb, k, kayit := newQAIstemci(t, config.GateLog, false)
	a1 := testLevel(t, gdb, "A1", 2, 2)
	testLevel(t, gdb, "A2", 2, 2)
	completeGroup(t, gdb, k.userID, a1[0])
	setProgress(t, gdb, k.userID, a1[1][0], 2, nil)
	setPointer(t, gdb, k.userID, "A2", 1)

	k.enter("A1", 2)
	k.expect(nil, "A2", 1, false)
	k.expect(k.round(a1[1], kelimeKumesi(a1[1][1:2])), "A2", 1, false)
	k.enter("A1", 2)
	k.expect(k.round(a1[1], nil), "A2", 1, false)
	if l := kayit.ihlaller(); len(l) != 0 {
		t.Errorf("ihlal üretildi:\n%s", strings.Join(l, "\n"))
	}
}

func TestQA_C2SonundaIsaretciBasaSarmaz(t *testing.T) {
	for _, yeni := range []bool{false, true} {
		t.Run(fmt.Sprintf("yeni=%v", yeni), func(t *testing.T) {
			gdb, k, _ := newQAIstemci(t, config.GateOff, yeni)
			testLevel(t, gdb, "A1", 2)
			c2 := testLevel(t, gdb, "C2", 2, 2)
			setPointer(t, gdb, k.userID, "C2", 1)

			k.enter("C2", 1)
			k.expect(k.round(c2[0], nil), "C2", 2, false)
			k.enter("C2", 2)
			if !yeni {
				k.expect(k.round(c2[1], kelimeKumesi(c2[1][:1])), "C2", 2, false)
				k.enter("C2", 2)
			}
			k.expect(k.round(c2[1], nil), "C2", 0, false)

			// "Tümü tamamlandı"dan sonra: gövdesiz/gövdeli advance ve geride bir
			// gruba current-group işaretçiyi değiştirmez, A1'e sarmaz.
			k.expect(k.ok(advanceRequest(nil)), "C2", 0, false)
			k.expect(k.ok(advanceRequest(fiber.Map{"level": "C2", "group": 2})), "C2", 0, false)
			k.ok(currentGroupRequest(fiber.Map{"level": "C2", "group": 1}))
			k.expect(nil, "C2", 0, false)
			k.ok(currentGroupRequest(fiber.Map{"level": "A1", "group": 1}))
			k.expect(nil, "C2", 0, false)
		})
	}
}

func TestQA_UcretsizGrupIhlaliIstemciSurumunuTasir(t *testing.T) {
	// Değiştirilmiş istemci günün ikinci grubunu ücretsiz açmaya çalışır: log
	// satırı ve Sentry etiketi X-App-Version'ı taşır; başlık yoksa "eski".
	for _, surum := range []string{"", "1.2.0+60"} {
		t.Run("surum="+surum, func(t *testing.T) {
			kayit := kapiRaporunuYakala(t)
			gdb, u, app := newGateTest(t, config.GateLog)
			setUsage(t, gdb, u.ID, map[string]any{"groups_started_today_count": 1, "unlocked_groups_today": "A1:1"})

			req := jsonRequest("/users/me/usage/group-started", fiber.Map{"level": "A1", "group": 2})
			if surum != "" {
				req.Header.Set("X-App-Version", surum)
			}
			if status, body := send(t, app, req); status != fiber.StatusOK {
				t.Fatalf("status = %d (%v)", status, body)
			}
			want := surum
			if want == "" {
				want = "eski"
			}
			l := kayit.ihlaller()
			if len(l) != 1 || !strings.Contains(l[0], "reason=free_group_reused") || !strings.Contains(l[0], " app_version="+want+" ") {
				t.Errorf("ihlal satırı = %v, beklenen free_group_reused app_version=%s", l, want)
			}
			kayit.mu.Lock()
			defer kayit.mu.Unlock()
			if len(kayit.tags) != 1 || kayit.tags[0]["app_version"] != want || kayit.tags[0]["gate_reason"] != "free_group_reused" {
				t.Errorf("Sentry etiketleri = %v", kayit.tags)
			}
		})
	}
}
