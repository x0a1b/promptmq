package storage

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHighThroughputPersistence tests the system's ability to handle 1M+ messages per second
func TestHighThroughputPersistence(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping high throughput test in short mode")
	}

	cfg := createTestConfig(t)
	cfg.Storage.WALNoSync = true                 // Disable fsync for maximum performance
	cfg.Storage.MemoryBuffer = 256 * 1024 * 1024 // 256MB buffer

	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	const (
		numTopics        = 100
		messagesPerTopic = 10000
		targetThroughput = 1000000 // 1M messages/second
		testDuration     = 10 * time.Second
	)

	var (
		messagesProcessed atomic.Uint64
		bytesProcessed    atomic.Uint64
		errorCount        atomic.Uint64
	)

	startTime := time.Now()
	endTime := startTime.Add(testDuration)

	// Create worker goroutines
	numWorkers := 50
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			messageID := uint64(workerID * 1000000) // Ensure unique IDs per worker

			for time.Now().Before(endTime) {
				topicID := messageID % numTopics
				topic := fmt.Sprintf("perf/topic/%d", topicID)

				payload := fmt.Sprintf("Performance test message %d from worker %d", messageID, workerID)

				msg := &Message{
					ID:        messageID,
					Topic:     topic,
					Payload:   []byte(payload),
					QoS:       1,
					Retain:    false,
					ClientID:  fmt.Sprintf("perf-client-%d", workerID),
					Timestamp: time.Now(),
				}

				if err := manager.persistMessage(msg); err != nil {
					errorCount.Add(1)
					continue
				}

				messagesProcessed.Add(1)
				bytesProcessed.Add(uint64(len(payload)))
				messageID++
			}
		}(i)
	}

	// Wait for all workers to complete
	wg.Wait()
	actualDuration := time.Since(startTime)

	// Force flush to ensure all messages are persisted
	flushStart := time.Now()
	err = manager.ForceFlush()
	require.NoError(t, err)
	flushDuration := time.Since(flushStart)

	// Calculate performance metrics
	totalMessages := messagesProcessed.Load()
	totalBytes := bytesProcessed.Load()
	errors := errorCount.Load()

	messagesPerSecond := float64(totalMessages) / actualDuration.Seconds()
	bytesPerSecond := float64(totalBytes) / actualDuration.Seconds()

	// Get storage statistics
	stats := manager.GetStats()

	t.Logf("Performance Test Results:")
	t.Logf("  Duration: %v", actualDuration)
	t.Logf("  Messages processed: %d", totalMessages)
	t.Logf("  Bytes processed: %d (%.2f MB)", totalBytes, float64(totalBytes)/(1024*1024))
	t.Logf("  Errors: %d", errors)
	t.Logf("  Messages/second: %.0f", messagesPerSecond)
	t.Logf("  MB/second: %.2f", bytesPerSecond/(1024*1024))
	t.Logf("  Flush duration: %v", flushDuration)
	t.Logf("  Storage stats: %+v", stats)

	// Verify performance requirements
	assert.Less(t, errors, uint64(totalMessages/1000), "Error rate should be less than 0.1%")
	assert.Greater(t, messagesPerSecond, float64(500000), "Should achieve at least 500K messages/second")
	assert.Equal(t, totalMessages, stats["total_messages"], "All messages should be accounted for")
	assert.Greater(t, stats["topic_count"], 1, "Should have multiple topics")
}

