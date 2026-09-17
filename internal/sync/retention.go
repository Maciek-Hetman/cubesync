package sync

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sync"
	"time"

	storedb "github.com/Maciek-Hetman/cubing-sync-backend/internal/store/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StatsService computes server-side solve statistics.
type StatsService struct {
	pool *pgxpool.Pool
}

// NewStatsService creates a StatsService.
func NewStatsService(pool *pgxpool.Pool) *StatsService {
	return &StatsService{pool: pool}
}

// ComputeStats returns aggregated solve statistics for the authenticated user.
// DNF solves are excluded from time-based stats; +2 solves add 2000ms to their effective time.
func (s *StatsService) ComputeStats(ctx context.Context, userID uuid.UUID, req StatsRequest) (StatsResponse, error) {
	q := storedb.New(s.pool)

	row, err := q.UserSolveStats(ctx, storedb.UserSolveStatsParams{
		UserID: userID,
		Event:  req.Event,
	})
	if err != nil {
		return StatsResponse{}, err
	}

	resp := StatsResponse{
		TotalCount:   row.TotalCount,
		CountedCount: row.CountedCount,
		DNFCount:     row.DnfCount,
		MinMS:        row.MinMs,
		MaxMS:        row.MaxMs,
		MeanMS:       row.MeanMs,
		StddevMS:     row.StddevMs,
		TotalMS:      row.TotalMs,
		DNFAverages:  []string{},
	}

	// Compute AoN values using the most recent N solve times.
	for _, n := range []int{5, 12, 50, 100} {
		if row.CountedCount+row.DnfCount < int64(n) {
			break
		}
		ao, isDNF, err := s.computeAoN(ctx, q, userID, req.Event, n)
		if err != nil {
			return StatsResponse{}, err
		}
		nStr := fmt.Sprintf("ao%d", n)
		if isDNF {
			resp.DNFAverages = append(resp.DNFAverages, nStr)
		} else {
			switch n {
			case 5:
				resp.Ao5 = ao
			case 12:
				resp.Ao12 = ao
			case 50:
				resp.Ao50 = ao
			case 100:
				resp.Ao100 = ao
			}
		}
	}

	return resp, nil
}

// computeAoN computes the average of the most recent n solves. isDNF reports a
// DNF average; a nil value without isDNF means there are not enough solves.
func (s *StatsService) computeAoN(
	ctx context.Context, q *storedb.Queries,
	userID uuid.UUID, event string, n int,
) (*int64, bool, error) {
	rows, err := q.UserSolveAoN(ctx, storedb.UserSolveAoNParams{
		UserID:   userID,
		Event:    event,
		LimitVal: int32(n),
	})
	if err != nil {
		return nil, false, err
	}
	times := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.Penalty == "dnf" {
			times = append(times, math.MaxInt64)
			continue
		}
		ms, ok := toInt64(row.EffectiveMs)
		if !ok {
			return nil, false, fmt.Errorf("unexpected effective_ms type %T", row.EffectiveMs)
		}
		times = append(times, ms)
	}
	avg, isDNF := computeAoNFromTimes(times, n)
	return avg, isDNF, nil
}

// computeAoNFromTimes applies the WCA/csTimer rule: drop ceil(5%) of the n
// times from each end; any DNF (math.MaxInt64) left after trimming makes the
// average DNF. The mean is rounded to the nearest millisecond.
func computeAoNFromTimes(times []int64, n int) (*int64, bool) {
	if n <= 0 || len(times) < n {
		return nil, false
	}
	sorted := slices.Clone(times[:n])
	slices.Sort(sorted)
	k := (n + 19) / 20
	trimmed := sorted[k : n-k]
	if trimmed[len(trimmed)-1] == math.MaxInt64 {
		return nil, true
	}
	var sum int64
	for _, t := range trimmed {
		sum += t
	}
	count := int64(len(trimmed))
	mean := (sum + count/2) / count
	return &mean, false
}

// toInt64 converts an interface{} value from a sqlc CASE expression to int64.
// pgx may return int16, int32, int64, or float64 depending on the expression type.
func toInt64(v interface{}) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int32:
		return int64(x), true
	case int16:
		return int64(x), true
	case float64:
		return int64(x), true
	case int:
		return int64(x), true
	default:
		return 0, false
	}
}

// RetentionService prunes old change_log rows once all eligible devices have
// advanced past them.
type RetentionService struct {
	pool                 *pgxpool.Pool
	inactiveDeviceWindow time.Duration
	runInterval          time.Duration
	logger               *slog.Logger
	wg                   sync.WaitGroup
	done                 chan struct{}
}

// NewRetentionService creates and starts the background retention job.
func NewRetentionService(pool *pgxpool.Pool, inactiveDeviceWindow, runInterval time.Duration) *RetentionService {
	s := &RetentionService{
		pool:                 pool,
		inactiveDeviceWindow: inactiveDeviceWindow,
		runInterval:          runInterval,
		logger:               slog.Default(),
		done:                 make(chan struct{}),
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.loop()
	}()
	return s
}

// Shutdown signals the retention loop to stop and waits for in-flight tasks.
func (s *RetentionService) Shutdown() {
	close(s.done)
	s.wg.Wait()
}

func (s *RetentionService) loop() {
	ticker := time.NewTicker(s.runInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.runOnce()
		}
	}
}

func (s *RetentionService) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	q := storedb.New(s.pool)

	userIDs, err := q.ListUsersWithChanges(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("retention_list_users_failed", "error", err)
		}
		return
	}

	cutoff := time.Now().UTC().Add(-effectiveInactiveWindow(s.inactiveDeviceWindow))
	for _, userID := range userIDs {
		minCursor, err := q.MinValidCursorForUser(ctx, storedb.MinValidCursorForUserParams{
			UserID:     userID,
			LastSeenAt: cutoff,
		})
		if err != nil {
			if s.logger != nil {
				s.logger.Error("retention_min_cursor_failed", "user_id", userID, "error", err)
			}
			continue
		}
		if minCursor <= 0 {
			continue
		}
		if _, err := q.PruneChangeLog(ctx, storedb.PruneChangeLogParams{
			UserID:   userID,
			ChangeID: minCursor,
		}); err != nil {
			if s.logger != nil {
				s.logger.Error("retention_prune_change_log_failed", "user_id", userID, "error", err)
			}
		}
		if _, err := q.PruneProcessedMutations(ctx, storedb.PruneProcessedMutationsParams{
			UserID:    userID,
			CreatedAt: cutoff,
		}); err != nil {
			if s.logger != nil {
				s.logger.Error("retention_prune_mutations_failed", "user_id", userID, "error", err)
			}
		}
	}
}
