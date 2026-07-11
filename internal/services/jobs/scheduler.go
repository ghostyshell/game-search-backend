// Package jobs runs the periodic ingest/sync jobs (debrid hosts, per-source
// scrapers, metadata enricher) via a shared JobScheduler.
package jobs

import (
	"context"
	"log"
	"sync"
	"time"
)

// Job is a named periodic unit of work.
type Job struct {
	Name         string
	Schedule     Schedule
	Run          func(ctx context.Context) error
}

// Schedule holds interval + initial delay for a periodic job.
type Schedule struct {
	Interval     time.Duration
	InitialDelay time.Duration
}

// Scheduler runs Jobs periodically. Overlapping runs of the same job are skipped
// (TryLock): if the previous tick is still running, the new tick is dropped
// rather than queued, so a slow scrape cannot pile up.
type Scheduler struct {
	mu   sync.Mutex
	jobs []Job
	wg   sync.WaitGroup
}

// NewScheduler builds an empty Scheduler.
func NewScheduler() *Scheduler { return &Scheduler{} }

// Register adds a job.
func (s *Scheduler) Register(j Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, j)
}

// Start launches every registered job in its own goroutine. The initial delay
// runs before the first tick so a cold deploy does not hammer all sources at
// once. Returns a stop func that cancels all jobs and waits for them to exit.
func (s *Scheduler) Start(parent context.Context) func() {
	s.mu.Lock()
	jobs := make([]Job, len(s.jobs))
	copy(jobs, s.jobs)
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(parent)
	for _, j := range jobs {
		s.wg.Add(1)
		go s.runPeriodic(ctx, j)
	}
	return func() {
		cancel()
		s.wg.Wait()
	}
}

// runPeriodic runs one job: wait InitialDelay, then tick on Interval. Skip a tick
// if the previous run is still in flight (TryLock).
func (s *Scheduler) runPeriodic(ctx context.Context, j Job) {
	defer s.wg.Done()

	if j.Schedule.InitialDelay > 0 {
		select {
		case <-time.After(j.Schedule.InitialDelay):
		case <-ctx.Done():
			return
		}
	}
	if j.Schedule.Interval <= 0 {
		j.Schedule.Interval = time.Minute
	}

	var runMu sync.Mutex
	ticker := time.NewTicker(j.Schedule.Interval)
	defer ticker.Stop()

	// First run immediately after the initial delay.
	if !runMu.TryLock() {
		return
	}
	go func() {
		defer runMu.Unlock()
		if err := j.Run(ctx); err != nil {
			log.Printf("job %s: %v", j.Name, err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !runMu.TryLock() {
				// ponytail: previous run still in flight; skip this tick rather
				// than queue (prevents piling up slow scrapes).
				continue
			}
			go func() {
				defer runMu.Unlock()
				if err := j.Run(ctx); err != nil {
					log.Printf("job %s: %v", j.Name, err)
				}
			}()
		}
	}
}