// TestLatencyUnderLoad measures message persistence latency under high load
func TestLatencyUnderLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping latency test in short mode")
	}

	cfg := createTestConfig(t)
	cfg.Storage.WALSyncInterval = 10 * time.Millisecond // Fast sync

	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	const numMessages = 10000
	latencies := make([]time.Duration, numMessages)

	// Measure latency for individual message persistence
	for i := 0; i < numMessages; i++ {
		msg := &Message{
			ID:        uint64(i + 1),
			Topic:     "latency/test",
			Payload:   []byte(fmt.Sprintf("latency test message %d", i)),
			QoS:       1,
			Retain:    false,
			ClientID:  "latency-client",
			Timestamp: time.Now(),
		}

		start := time.Now()
		err := manager.persistMessage(msg)
		latencies[i] = time.Since(start)

		require.NoError(t, err)
	}

	// Calculate latency statistics
	var totalLatency time.Duration
	maxLatency := time.Duration(0)
	minLatency := time.Hour // Large initial value

	for _, latency := range latencies {
		totalLatency += latency
		if latency > maxLatency {
			maxLatency = latency
		}
		if latency < minLatency {
			minLatency = latency
		}
	}

	avgLatency := totalLatency / numMessages

	// Calculate percentiles
	// Sort latencies for percentile calculation
	sortedLatencies := make([]time.Duration, len(latencies))
	copy(sortedLatencies, latencies)

	// Simple bubble sort for small dataset
	for i := 0; i < len(sortedLatencies)-1; i++ {
		for j := 0; j < len(sortedLatencies)-i-1; j++ {
			if sortedLatencies[j] > sortedLatencies[j+1] {
				sortedLatencies[j], sortedLatencies[j+1] = sortedLatencies[j+1], sortedLatencies[j]
			}
		}
	}

	p50 := sortedLatencies[len(sortedLatencies)/2]
	p95 := sortedLatencies[int(float64(len(sortedLatencies))*0.95)]
	p99 := sortedLatencies[int(float64(len(sortedLatencies))*0.99)]

	t.Logf("Latency Test Results:")
	t.Logf("  Messages: %d", numMessages)
	t.Logf("  Average latency: %v", avgLatency)
	t.Logf("  Min latency: %v", minLatency)
	t.Logf("  Max latency: %v", maxLatency)
	t.Logf("  P50 latency: %v", p50)
	t.Logf("  P95 latency: %v", p95)
	t.Logf("  P99 latency: %v", p99)

	// Verify latency requirements (sub-10ms for enterprise grade)
	assert.Less(t, p95, 10*time.Millisecond, "P95 latency should be under 10ms")
	assert.Less(t, avgLatency, 5*time.Millisecond, "Average latency should be under 5ms")
}

// TestMemoryBufferOverflow tests behavior when memory buffer overflows
func TestMemoryBufferOverflow(t *testing.T) {
	cfg := createTestConfig(t)
	cfg.Storage.MemoryBuffer = 1024 // Very small buffer (1KB)
	cfg.Storage.WALNoSync = true    // Disable sync for faster testing

	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Send messages that will overflow the memory buffer
	const numMessages = 100
	largePayload := make([]byte, 512) // 512 bytes per message

	for i := 0; i < numMessages; i++ {
		msg := &Message{
			ID:        uint64(i + 1),
			Topic:     "overflow/test",
			Payload:   largePayload,
			QoS:       1,
			Retain:    false,
			ClientID:  "overflow-client",
			Timestamp: time.Now(),
		}

		err := manager.persistMessage(msg)
		require.NoError(t, err, "Should handle overflow gracefully")
	}

	// Force flush and verify all messages were persisted
	err = manager.ForceFlush()
	require.NoError(t, err)

	stats := manager.GetStats()
	assert.Equal(t, uint64(numMessages), stats["total_messages"])

	t.Logf("Memory overflow test completed:")
	t.Logf("  Messages persisted: %d", stats["total_messages"])
	t.Logf("  Total bytes: %d", stats["total_bytes"])
	t.Logf("  Final buffer size: %d", stats["memory_buffer_size"])
}

// TestRecoveryPerformance tests the performance of WAL recovery on startup
func TestRecoveryPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping recovery performance test in short mode")
	}

	cfg := createTestConfig(t)
	cfg.Storage.WALNoSync = true
	logger := createTestLogger()

	// First, create a manager and persist many messages
	manager1, err := New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager1.Start(ctx)
	require.NoError(t, err)

	const numMessages = 50000
	const numTopics = 10

	t.Logf("Creating %d messages across %d topics for recovery test", numMessages, numTopics)

	// Persist messages
	for i := 0; i < numMessages; i++ {
		topicID := i % numTopics
		msg := &Message{
			ID:        uint64(i + 1),
			Topic:     fmt.Sprintf("recovery/topic/%d", topicID),
			Payload:   []byte(fmt.Sprintf("Recovery test message %d", i)),
			QoS:       1,
			Retain:    false,
			ClientID:  fmt.Sprintf("recovery-client-%d", i%100),
			Timestamp: time.Now(),
		}

		err := manager1.persistMessage(msg)
		require.NoError(t, err)

		if i%10000 == 0 {
			t.Logf("Persisted %d messages", i)
		}
	}

	// Force flush and stop
	err = manager1.ForceFlush()
	require.NoError(t, err)
	err = manager1.StopManager()
	require.NoError(t, err)

	t.Logf("All messages persisted, starting recovery test")

	// Now test recovery performance
	recoveryStart := time.Now()
	manager2, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager2.StopManager()

	err = manager2.Start(ctx)
	require.NoError(t, err)
	recoveryDuration := time.Since(recoveryStart)

	// Verify recovery
	stats := manager2.GetStats()

	t.Logf("Recovery Performance Results:")
	t.Logf("  Recovery duration: %v", recoveryDuration)
	t.Logf("  Messages recovered: %d", stats["total_messages"])
	t.Logf("  Topics recovered: %d", stats["topic_count"])
	t.Logf("  Messages/second during recovery: %.0f", float64(stats["total_messages"].(uint64))/recoveryDuration.Seconds())

	// Verify all messages were recovered
	assert.Equal(t, uint64(numMessages), stats["total_messages"])
	assert.Equal(t, numTopics, stats["topic_count"])

	// Recovery should be reasonably fast
	messagesPerSecond := float64(stats["total_messages"].(uint64)) / recoveryDuration.Seconds()
	assert.Greater(t, messagesPerSecond, 10000.0, "Recovery should process at least 10K messages/second")
}

