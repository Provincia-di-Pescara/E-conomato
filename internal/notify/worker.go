package notify

import (
	"context"
	"time"

	"github.com/Provincia-di-Pescara/e-conomato/internal/config"
	"github.com/Provincia-di-Pescara/e-conomato/internal/database"
	"github.com/Provincia-di-Pescara/e-conomato/internal/email"
	"github.com/Provincia-di-Pescara/e-conomato/internal/logger"
)

// Worker consuma la outbox `email_outbox` con backoff esponenziale.
type Worker struct {
	db       *database.DB
	cfg      *config.Config
	emitter  *Emitter
	Interval time.Duration
	Batch    int
	MaxRetry int
}

// NewWorker costruisce un Worker con default ragionevoli.
func NewWorker(db *database.DB, cfg *config.Config, e *Emitter) *Worker {
	return &Worker{
		db:       db,
		cfg:      cfg,
		emitter:  e,
		Interval: 30 * time.Second,
		Batch:    10,
		MaxRetry: 5,
	}
}

// Run processa la outbox finché ctx non viene cancellato. Il loop si sveglia
// su ticker oppure su segnale dall'Emitter (nuovo job appena accodato).
func (w *Worker) Run(ctx context.Context) {
	if w.cfg.SMTPServer == "" {
		logger.Info("notify worker: SMTP_SERVER non configurato, worker non avviato")
		return
	}
	logger.Info("notify worker: avviato (interval=%s, batch=%d, max_retry=%d)", w.Interval, w.Batch, w.MaxRetry)
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		w.processBatch()
		select {
		case <-ctx.Done():
			logger.Info("notify worker: shutdown")
			return
		case <-ticker.C:
		case <-w.emitter.Wake():
		}
	}
}

func (w *Worker) processBatch() {
	now := time.Now()
	jobs, err := w.db.LeasePendingEmails(now, w.Batch)
	if err != nil {
		logger.Error("notify worker: LeasePendingEmails: %v", err)
		return
	}
	for _, j := range jobs {
		err := email.Send(w.cfg, j.Destinatario, j.Soggetto, j.CorpoHTML)
		if err == nil {
			if mErr := w.db.MarkEmailSent(j.ID, time.Now()); mErr != nil {
				logger.Error("notify worker: MarkEmailSent id=%d: %v", j.ID, mErr)
			}
			logger.Info("notify worker: email inviata id=%d to=%s tipo=%s", j.ID, j.Destinatario, j.Tipo)
			continue
		}
		attempts := j.Tentativi + 1
		abbandona := attempts >= w.MaxRetry
		next := time.Now().Add(backoff(attempts))
		if mErr := w.db.MarkEmailFailed(j.ID, attempts, err.Error(), next, abbandona); mErr != nil {
			logger.Error("notify worker: MarkEmailFailed id=%d: %v", j.ID, mErr)
		}
		if abbandona {
			logger.Warn("notify worker: email abbandonata id=%d (tentativi=%d): %v", j.ID, attempts, err)
		} else {
			logger.Warn("notify worker: email fallita id=%d (tentativo %d/%d), retry tra %s: %v", j.ID, attempts, w.MaxRetry, next.Sub(time.Now()).Round(time.Second), err)
		}
	}
}

// backoff esponenziale con cap a 1h: 60s, 120s, 240s, 480s, 960s, ..., 3600s.
func backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := time.Duration(60) * time.Second
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= time.Hour {
			return time.Hour
		}
	}
	return d
}
