package incident

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type Outcome struct{ Status, PR, Report string }
type Jobs interface {
	Execute(context.Context, Incident, Resource, func() error) (Outcome, error)
}
type Worker struct {
	config    Config
	store     *Store
	jobs      Jobs
	wg        sync.WaitGroup
	mu        sync.Mutex
	executing map[string]string
	fatal     chan error
}

func NewWorker(c Config, s *Store, j Jobs) *Worker {
	return &Worker{config: c, store: s, jobs: j, fatal: make(chan error, 1), executing: map[string]string{}}
}
func (w *Worker) Wait() { w.wg.Wait() }

func (w *Worker) Tick(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	rows, err := w.store.claim(w.config, time.Now(), w.executing)
	if err != nil {
		return err
	}
	for _, r := range rows {
		jobCtx, cancel := context.WithTimeout(ctx, time.Duration(w.config.TimeoutSeconds)*time.Second)
		w.executing[r.Key] = r.Resource.Repo
		w.wg.Add(1)
		go w.execute(jobCtx, cancel, r)
	}
	return nil
}

func (w *Worker) execute(ctx context.Context, cancel context.CancelFunc, r Incident) {
	defer w.wg.Done()
	defer cancel()
	defer func() { w.mu.Lock(); delete(w.executing, r.Key); w.mu.Unlock() }()
	monitorDone := make(chan struct{})
	defer close(monitorDone)
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-monitorDone:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				rows, err := w.store.List()
				if err != nil {
					cancel()
					return
				}
				for _, s := range rows {
					if s.Key == r.Key && (s.Status == "cancelled" || s.Status == "resolved") {
						cancel()
						return
					}
				}
			}
		}
	}()
	result, jobErr := w.jobs.Execute(ctx, r, r.Resource, func() error { return w.store.MarkPublishing(r.Key) })
	var cleanupErr *CleanupFailure
	if errors.As(jobErr, &cleanupErr) {
		select {
		case w.fatal <- jobErr:
		default:
		}
	}
	err := w.store.Update(func(rows map[string]*Incident) error {
		row := rows[r.Key]
		if row == nil {
			return fmt.Errorf("incident disappeared")
		}
		if row.Status == "cancelled" || row.Status == "resolved" {
			if result.PR != "" {
				row.PR = result.PR
				row.Report = result.Report
			}
			return nil
		}
		row.UpdatedAt = time.Now()
		if jobErr != nil {
			row.Report = fmt.Sprintf("Investigation interrupted or failed: %v\n%s", jobErr, result.Report)
			if row.Publication {
				row.Status = "publishing"
			} else if row.Attempts >= w.config.MaxAttempts {
				row.Status = "review"
			} else {
				row.Status = "queued"
			}
			row.NextAt = time.Now().Add(30 * time.Second)
			return nil
		}
		if result.Status != "draft" && result.Status != "review" {
			return fmt.Errorf("invalid job outcome %q", result.Status)
		}
		row.Status = result.Status
		row.PR = result.PR
		row.Report = result.Report
		return nil
	})
	if err != nil {
		select {
		case w.fatal <- err:
		default:
		}
	}
}

// Run drains the local durable backlog regardless of broker connectivity.
func (w *Worker) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	defer w.Wait()
	defer cancel()
	for {
		if err := w.Tick(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-w.fatal:
			return err
		case <-t.C:
		}
	}
}

func logError(message string, err error) { slog.Error(message, "error", err) }
