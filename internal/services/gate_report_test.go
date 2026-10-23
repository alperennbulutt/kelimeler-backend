package services

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"kelimeler-backend/internal/config"
)

// kayitliRaporlayici, log satırlarını ve Sentry mesajlarını biriktirir.
type kayitliRaporlayici struct {
	mu       sync.Mutex
	lines    []string
	captures []string
}

func (k *kayitliRaporlayici) reporter() *GateReporter {
	return NewGateReporterWith(
		func(format string, args ...any) {
			k.mu.Lock()
			defer k.mu.Unlock()
			k.lines = append(k.lines, fmt.Sprintf(format, args...))
		},
		func(msg string) {
			k.mu.Lock()
			defer k.mu.Unlock()
			k.captures = append(k.captures, msg)
		},
	)
}

func learnViolation(user string, mode config.GateEnforcement) GateViolation {
	return GateViolation{
		Kind: GateKindLearn, Reason: GateReasonGroupNotUnlocked, Mode: mode,
		UserID: user, RequestID: "req-1", WordID: "A1_2_5", Level: "A1", Group: 2,
	}
}

func TestGateReporter_SatirBicimi(t *testing.T) {
	k := &kayitliRaporlayici{}
	r := k.reporter()
	now := time.Now()

	r.Report(learnViolation("u1", config.GateLog), now)
	r.Report(GateViolation{
		Kind: GateKindReview, Reason: GateReasonReviewCheckpoint, Mode: config.GateEnforce,
		UserID: "u1", RequestID: "req-2", ReviewAnswers: 30, ReviewAdPasses: 0,
	}, now)
	// Grup açılışı ihlalinde kelime yoktur.
	r.Report(GateViolation{
		Kind: GateKindLearn, Reason: GateReasonFreeGroupReused, Mode: config.GateEnforce,
		UserID: "u2", RequestID: "req-3", Level: "A1", Group: 3,
	}, now)

	want := []string{
		`gate_violation kind=learn reason=group_not_unlocked mode=log user=u1 request_id=req-1 word="A1_2_5" group=A1:2 count=1`,
		`gate_violation kind=review reason=review_checkpoint mode=enforce user=u1 request_id=req-2 answers_today=30 passes=0 count=1`,
		`gate_violation kind=learn reason=free_group_reused mode=enforce user=u2 request_id=req-3 group=A1:3 count=1`,
	}
	if strings.Join(k.lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("log satırları:\n%s\nbeklenen:\n%s", strings.Join(k.lines, "\n"), strings.Join(want, "\n"))
	}
	// Enforce kipinde ret beklenen davranıştır; Sentry'ye yalnızca log kipi gider.
	if len(k.captures) != 1 || k.captures[0] != "reklam kapısı ihlali (kip=log): learn/group_not_unlocked" {
		t.Errorf("Sentry mesajları = %q", k.captures)
	}
}

func TestGateReporter_KullaniciBasinaGundeBirSentryVeSinirliLog(t *testing.T) {
	kullanimKonumu(t, "Europe/Istanbul")
	k := &kayitliRaporlayici{}
	r := k.reporter()
	now := utc(t, "2026-10-23T12:00:00Z")

	for i := 0; i < 250; i++ {
		r.Report(learnViolation("u1", config.GateLog), now)
	}
	// İlk 10 satır + 100. ve 200. ihlal.
	if len(k.lines) != gateLogFirstN+2 {
		t.Errorf("log satırı = %d, beklenen %d", len(k.lines), gateLogFirstN+2)
	}
	if last := k.lines[len(k.lines)-1]; !strings.HasSuffix(last, "count=200") {
		t.Errorf("son satır = %q, count=200 bekleniyordu", last)
	}
	if len(k.captures) != 1 {
		t.Errorf("Sentry olayı = %d, beklenen 1", len(k.captures))
	}

	// Başka kullanıcı ve başka tür ayrı sayılır.
	r.Report(learnViolation("u2", config.GateLog), now)
	v := learnViolation("u1", config.GateLog)
	v.Kind, v.Reason = GateKindReview, GateReasonReviewCheckpoint
	r.Report(v, now)
	if len(k.captures) != 3 {
		t.Errorf("Sentry olayı = %d, beklenen 3 (u2 ve u1/review ayrı)", len(k.captures))
	}

	// İstanbul gece yarısında sayaçlar sıfırlanır.
	r.Report(learnViolation("u1", config.GateLog), utc(t, "2026-10-23T21:00:00Z"))
	if len(k.captures) != 4 {
		t.Errorf("gün dönümünde yeni Sentry olayı beklenirdi (olay = %d)", len(k.captures))
	}
	if last := k.lines[len(k.lines)-1]; !strings.HasSuffix(last, "count=1") {
		t.Errorf("gün dönümünden sonraki satır = %q, count=1 bekleniyordu", last)
	}
}

func TestGateReporter_EszamanliRaporlarSayilir(t *testing.T) {
	k := &kayitliRaporlayici{}
	r := k.reporter()
	now := time.Now()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Report(learnViolation("u1", config.GateLog), now)
		}()
	}
	wg.Wait()
	if len(k.captures) != 1 {
		t.Errorf("Sentry olayı = %d, beklenen 1", len(k.captures))
	}
	if len(k.lines) != gateLogFirstN {
		t.Errorf("log satırı = %d, beklenen %d", len(k.lines), gateLogFirstN)
	}
}
