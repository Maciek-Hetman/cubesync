package sync

import (
	"math"
	"testing"
)

const dnf = math.MaxInt64

func TestComputeAoNFromTimes(t *testing.T) {
	tests := []struct {
		name    string
		times   []int64
		n       int
		want    int64
		wantDNF bool
		wantNil bool
	}{
		{name: "ao5 trims one each side", times: []int64{1000, 1100, 1200, 1300, 1400}, n: 5, want: 1200},
		{name: "ao5 one DNF is trimmed", times: []int64{1000, 1100, 1200, 1300, dnf}, n: 5, want: 1200},
		{name: "ao5 two DNFs", times: []int64{1000, dnf, 1200, 1300, dnf}, n: 5, wantDNF: true},
		{name: "ao5 rounds to nearest ms", times: []int64{1000, 1001, 1002, 1002, 2000}, n: 5, want: 1002},
		{name: "ao12 trims one each side", times: seq(12), n: 12, want: 6500},
		{name: "ao50 tolerates three DNFs", times: withDNFs(seq(50), 3), n: 50, want: 28500},
		{name: "ao50 four DNFs", times: withDNFs(seq(50), 4), n: 50, wantDNF: true},
		{name: "ao100 trims five each side", times: seq(100), n: 100, want: 50500},
		{name: "ao100 six DNFs", times: withDNFs(seq(100), 6), n: 100, wantDNF: true},
		{name: "not enough solves", times: []int64{1000, 1100}, n: 5, wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, isDNF := computeAoNFromTimes(tt.times, tt.n)
			if isDNF != tt.wantDNF {
				t.Fatalf("isDNF = %v, want %v", isDNF, tt.wantDNF)
			}
			if tt.wantDNF || tt.wantNil {
				if got != nil {
					t.Fatalf("value = %d, want nil", *got)
				}
				return
			}
			if got == nil || *got != tt.want {
				t.Fatalf("value = %v, want %d", got, tt.want)
			}
		})
	}
}

// seq returns 1000, 2000, ..., n*1000.
func seq(n int) []int64 {
	times := make([]int64, n)
	for i := range times {
		times[i] = int64(i+1) * 1000
	}
	return times
}

// withDNFs replaces the first count times with DNFs.
func withDNFs(times []int64, count int) []int64 {
	for i := 0; i < count; i++ {
		times[i] = dnf
	}
	return times
}
