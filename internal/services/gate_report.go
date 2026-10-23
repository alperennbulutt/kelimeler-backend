package services

import (
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"kelimeler-backend/internal/config"
	"kelimeler-backend/internal/observability"
)

// GateViolation, raporlanan bir kapı ihlalidir.
type GateViolation struct {
	Kind      string // GateKindLearn | GateKindReview
	Reason    string // GateReason...
	Mode      config.GateEnforcement
	UserID    string
	RequestID string

	// learn
	WordID string
	Level  string
	Group  int

	// review
	ReviewAnswers  int
	ReviewAdPasses int
}

// Raporlama sınırları: kullanıcı başına, tür başına, kullanım günü başına.
const (
	// gateLogFirstN kadar ihlal tek tek loglanır; sonrası yalnızca her
	// gateLogEvery'nin katında (count alanıyla) loglanır.
	gateLogFirstN = 10
	gateLogEvery  = 100
)

// GateReporter, kapı ihlallerini loglar ve log kipinde Sentry'ye uyarı olarak
// bildirir.
//
// Sentry'ye kullanıcı/tür/gün başına yalnızca İLK ihlal gider: değiştirilmiş
// bir istemci dakikada yüzlerce istek atabilir ve kotayı tüketirdi. Sentry
// mesajı kullanıcıyı içermez, tür ve nedene göre gruplanır (olay sayısı
// "kaç kullanıcı etkilenirdi" sorusunun cevabıdır); kullanıcı ve istek kimliği
// log satırındadır.
//
// Sayaçlar süreç belleğindedir ve gün dönünce sıfırlanır; birden çok replika
// varsa her biri kendi sınırını uygular.
type GateReporter struct {
	mu     sync.Mutex
	day    string
	counts map[string]int

	logf    func(format string, args ...any)
	capture func(msg string)
}

// NewGateReporter, standart log ve observability.CaptureWarning kullanan bir
// raporlayıcı kurar.
func NewGateReporter() *GateReporter {
	return NewGateReporterWith(log.Printf, observability.CaptureWarning)
}

// NewGateReporterWith, çıktıları verilen fonksiyonlara yönlendirir (testler).
func NewGateReporterWith(logf func(format string, args ...any), capture func(msg string)) *GateReporter {
	return &GateReporter{counts: map[string]int{}, logf: logf, capture: capture}
}

// Report, ihlali kaydeder. now'ın kullanım günü sınırlamanın günüdür.
func (r *GateReporter) Report(v GateViolation, now time.Time) {
	day := UsageDate(now)
	key := v.UserID + "|" + v.Kind

	r.mu.Lock()
	if r.day != day {
		r.day = day
		r.counts = map[string]int{}
	}
	r.counts[key]++
	n := r.counts[key]
	r.mu.Unlock()

	if n <= gateLogFirstN || n%gateLogEvery == 0 {
		r.logf("%s", formatGateViolation(v, n))
	}
	if n == 1 && v.Mode != config.GateEnforce {
		r.capture(fmt.Sprintf("reklam kapısı ihlali (kip=%s): %s/%s", v.Mode, v.Kind, v.Reason))
	}
}

// formatGateViolation, tek satırlık key=value log kaydıdır; grep ve log
// sorgusuyla sayılabilsin diye alan adları sabittir.
func formatGateViolation(v GateViolation, count int) string {
	line := "gate_violation kind=" + v.Kind +
		" reason=" + v.Reason +
		" mode=" + string(v.Mode) +
		" user=" + v.UserID +
		" request_id=" + v.RequestID
	switch v.Kind {
	case GateKindLearn:
		// Grup açılışı ihlalinde (free_group_reused) kelime yoktur.
		if v.WordID != "" {
			line += " word=" + strconv.Quote(v.WordID)
		}
		line += " group=" + v.Level + ":" + strconv.Itoa(v.Group)
	case GateKindReview:
		line += " answers_today=" + strconv.Itoa(v.ReviewAnswers) +
			" passes=" + strconv.Itoa(v.ReviewAdPasses)
	}
	return line + " count=" + strconv.Itoa(count)
}