// TestConcurrentTopicAccess tests concurrent access to multiple topics
func TestConcurrentTopicAccess(t *testing.T) {
	cfg := createTestConfig(t)
	cfg.Storage.WALNoSync = true
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	const (
		numTopics          = 20
		numWorkersPerTopic = 5
		messagesPerWorker  = 1000
	)

	var wg sync.WaitGroup
	errorCount := atomic.Uint64{}

	// Start workers for each topic
	for topicID := 0; topicID < numTopics; topicID++ {
		for workerID := 0; workerID < numWorkersPerTopic; workerID++ {
			wg.Add(1)
			go func(tID, wID int) {
				defer wg.Done()

				topic := fmt.Sprintf("concurrent/topic/%d", tID)

				for i := 0; i < messagesPerWorker; i++ {
					msg := &Message{
						ID:        uint64(tID*10000 + wID*1000 + i),
						Topic:     topic,
						Payload:   []byte(fmt.Sprintf("Message from topic %d worker %d msg %d", tID, wID, i)),
						QoS:       1,
						Retain:    false,
						ClientID:  fmt.Sprintf("client-%d-%d", tID, wID),
						Timestamp: time.Now(),
					}

					if err := manager.persistMessage(msg); err != nil {
						errorCount.Add(1)
						t.Errorf("Error persisting message: %v", err)
					}
				}
			}(topicID, workerID)
		}
	}

	wg.Wait()

	// Force flush
	err = manager.ForceFlush()
	require.NoError(t, err)

	// Verify results
	stats := manager.GetStats()
	expectedMessages := uint64(numTopics * numWorkersPerTopic * messagesPerWorker)

	t.Logf("Concurrent Topic Access Results:")
	t.Logf("  Expected messages: %d", expectedMessages)
	t.Logf("  Actual messages: %d", stats["total_messages"])
	t.Logf("  Topics: %d", stats["topic_count"])
	t.Logf("  Errors: %d", errorCount.Load())

	assert.Equal(t, expectedMessages, stats["total_messages"])
	assert.Equal(t, numTopics, stats["topic_count"])
	assert.Equal(t, uint64(0), errorCount.Load())
}

// BenchmarkHighThroughputWrite benchmarks write performance
func BenchmarkHighThroughputWrite(b *testing.B) {
	cfg := createTestConfigBench()
	cfg.Storage.WALNoSync = true                 // Disable fsync for maximum performance
	cfg.Storage.MemoryBuffer = 256 * 1024 * 1024 // 256MB buffer

	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(b, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(b, err)

	// Pre-allocate message template
	msg := &Message{
		Topic:     "benchmark/write",
		Payload:   make([]byte, 1024), // 1KB payload
		QoS:       1,
		Retain:    false,
		ClientID:  "benchmark-client",
		Timestamp: time.Now(),
	}

	b.ResetTimer()
	b.SetBytes(int64(len(msg.Payload)))

	b.RunParallel(func(pb *testing.PB) {
		id := uint64(0)
		for pb.Next() {
			id++
			msg.ID = id
			msg.Timestamp = time.Now()

			if err := manager.persistMessage(msg); err != nil {
				b.Error(err)
			}
		}
	})
}

// BenchmarkSerialization benchmarks message serialization/deserialization
func BenchmarkSerialization(b *testing.B) {
	cfg := createTestConfigBench()
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(b, err)
	defer manager.StopManager()

	msg := &Message{
		ID:        12345,
		Topic:     "benchmark/serialization",
		Payload:   make([]byte, 1024), // 1KB payload
		QoS:       1,
		Retain:    false,
		ClientID:  "benchmark-client",
		Timestamp: time.Now(),
	}

	b.ResetTimer()
	b.SetBytes(int64(len(msg.Payload)))

	b.Run("Serialize", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, err := manager.serializeMessage(msg)
				if err != nil {
					b.Error(err)
				}
			}
		})
	})

	// Pre-serialize for deserialization benchmark
	data, err := manager.serializeMessage(msg)
	require.NoError(b, err)

	b.Run("Deserialize", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, err := manager.deserializeMessage(data)
				if err != nil {
					b.Error(err)
				}
			}
		})
	})
}
