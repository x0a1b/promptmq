package metrics

import "time"

// StorageMetrics defines the interface for storage layer metrics integration
type StorageMetrics interface {
	// RecordWALWrite records WAL write metrics (latency and success/failure)
	RecordWALWrite(latency time.Duration, success bool)

	// RecordWALBufferUsage records current WAL buffer usage in bytes
	RecordWALBufferUsage(bytes int64)

	// RecordWALRecovery records WAL recovery duration
	RecordWALRecovery(duration time.Duration)
}

// NoOpStorageMetrics provides a no-op implementation for testing
type NoOpStorageMetrics struct{}

func (n *NoOpStorageMetrics) RecordWALWrite(latency time.Duration, success bool) {}
func (n *NoOpStorageMetrics) RecordWALBufferUsage(bytes int64)                   {}
func (n *NoOpStorageMetrics) RecordWALRecovery(duration time.Duration)           {}

// Ensure Server implements StorageMetrics interface at compile time
var _ StorageMetrics = (*Server)(nil)
var _ StorageMetrics = (*NoOpStorageMetrics)(nil)
