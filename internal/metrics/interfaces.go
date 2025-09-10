package metrics

import "time"

// StorageMetrics defines the interface for storage layer metrics integration
type StorageMetrics interface {
	// RecordSQLiteWrite records SQLite write metrics (latency and success/failure)
	RecordSQLiteWrite(latency time.Duration, success bool)

	// RecordStorageUsage records current storage usage in bytes
	RecordStorageUsage(bytes int64)

	// RecordRecovery records recovery duration
	RecordRecovery(duration time.Duration)
}

// NoOpStorageMetrics provides a no-op implementation for testing
type NoOpStorageMetrics struct{}

func (n *NoOpStorageMetrics) RecordSQLiteWrite(latency time.Duration, success bool) {}
func (n *NoOpStorageMetrics) RecordStorageUsage(bytes int64)                        {}
func (n *NoOpStorageMetrics) RecordRecovery(duration time.Duration)                 {}

// Ensure Server implements StorageMetrics interface at compile time
var _ StorageMetrics = (*Server)(nil)
var _ StorageMetrics = (*NoOpStorageMetrics)(nil)
