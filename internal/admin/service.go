package admin

import (
	"context"
	"fmt"
	"strings"
	"time"

	storedb "github.com/Maciek-Hetman/cubing-sync-backend/internal/store/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	unmatchedRoute  = "unmatched"
	defaultLookback = 24 * time.Hour
	maxRange        = 90 * 24 * time.Hour
	IntervalHour    = "hour"
	IntervalDay     = "day"
)

type Service struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

type Overview struct {
	TotalUsers     int64 `json:"total_users"`
	VerifiedUsers  int64 `json:"verified_users"`
	NewUsers24h    int64 `json:"new_users_24h"`
	NewUsers7d     int64 `json:"new_users_7d"`
	NewUsers30d    int64 `json:"new_users_30d"`
	ActiveUsers24h int64 `json:"active_users_24h"`
	ActiveUsers7d  int64 `json:"active_users_7d"`
	ActiveUsers30d int64 `json:"active_users_30d"`
	TotalDevices   int64 `json:"total_devices"`
	TotalSessions  int64 `json:"total_sessions"`
	TotalSolves    int64 `json:"total_solves"`
}

type RequestSeriesPoint struct {
	Bucket            time.Time `json:"bucket"`
	RequestCount      int64     `json:"request_count"`
	Status2xx         int64     `json:"status_2xx"`
	Status3xx         int64     `json:"status_3xx"`
	Status4xx         int64     `json:"status_4xx"`
	Status5xx         int64     `json:"status_5xx"`
	AverageDurationMS float64   `json:"average_duration_ms"`
	MaxDurationMS     int64     `json:"max_duration_ms"`
}

type RequestSeries struct {
	From     time.Time            `json:"from"`
	To       time.Time            `json:"to"`
	Interval string               `json:"interval"`
	Points   []RequestSeriesPoint `json:"points"`
}

type ErrorSeriesPoint struct {
	Bucket       time.Time `json:"bucket"`
	Method       string    `json:"method"`
	Route        string    `json:"route"`
	StatusCode   int       `json:"status_code"`
	RequestCount int64     `json:"request_count"`
}

type ErrorSeries struct {
	From     time.Time          `json:"from"`
	To       time.Time          `json:"to"`
	Interval string             `json:"interval"`
	Points   []ErrorSeriesPoint `json:"points"`
}

type QueryRange struct {
	From     time.Time
	To       time.Time
	Interval string
}

type Error struct {
	Code    string
	Message string
}

func (e Error) Error() string { return e.Message }

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, now: time.Now}
}

func (s *Service) RecordRequest(ctx context.Context, method, route string, status int, duration time.Duration) error {
	if shouldSkipRoute(route) {
		return nil
	}
	if status <= 0 {
		status = 200
	}
	durationMS := duration.Milliseconds()
	if durationMS < 0 {
		durationMS = 0
	}
	return storedb.New(s.pool).RecordRequestStat(ctx, storedb.RecordRequestStatParams{
		BucketHour:      s.now().UTC().Truncate(time.Hour),
		Method:          method,
		Route:           normalizeRoute(route),
		StatusCode:      int32(status),
		TotalDurationMs: durationMS,
	})
}

func (s *Service) Overview(ctx context.Context) (Overview, error) {
	row, err := storedb.New(s.pool).GetOverviewStats(ctx)
	if err != nil {
		return Overview{}, err
	}
	return Overview{
		TotalUsers:     row.TotalUsers,
		VerifiedUsers:  row.VerifiedUsers,
		NewUsers24h:    row.NewUsers24h,
		NewUsers7d:     row.NewUsers7d,
		NewUsers30d:    row.NewUsers30d,
		ActiveUsers24h: row.ActiveUsers24h,
		ActiveUsers7d:  row.ActiveUsers7d,
		ActiveUsers30d: row.ActiveUsers30d,
		TotalDevices:   row.TotalDevices,
		TotalSessions:  row.TotalSessions,
		TotalSolves:    row.TotalSolves,
	}, nil
}

