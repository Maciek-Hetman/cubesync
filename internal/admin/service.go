package admin

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	storedb "github.com/Maciek-Hetman/cubing-sync-backend/internal/store/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	unmatchedRoute  = "unmatched"
	defaultLookback = 24 * time.Hour
	maxRange        = 90 * 24 * time.Hour
	IntervalHour    = "hour"
	IntervalDay     = "day"
)

const (
	RequestTypeAuth     = "auth"
	RequestTypeAccount  = "account"
	RequestTypeSync     = "sync"
	RequestTypeSnapshot = "snapshot"
	RequestTypeSessions = "sessions"
	RequestTypeStats    = "stats"
	RequestTypeOther    = "other"
)

type metric struct {
	method   string
	route    string
	status   int
	duration time.Duration
	at       time.Time
}

type errorEvent struct {
	userID     uuid.NullUUID
	method     string
	route      string
	statusCode int32
	code       string
	message    string
}

type Service struct {
	pool      *pgxpool.Pool
	now       func() time.Time
	logger    *slog.Logger
	metrics   chan metric
	errorLogs chan errorEvent
	done      chan struct{}
	stopOnce  sync.Once
	stopChan  chan struct{}
	shutdown  atomic.Bool
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

type ErrorLog struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UserID    *string   `json:"user_id,omitempty"`
	Method    string    `json:"method"`
	Route     string    `json:"route"`
	Status    int       `json:"status"`
	Code      string    `json:"code"`
	Message   string    `json:"message"`
}

type ErrorLogResponse struct {
	Errors       []ErrorLog `json:"errors"`
	NextCursor   *time.Time `json:"next_cursor"`
	NextCursorID *int64     `json:"next_cursor_id,omitempty"`
}

type QueryRange struct {
	From     time.Time
	To       time.Time
	Interval string
}

type RequestTypeCount struct {
	Type         string `json:"type"`
	RequestCount int64  `json:"request_count"`
}

type RequestTypeSeries struct {
	From     time.Time          `json:"from"`
	To       time.Time          `json:"to"`
	Interval string             `json:"interval"`
	Types    []RequestTypeCount `json:"types"`
}

type Error struct {
	Code    string
	Message string
}

func (e Error) Error() string { return e.Message }

func NewService(pool *pgxpool.Pool) *Service {
	s := &Service{
		pool:      pool,
		now:       time.Now,
		logger:    slog.Default(),
		metrics:   make(chan metric, 4096),
		errorLogs: make(chan errorEvent, 4096),
		done:      make(chan struct{}),
		stopChan:  make(chan struct{}),
	}
	go s.flushLoop()
	return s
}

func (s *Service) flushLoop() {
	defer close(s.done)

	buffer := make([]metric, 0, 200)
	errBuffer := make([]errorEvent, 0, 100)
	flushAll := func() {
		s.flush(buffer)
		buffer = buffer[:0]
		s.flushErrors(errBuffer)
		errBuffer = errBuffer[:0]
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	s.runCleanup()
	cleanupTicker := time.NewTicker(24 * time.Hour)
	defer cleanupTicker.Stop()

	for {
		select {
		case m := <-s.metrics:
			buffer = append(buffer, m)
			if len(buffer) >= 200 {
				s.flush(buffer)
				buffer = buffer[:0]
			}
		case e := <-s.errorLogs:
			errBuffer = append(errBuffer, e)
			if len(errBuffer) >= 100 {
				s.flushErrors(errBuffer)
				errBuffer = errBuffer[:0]
			}
		case <-ticker.C:
			flushAll()
		case <-cleanupTicker.C:
			s.runCleanup()
		case <-s.stopChan:
			for {
				select {
				case m := <-s.metrics:
					buffer = append(buffer, m)
				case e := <-s.errorLogs:
					errBuffer = append(errBuffer, e)
				default:
					flushAll()
					return
				}
			}
		}
	}
}

func (s *Service) runCleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	q := storedb.New(s.pool)
	if err := q.DeleteOldErrors(ctx); err != nil {
		s.logger.Error("delete_old_errors_failed", "error", err)
	}
	if err := q.DeleteOldRequestStats(ctx); err != nil {
		s.logger.Error("delete_old_request_stats_failed", "error", err)
	}
}

type metricKey struct {
	bucketHour time.Time
	method     string
	route      string
	statusCode int32
}

