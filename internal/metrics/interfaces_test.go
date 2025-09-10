package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNoOpStorageMetrics(t *testing.T) {
	metrics := &NoOpStorageMetrics{}

	// Verify it implements StorageMetrics interface
	var _ StorageMetrics = metrics

	// Test that all methods can be called without panicking
	t.Run("RecordSQLiteWrite", func(t *testing.T) {
		// Should not panic
		metrics.RecordSQLiteWrite(100*time.Millisecond, true)
		metrics.RecordSQLiteWrite(50*time.Microsecond, false)
	})

	t.Run("RecordStorageUsage", func(t *testing.T) {
		// Should not panic
		metrics.RecordStorageUsage(1024)
		metrics.RecordStorageUsage(0)
		metrics.RecordStorageUsage(-1) // negative values
	})

	t.Run("RecordRecovery", func(t *testing.T) {
		// Should not panic
		metrics.RecordRecovery(500 * time.Millisecond)
		metrics.RecordRecovery(0)
		metrics.RecordRecovery(time.Second)
	})
}

func TestStorageMetricsInterface(t *testing.T) {
	// Test that Server implements StorageMetrics interface
	server := &Server{}
	var _ StorageMetrics = server

	// Test that NoOpStorageMetrics implements StorageMetrics interface
	noOp := &NoOpStorageMetrics{}
	var _ StorageMetrics = noOp
}

func TestStorageMetricsInterfaceMethods(t *testing.T) {
	tests := []struct {
		name    string
		metrics StorageMetrics
	}{
		{
			name:    "NoOpStorageMetrics",
			metrics: &NoOpStorageMetrics{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test all interface methods exist and can be called
			assert.NotPanics(t, func() {
				tt.metrics.RecordSQLiteWrite(time.Millisecond, true)
				tt.metrics.RecordStorageUsage(1024)
				tt.metrics.RecordRecovery(time.Second)
			})
		})
	}
}

func TestNoOpStorageMetricsEdgeCases(t *testing.T) {
	metrics := &NoOpStorageMetrics{}

	t.Run("ExtremeDurations", func(t *testing.T) {
		// Test with extreme duration values
		assert.NotPanics(t, func() {
			metrics.RecordSQLiteWrite(0, true)
			metrics.RecordSQLiteWrite(time.Hour*24*365, false) // 1 year
			metrics.RecordRecovery(time.Nanosecond)
			metrics.RecordRecovery(time.Hour * 24 * 365) // 1 year
		})
	})

	t.Run("ExtremeByteSizes", func(t *testing.T) {
		// Test with extreme byte values
		assert.NotPanics(t, func() {
			metrics.RecordStorageUsage(0)
			metrics.RecordStorageUsage(-9223372036854775808) // min int64
			metrics.RecordStorageUsage(9223372036854775807)  // max int64
		})
	})

	t.Run("ConcurrentCalls", func(t *testing.T) {
		// Test concurrent access to no-op metrics
		done := make(chan bool, 3)

		go func() {
			for i := 0; i < 100; i++ {
				metrics.RecordSQLiteWrite(time.Millisecond, i%2 == 0)
			}
			done <- true
		}()

		go func() {
			for i := 0; i < 100; i++ {
				metrics.RecordStorageUsage(int64(i * 1024))
			}
			done <- true
		}()

		go func() {
			for i := 0; i < 100; i++ {
				metrics.RecordRecovery(time.Millisecond * time.Duration(i))
			}
			done <- true
		}()

		// Wait for all goroutines to complete
		for i := 0; i < 3; i++ {
			<-done
		}
	})
}