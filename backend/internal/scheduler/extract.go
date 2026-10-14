package scheduler

import (
	"ai-library/backend/internal/ports"
	"ai-library/backend/internal/repository"
	"context"
	"errors"
	"time"
	_ "time/tzdata"
)

type Runner struct {
	Store     *repository.Entries
	Extractor ports.Extractor
	Cache     repository.ResultCache
}

func DueDay(now time.Time) (string, bool) {
	zone, _ := time.LoadLocation("Asia/Shanghai")
	local := now.In(zone)
	return local.Format("2006-01-02"), local.Hour() > 4 || (local.Hour() == 4 && local.Minute() >= 30)
}
func (r *Runner) RunDue(ctx context.Context, now time.Time) error {
	day, due := DueDay(now)
	if !due {
		return nil
	}
	if r.Extractor == nil {
		return errors.New("extraction adapter is required")
	}
	now = now.UTC().Truncate(time.Millisecond)
	scopes, err := r.Store.ExtractionScopes(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, scope := range scopes {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lease, claimed, err := r.Store.ClaimExtraction(ctx, scope, now, day)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !claimed {
			continue
		}
		if err = r.runKB(ctx, scope, lease, now, day); err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = r.Store.FailExtraction(cleanup, scope, lease.Token, now)
			cancel()
			failures = append(failures, errors.New("scheduled extraction failed; durable checkpoint retained"))
		}
	}
	return errors.Join(failures...)
}
func (r *Runner) runKB(ctx context.Context, scope ports.Scope, lease repository.ExtractionLease, now time.Time, day string) error {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	// Keep the watermark behind PostgreSQL's current rounded millisecond.
	until := now.Add(-time.Millisecond)
	sessions, err := r.Store.ExtractionHistory(ctx, scope, "", &lease.Since, &until)
	if err != nil {
		return err
	}
	var all []ports.KnowledgeSection
	for _, session := range sessions {
		users := 0
		for _, m := range session.Messages {
			if m.Role == "user" {
				users++
			}
		}
		if users < 2 {
			continue
		}
		var request string
		if err = r.Store.Pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&request); err != nil {
			return err
		}
		items, err := r.Extractor.Extract(ctx, ports.ExtractRequest{RequestID: request, History: session.Messages, Mode: "scheduled"})
		if err != nil {
			return err
		}
		all = append(all, items...)
	}
	entries, err := r.Store.SaveExtracted(ctx, scope, all, &repository.ExtractionCheckpoint{Until: until, Day: day, Token: lease.Token})
	if err != nil {
		return err
	}
	if len(entries) > 0 && r.Cache != nil {
		for _, prefix := range []string{"go:entry:" + scope.KnowledgeBaseID + ":", "entry:" + scope.KnowledgeBaseID + ":"} {
			if err = r.Cache.DeletePrefix(ctx, prefix); err != nil {
				return err
			}
		}
		return r.Cache.Delete(ctx, "go:analytics:overview:"+scope.UserID, "analytics:overview:"+scope.UserID, "go:analytics:kb:"+scope.KnowledgeBaseID, "analytics:kb:"+scope.KnowledgeBaseID)
	}
	return nil
}