func (s *Service) flush(buffer []metric) {
	if len(buffer) == 0 {
		return
	}
	aggregated := make(map[metricKey]*storedb.RecordRequestStatParams)
	for _, m := range buffer {
		status := int32(m.status)
		if status <= 0 {
			status = 200
		}
		durationMS := max(m.duration.Milliseconds(), 0)
		key := metricKey{
			bucketHour: m.at.UTC().Truncate(time.Hour),
			method:     m.method,
			route:      normalizeRoute(m.route),
			statusCode: status,
		}
		agg, ok := aggregated[key]
		if !ok {
			agg = &storedb.RecordRequestStatParams{
				BucketHour: key.bucketHour, Method: key.method, Route: key.route, StatusCode: key.statusCode,
			}
			aggregated[key] = agg
		}
		agg.RequestCount++
		agg.TotalDurationMs += durationMS
		agg.MaxDurationMs = max(agg.MaxDurationMs, durationMS)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := storedb.New(s.pool)
	for _, agg := range aggregated {
		if err := q.RecordRequestStat(ctx, *agg); err != nil {
			s.logger.Error("record_request_stat_failed", "error", err)
		}
	}
}

func (s *Service) flushErrors(buffer []errorEvent) {
	if len(buffer) == 0 {
		return
	}
	rows := make([]storedb.CopyRequestErrorsParams, 0, len(buffer))
	for _, e := range buffer {
		rows = append(rows, storedb.CopyRequestErrorsParams{
			UserID:     e.userID,
			Method:     e.method,
			Route:      normalizeRoute(e.route),
			StatusCode: e.statusCode,
			Code:       e.code,
			Message:    e.message,
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := storedb.New(s.pool).CopyRequestErrors(ctx, rows); err != nil {
		s.logger.Error("record_request_errors_failed", "error", err, "count", len(rows))
	}
}

func (s *Service) RecordRequestAsync(method, route string, status int, duration time.Duration) {
	if shouldSkipRoute(route) || s.shutdown.Load() {
		return
	}
	m := metric{
		method:   method,
		route:    route,
		status:   status,
		duration: duration,
		at:       s.now(),
	}
	select {
	case s.metrics <- m:
	default:
	}
}

func (s *Service) Shutdown() {
	s.shutdown.Store(true)
	s.stopOnce.Do(func() {
		close(s.stopChan)
	})
	<-s.done
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

func (s *Service) RequestTypeStats(ctx context.Context, query QueryRange) (RequestTypeSeries, error) {
	resolved, err := resolveRange(s.now().UTC(), query)
	if err != nil {
		return RequestTypeSeries{}, err
	}
	rows, err := storedb.New(s.pool).ListRequestStatsByType(ctx, storedb.ListRequestStatsByTypeParams{
		FromTime: resolved.From,
		ToTime:   resolved.To,
	})
	if err != nil {
		return RequestTypeSeries{}, err
	}
	counts := make(map[string]int64)
	for _, row := range rows {
		counts[requestTypeForRoute(row.Route)] += row.RequestCount
	}
	types := make([]RequestTypeCount, 0, len(counts))
	for requestType, count := range counts {
		types = append(types, RequestTypeCount{Type: requestType, RequestCount: count})
	}
	sort.Slice(types, func(i, j int) bool {
		if types[i].RequestCount == types[j].RequestCount {
			return types[i].Type < types[j].Type
		}
		return types[i].RequestCount > types[j].RequestCount
	})
	return RequestTypeSeries{
		From:     resolved.From,
		To:       resolved.To,
		Interval: resolved.Interval,
		Types:    types,
	}, nil
}

func (s *Service) ListErrors(ctx context.Context, before time.Time, beforeID int64, limit int) (ErrorLogResponse, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if before.IsZero() {
		before = s.now().UTC()
	}
	if beforeID == 0 {
		beforeID = math.MaxInt64
	}

	rows, err := storedb.New(s.pool).ListIndividualErrors(ctx, storedb.ListIndividualErrorsParams{
		Before:   before,
		BeforeID: beforeID,
		LimitVal: int32(limit),
	})
	if err != nil {
		return ErrorLogResponse{}, err
	}

	errors := make([]ErrorLog, 0, len(rows))
	for _, row := range rows {
		var userID *string
		if row.UserID.Valid {
			idStr := row.UserID.UUID.String()
			userID = &idStr
		}
		errors = append(errors, ErrorLog{
			ID:        row.ID,
			CreatedAt: row.CreatedAt,
			UserID:    userID,
			Method:    row.Method,
			Route:     row.Route,
			Status:    int(row.StatusCode),
			Code:      row.Code,
			Message:   row.Message,
		})
	}

	var nextCursor *time.Time
	var nextCursorID *int64
	if len(errors) == limit {
		lastTime := errors[len(errors)-1].CreatedAt
		lastID := errors[len(errors)-1].ID
		nextCursor = &lastTime
		nextCursorID = &lastID
	}

	return ErrorLogResponse{
		Errors:       errors,
		NextCursor:   nextCursor,
		NextCursorID: nextCursorID,
	}, nil
}

func (s *Service) RecordErrorAsync(userID uuid.UUID, method, route string, status int, code, message string) {
	if shouldSkipRoute(route) || s.shutdown.Load() || status == 401 {
		return
	}

	var pgUserID uuid.NullUUID
	if userID != uuid.Nil {
		pgUserID = uuid.NullUUID{UUID: userID, Valid: true}
	}

	e := errorEvent{
		userID:     pgUserID,
		method:     method,
		route:      route,
		statusCode: int32(status),
		code:       code,
		message:    message,
	}
	select {
	case s.errorLogs <- e:
	default:
	}
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

func requestTypeForRoute(route string) string {
	switch {
	case strings.HasPrefix(route, "/v1/auth/"):
		return RequestTypeAuth
	case strings.HasPrefix(route, "/v1/me"):
		return RequestTypeAccount
	case route == "/v1/sync":
		return RequestTypeSync
	case route == "/v1/snapshot":
		return RequestTypeSnapshot
	case strings.HasPrefix(route, "/v1/sessions"):
		return RequestTypeSessions
	case strings.HasPrefix(route, "/v1/stats"):
		return RequestTypeStats
	default:
		return RequestTypeOther
	}
}
