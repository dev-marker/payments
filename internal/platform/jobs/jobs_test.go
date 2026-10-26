package jobs_test

import (
	"testing"
	"time"

	"github.com/fiston-user/payments/internal/platform/jobs"
)

func TestNext(t *testing.T) {
	t.Parallel()
	daily := jobs.Job{Every: 24 * time.Hour, At: 2 * time.Hour}
	often := jobs.Job{Every: 15 * time.Second, At: 2 * time.Hour}
	tests := []struct {
		job       jobs.Job
		now, want string
	}{
		{daily, "2026-11-13T01:59:59Z", "2026-11-13T02:00:00Z"},
		{daily, "2026-11-13T02:00:00Z", "2026-11-14T02:00:00Z"}, // this one is running
		{daily, "2026-11-13T23:30:00Z", "2026-11-14T02:00:00Z"},
		{often, "2026-11-13T10:00:07Z", "2026-11-13T10:00:15Z"},
	}
	for _, tt := range tests {
		now, _ := time.Parse(time.RFC3339, tt.now)
		if got := tt.job.Next(now).Format(time.RFC3339); got != tt.want {
			t.Errorf("%v at %v: Next(%s) = %s, want %s", tt.job.Every, tt.job.At, tt.now, got, tt.want)
		}
	}
}
