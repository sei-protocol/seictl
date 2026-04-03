package engine

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// taskDuration records the execution duration of each task in seconds.
	taskDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "seictl_task_duration_seconds",
			Help:    "Duration of task execution in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"type", "status"},
	)

	// taskSubmissions counts the total number of task submissions.
	taskSubmissions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "seictl_task_submissions_total",
			Help: "Total number of tasks submitted.",
		},
		[]string{"type"},
	)

	// taskFailures counts the total number of task failures.
	taskFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "seictl_task_failures_total",
			Help: "Total number of tasks that failed.",
		},
		[]string{"type"},
	)
)

func init() {
	prometheus.MustRegister(taskDuration)
	prometheus.MustRegister(taskSubmissions)
	prometheus.MustRegister(taskFailures)
}
