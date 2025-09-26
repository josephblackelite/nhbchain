package recon

import (
	"context"
	"log"
	"time"
)

// SchedulerConfig configures the nightly reconciliation scheduler.
type SchedulerConfig struct {
	Reconciler *Reconciler
	Window     time.Duration
	RunHour    int
	RunMinute  int
	Location   *time.Location
	Logger     *log.Logger
}

// Scheduler executes reconciliation on a fixed cadence.
type Scheduler struct {
	reconciler *Reconciler
	window     time.Duration
	runHour    int
	runMinute  int
	location   *time.Location
	logger     *log.Logger
}

// NewScheduler constructs a scheduler with sane defaults.
func NewScheduler(cfg SchedulerConfig) *Scheduler {
	window := cfg.Window
	if window <= 0 {
		window = 24 * time.Hour
	}
	loc := cfg.Location
	if loc == nil {
		loc = time.UTC
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &Scheduler{
		reconciler: cfg.Reconciler,
		window:     window,
		runHour:    clampHour(cfg.RunHour),
		runMinute:  clampMinute(cfg.RunMinute),
		location:   loc,
		logger:     logger,
	}
}

// Start begins the scheduling loop until the context is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	if s == nil || s.reconciler == nil {
		return
	}
	for {
		now := time.Now().In(s.location)
		next := s.nextRun(now)
		delay := next.Sub(now)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			start := next.Add(-s.window)
			opts := RunOptions{Start: start, End: next}
			if _, err := s.reconciler.Run(ctx, opts); err != nil {
				s.logger.Printf("recon scheduler run failed: %v", err)
			}
		}
	}
}

func (s *Scheduler) nextRun(after time.Time) time.Time {
	target := time.Date(after.Year(), after.Month(), after.Day(), s.runHour, s.runMinute, 0, 0, s.location)
	if !target.After(after) {
		target = target.Add(24 * time.Hour)
	}
	return target
}

func clampHour(hour int) int {
	if hour < 0 {
		return 0
	}
	if hour > 23 {
		return 23
	}
	return hour
}

func clampMinute(minute int) int {
	if minute < 0 {
		return 0
	}
	if minute > 59 {
		return 59
	}
	return minute
}
