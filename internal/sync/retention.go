package sync

import (
	"context"
	"math"
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
	}

	// Compute AoN values using the most recent N solve times.
	for _, n := range []int{5, 12, 50, 100} {
		if row.CountedCount+row.DnfCount < int64(n) {
			break
		}
		ao, err := s.computeAoN(ctx, q, userID, req.Event, n)
		if err != nil {
			return StatsResponse{}, err
		}
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

	return resp, nil
}

// computeAoN computes the Average of N (AoN) for the most recent N solves.
// Returns nil if there are fewer than n solves, or if more than 1/5 of solves are DNF.
func (s *StatsService) computeAoN(
	ctx context.Context, q *storedb.Queries,
	userID uuid.UUID, event string, n int,
) (*int64, error) {
	rows, err := q.UserSolveAoN(ctx, storedb.UserSolveAoNParams{
		UserID:   userID,
		Event:    event,
		LimitVal: int32(n),
	})
	if err != nil {
		return nil, err
	}
	if len(rows) < n {
		return nil, nil
	}

	// Standard AoN: drop best and worst (for n>=5), discard if >1/5 are DNF.
	dnfCount := 0
	times := make([]int64, 0, n)
	for _, row := range rows {
		if row.Penalty == "dnf" {
			dnfCount++
			times = append(times, math.MaxInt64) // DNF sorts to worst
		} else {
			// EffectiveMs is interface{} from sqlc CASE expression — convert via pgx numeric types.
			ms, ok := toInt64(row.EffectiveMs)
			if !ok {
				return nil, nil
			}
			times = append(times, ms)
		}
	}

	// More than 1/5 DNF → AoN is DNF.
	maxDNF := n / 5
	if n == 5 {
		maxDNF = 1
	}
	if dnfCount > maxDNF {
		return nil, nil
	}

	// Sort ascending (insertion sort — n is small).
	sortedTimes := make([]int64, n)
	copy(sortedTimes, times)
	for i := 1; i < n; i++ {
		key := sortedTimes[i]
		j := i - 1
		for j >= 0 && sortedTimes[j] > key {
			sortedTimes[j+1] = sortedTimes[j]
			j--
		}
		sortedTimes[j+1] = key
	}

	// Drop best and worst for n >= 5.
	trimFrom, trimTo := 0, n
	if n >= 5 {
		trimFrom = 1
		trimTo = n - 1
	}
	trimmed := sortedTimes[trimFrom:trimTo]

	var sum int64
	for _, t := range trimmed {
		if t == math.MaxInt64 {
			return nil, nil
		}
		sum += t
	}

	avg := sum / int64(len(trimmed))
	return &avg, nil
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
	done                 chan struct{}
}

// NewRetentionService creates and starts the background retention job.
func NewRetentionService(pool *pgxpool.Pool, inactiveDeviceWindow, runInterval time.Duration) *RetentionService {
	s := &RetentionService{
		pool:                 pool,
		inactiveDeviceWindow: inactiveDeviceWindow,
		runInterval:          runInterval,
		done:                 make(chan struct{}),
	}
	go s.loop()
	return s
}

// Shutdown signals the retention loop to stop.
func (s *RetentionService) Shutdown() {
	close(s.done)
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
		return
	}

	cutoff := time.Now().UTC().Add(-s.inactiveDeviceWindow)
	for _, userID := range userIDs {
		minCursor, err := q.MinValidCursorForUser(ctx, storedb.MinValidCursorForUserParams{
			UserID:     userID,
			LastSeenAt: cutoff,
		})
		if err != nil || minCursor <= 0 {
			continue
		}
		// Prune change_log rows that all active devices have passed.
		_, _ = q.PruneChangeLog(ctx, storedb.PruneChangeLogParams{
			UserID:   userID,
			ChangeID: minCursor,
		})
		// Prune processed_mutations older than the inactive device window.
		_, _ = q.PruneProcessedMutations(ctx, storedb.PruneProcessedMutationsParams{
			UserID:    userID,
			CreatedAt: cutoff,
		})
	}
}