func (s *Service) RequestStats(ctx context.Context, query QueryRange) (RequestSeries, error) {
	resolved, err := resolveRange(s.now().UTC(), query)
	if err != nil {
		return RequestSeries{}, err
	}
	rows, err := storedb.New(s.pool).ListRequestStats(ctx, storedb.ListRequestStatsParams{
		Interval: resolved.Interval,
		FromTime: resolved.From,
		ToTime:   resolved.To,
	})
	if err != nil {
		return RequestSeries{}, err
	}
	points := make([]RequestSeriesPoint, 0, len(rows))
	for _, row := range rows {
		average := 0.0
		if row.RequestCount > 0 {
			average = float64(row.TotalDurationMs) / float64(row.RequestCount)
		}
		points = append(points, RequestSeriesPoint{
			Bucket:            row.Bucket,
			RequestCount:      row.RequestCount,
			Status2xx:         row.Status2xx,
			Status3xx:         row.Status3xx,
			Status4xx:         row.Status4xx,
			Status5xx:         row.Status5xx,
			AverageDurationMS: average,
			MaxDurationMS:     row.MaxDurationMs,
		})
	}
	return RequestSeries{
		From:     resolved.From,
		To:       resolved.To,
		Interval: resolved.Interval,
		Points:   points,
	}, nil
}

func (s *Service) ErrorStats(ctx context.Context, query QueryRange) (ErrorSeries, error) {
	resolved, err := resolveRange(s.now().UTC(), query)
	if err != nil {
		return ErrorSeries{}, err
	}
	rows, err := storedb.New(s.pool).ListErrorStats(ctx, storedb.ListErrorStatsParams{
		Interval: resolved.Interval,
		FromTime: resolved.From,
		ToTime:   resolved.To,
	})
	if err != nil {
		return ErrorSeries{}, err
	}
	points := make([]ErrorSeriesPoint, 0, len(rows))
	for _, row := range rows {
		points = append(points, ErrorSeriesPoint{
			Bucket:       row.Bucket,
			Method:       row.Method,
			Route:        row.Route,
			StatusCode:   int(row.StatusCode),
			RequestCount: row.RequestCount,
		})
	}
	return ErrorSeries{
		From:     resolved.From,
		To:       resolved.To,
		Interval: resolved.Interval,
		Points:   points,
	}, nil
}

func resolveRange(now time.Time, query QueryRange) (QueryRange, error) {
	interval := strings.ToLower(strings.TrimSpace(query.Interval))
	if interval == "" {
		interval = IntervalHour
	}
	if interval != IntervalHour && interval != IntervalDay {
		return QueryRange{}, Error{Code: "invalid_interval", Message: "interval must be hour or day"}
	}
	from := query.From
	to := query.To
	if from.IsZero() && to.IsZero() {
		to = now
		from = now.Add(-defaultLookback)
	}
	if from.IsZero() || to.IsZero() {
		return QueryRange{}, Error{Code: "invalid_range", Message: "from and to must both be provided"}
	}
	from = from.UTC()
	to = to.UTC()
	if !from.Before(to) {
		return QueryRange{}, Error{Code: "invalid_range", Message: "from must be earlier than to"}
	}
	if to.Sub(from) > maxRange {
		return QueryRange{}, Error{Code: "invalid_range", Message: fmt.Sprintf("range cannot exceed %d days", int(maxRange.Hours()/24))}
	}
	return QueryRange{From: from, To: to, Interval: interval}, nil
}

func shouldSkipRoute(route string) bool {
	return strings.HasPrefix(route, "/health/") || strings.HasPrefix(route, "/v1/admin/stats")
}

func normalizeRoute(route string) string {
	route = strings.TrimSpace(route)
	if route == "" {
		return unmatchedRoute
	}
	return route
}
