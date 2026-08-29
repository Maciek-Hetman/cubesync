package admin

import (
	"testing"
	"time"
)

func TestResolveRangeDefaultsToLast24Hours(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 10, 15, 0, 0, time.UTC)
	resolved, err := resolveRange(now, QueryRange{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Interval != IntervalHour {
		t.Fatalf("interval=%s", resolved.Interval)
	}
	if !resolved.To.Equal(now) || !resolved.From.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("unexpected range %s to %s", resolved.From, resolved.To)
	}
}

func TestResolveRangeRejectsInvalidValues(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	cases := []QueryRange{
		{From: now, To: now, Interval: IntervalHour},
		{From: now.Add(time.Hour), To: now, Interval: IntervalHour},
		{From: now.Add(-100 * 24 * time.Hour), To: now, Interval: IntervalHour},
		{Interval: "week"},
		{From: now.Add(-time.Hour)},
	}
	for _, query := range cases {
		if _, err := resolveRange(now, query); err == nil {
			t.Fatalf("expected error for %+v", query)
		}
	}
}

func TestShouldSkipInternalRoutes(t *testing.T) {
	t.Parallel()
	if !shouldSkipRoute("/health/ready") || !shouldSkipRoute("/v1/admin/stats/overview") {
		t.Fatal("expected health and admin stats routes to be skipped")
	}
	if shouldSkipRoute("/v1/me") || shouldSkipRoute("/v1/sync") {
		t.Fatal("did not expect application routes to be skipped")
	}
}

func TestNormalizeRouteUsesUnmatchedLabel(t *testing.T) {
	t.Parallel()
	if got := normalizeRoute(""); got != unmatchedRoute {
		t.Fatalf("got %q", got)
	}
	if got := normalizeRoute("/v1/me"); got != "/v1/me" {
		t.Fatalf("got %q", got)
	}
}

func TestRequestTypeForRoute(t *testing.T) {
	t.Parallel()
	cases := []struct {
		route string
		want  string
	}{
		{"/v1/auth/login", RequestTypeAuth},
		{"/v1/auth/refresh", RequestTypeAuth},
		{"/v1/me", RequestTypeAccount},
		{"/v1/me/password", RequestTypeAccount},
		{"/v1/sync", RequestTypeSync},
		{"/v1/snapshot", RequestTypeSnapshot},
		{"/v1/sessions", RequestTypeSessions},
		{"/v1/sessions/{id}/solves", RequestTypeSessions},
		{"/v1/stats", RequestTypeStats},
		{"/v1", RequestTypeOther},
		{"", RequestTypeOther},
		{"/unknown", RequestTypeOther},
		{"/v1/auth", RequestTypeOther},
	}
	for _, tc := range cases {
		if got := requestTypeForRoute(tc.route); got != tc.want {
			t.Errorf("requestTypeForRoute(%q) = %q, want %q", tc.route, got, tc.want)
		}
	}
}
