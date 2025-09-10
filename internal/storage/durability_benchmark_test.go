package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/x0a1b/promptmq/internal/config"
)

// BenchmarkSyncModePerformance compares performance of all sync modes
func BenchmarkSyncModePerformance(b *testing.B) {
	syncModes := []struct {
		name         string
		mode         string
		batchSize    int
		syncInterval time.Duration
	}{
		{"Immediate", "immediate", 0, 0},
		{"Periodic_100ms", "periodic", 0, 100 * time.Millisecond},
		{"Periodic_50ms", "periodic", 0, 50 * time.Millisecond},
		{"Batch_10", "batch", 10, 0},
		{"Batch_50", "batch", 50, 0},
		{"Batch_100", "batch", 100, 0},
	}

	for _, sm := range syncModes {
		b.Run(sm.name, func(b *testing.B) {
			cfg := createBenchmarkConfig(b)
			cfg.Storage.WAL.SyncMode = sm.mode
			cfg.Storage.WAL.BatchSyncSize = sm.batchSize
			cfg.Storage.WAL.SyncInterval = sm.syncInterval

			manager, err := New(cfg, zerolog.New(nil).Level(zerolog.Disabled))
			if err != nil {
				b.Fatalf("Failed to create manager: %v", err)
			}
			defer manager.StopManager()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err = manager.Start(ctx)
			if err != nil {
				b.Fatalf("Failed to start manager: %v", err)
			}

			b.ResetTimer()

			// Benchmark message throughput
			for i := 0; i < b.N; i++ {
				msg := &Message{
					ID:        uint64(i + 1),
					Topic:     fmt.Sprintf("bench/%s/topic", sm.name),
					Payload:   []byte(fmt.Sprintf("benchmark message %d", i)),
					QoS:       1,
					ClientID:  "bench-client",
					Timestamp: time.Now(),
				}

				err := manager.persistMessage(msg)
				if err != nil {
					b.Fatalf("Failed to persist message: %v", err)
				}
			}

			b.StopTimer()

			// Ensure all messages are persisted
			if sm.mode != "immediate" {
				manager.ForceFlush()
			}
		})
	}
}

// BenchmarkWriteLatency measures write latency for different sync modes
func BenchmarkWriteLatency(b *testing.B) {
	syncModes := []string{"immediate", "periodic", "batch"}

	for _, mode := range syncModes {
		b.Run(mode, func(b *testing.B) {
			cfg := createBenchmarkConfig(b)
			cfg.Storage.WAL.SyncMode = mode
			cfg.Storage.WAL.BatchSyncSize = 10

			manager, err := New(cfg, zerolog.New(nil).Level(zerolog.Disabled))
			if err != nil {
				b.Fatalf("Failed to create manager: %v", err)
			}
			defer manager.StopManager()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err = manager.Start(ctx)
			if err != nil {
				b.Fatalf("Failed to start manager: %v", err)
			}

			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				msg := &Message{
					ID:        uint64(i + 1),
					Topic:     fmt.Sprintf("latency/%s", mode),
					Payload:   []byte(fmt.Sprintf("latency test message %d", i)),
					QoS:       1,
					ClientID:  "latency-client",
					Timestamp: time.Now(),
				}

				start := time.Now()
				err := manager.persistMessage(msg)
				duration := time.Since(start)

				if err != nil {
					b.Fatalf("Failed to persist message: %v", err)
				}

				// For immediate mode, we care about individual write latency
				if mode == "immediate" && i == 0 {
					b.Logf("First write latency for %s: %v", mode, duration)
				}
			}
		})
	}
}

// BenchmarkRecoveryPerformance measures recovery time for different message counts
func BenchmarkRecoveryPerformance(b *testing.B) {
	messageCounts := []int{100, 1000, 10000}

	for _, count := range messageCounts {
		b.Run(fmt.Sprintf("%d_messages", count), func(b *testing.B) {
			cfg := createBenchmarkConfig(b)
			cfg.Storage.WAL.SyncMode = "immediate"

			// Phase 1: Write messages
			manager1, err := New(cfg, zerolog.New(nil).Level(zerolog.Disabled))
			if err != nil {
				b.Fatalf("Failed to create manager: %v", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err = manager1.Start(ctx)
			if err != nil {
				b.Fatalf("Failed to start manager: %v", err)
			}

			// Write test messages
			for i := 0; i < count; i++ {
				msg := &Message{
					ID:        uint64(i + 1),
					Topic:     fmt.Sprintf("recovery/bench/%d", i%10), // 10 topics
					Payload:   []byte(fmt.Sprintf("recovery benchmark message %d", i)),
					QoS:       1,
					ClientID:  "recovery-bench-client",
					Timestamp: time.Now(),
				}

				err := manager1.persistMessage(msg)
				if err != nil {
					b.Fatalf("Failed to persist message: %v", err)
				}
			}

			manager1.StopManager()

			// Phase 2: Benchmark recovery
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				manager2, err := New(cfg, zerolog.New(nil).Level(zerolog.Disabled))
				if err != nil {
					b.Fatalf("Failed to create recovery manager: %v", err)
				}

				ctx2, cancel2 := context.WithCancel(context.Background())

				start := time.Now()
				err = manager2.Start(ctx2)
				recoveryTime := time.Since(start)

				if err != nil {
					cancel2()
					manager2.StopManager()
					b.Fatalf("Failed to start recovery manager: %v", err)
				}

				stats := manager2.GetStats()
				recoveredCount := stats["total_messages"].(uint64)

				if recoveredCount != uint64(count) {
					b.Logf("Warning: Expected %d messages, recovered %d", count, recoveredCount)
				}

				if i == 0 {
					b.Logf("Recovery of %d messages took: %v", count, recoveryTime)
				}

				cancel2()
				manager2.StopManager()
			}
		})
	}
}

// BenchmarkConcurrentWrites measures performance under concurrent load
func BenchmarkConcurrentWrites(b *testing.B) {
	syncModes := []string{"immediate", "periodic", "batch"}
	workerCounts := []int{1, 4, 8}

	for _, mode := range syncModes {
		for _, workers := range workerCounts {
			b.Run(fmt.Sprintf("%s_%d_workers", mode, workers), func(b *testing.B) {
				cfg := createBenchmarkConfig(b)
				cfg.Storage.WAL.SyncMode = mode
				cfg.Storage.WAL.BatchSyncSize = 20

				manager, err := New(cfg, zerolog.New(nil).Level(zerolog.Disabled))
				if err != nil {
					b.Fatalf("Failed to create manager: %v", err)
				}
				defer manager.StopManager()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				err = manager.Start(ctx)
				if err != nil {
					b.Fatalf("Failed to start manager: %v", err)
				}

				b.ResetTimer()

				// Run concurrent workers
				b.RunParallel(func(pb *testing.PB) {
					workerID := 0
					for pb.Next() {
						msg := &Message{
							ID:        uint64(time.Now().UnixNano()), // Unique ID
							Topic:     fmt.Sprintf("concurrent/%s/worker%d", mode, workerID),
							Payload:   []byte(fmt.Sprintf("concurrent message from worker %d", workerID)),
							QoS:       1,
							ClientID:  fmt.Sprintf("worker-%d", workerID),
							Timestamp: time.Now(),
						}

						err := manager.persistMessage(msg)
						if err != nil {
							b.Fatalf("Failed to persist message: %v", err)
						}
						workerID++
					}
				})
			})
		}
	}
}

// BenchmarkMemoryUsage measures memory consumption during operations
func BenchmarkMemoryUsage(b *testing.B) {
	cfg := createBenchmarkConfig(b)
	cfg.Storage.WAL.SyncMode = "periodic"
	cfg.Storage.MemoryBuffer = 10 * 1024 * 1024 // 10MB buffer

	manager, err := New(cfg, zerolog.New(nil).Level(zerolog.Disabled))
	if err != nil {
		b.Fatalf("Failed to create manager: %v", err)
	}
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	if err != nil {
		b.Fatalf("Failed to start manager: %v", err)
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		msg := &Message{
			ID:        uint64(i + 1),
			Topic:     fmt.Sprintf("memory/test/%d", i%100), // 100 topics
			Payload:   make([]byte, 1024),                   // 1KB payload
			QoS:       1,
			ClientID:  "memory-test-client",
			Timestamp: time.Now(),
		}

		err := manager.persistMessage(msg)
		if err != nil {
			b.Fatalf("Failed to persist message: %v", err)
		}

		// Periodically check memory usage
		if i%1000 == 0 {
			stats := manager.GetStats()
			bufferSize := stats["memory_buffer_size"].(uint64)
			b.Logf("Iteration %d: Memory buffer size: %d bytes", i, bufferSize)
		}
	}
}

// BenchmarkBatchSizeImpact measures impact of different batch sizes
func BenchmarkBatchSizeImpact(b *testing.B) {
	batchSizes := []int{1, 10, 50, 100, 500}

	for _, batchSize := range batchSizes {
		b.Run(fmt.Sprintf("batch_%d", batchSize), func(b *testing.B) {
			cfg := createBenchmarkConfig(b)
			cfg.Storage.WAL.SyncMode = "batch"
			cfg.Storage.WAL.BatchSyncSize = batchSize

			manager, err := New(cfg, zerolog.New(nil).Level(zerolog.Disabled))
			if err != nil {
				b.Fatalf("Failed to create manager: %v", err)
			}
			defer manager.StopManager()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err = manager.Start(ctx)
			if err != nil {
				b.Fatalf("Failed to start manager: %v", err)
			}

			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				msg := &Message{
					ID:        uint64(i + 1),
					Topic:     fmt.Sprintf("batch/size/%d", batchSize),
					Payload:   []byte(fmt.Sprintf("batch size test message %d", i)),
					QoS:       1,
					ClientID:  "batch-client",
					Timestamp: time.Now(),
				}

				err := manager.persistMessage(msg)
				if err != nil {
					b.Fatalf("Failed to persist message: %v", err)
				}
			}

			// Ensure final batch is written
			manager.ForceFlush()
		})
	}
}

// BenchmarkDurabilityVsThroughput compares throughput vs durability guarantees
func BenchmarkDurabilityVsThroughput(b *testing.B) {
	configurations := []struct {
		name        string
		syncMode    string
		forceFsync  bool
		memBuffer   uint64
		expectedOps int // Expected ops/sec (approximate)
	}{
		{"MaxThroughput", "periodic", false, 100 * 1024 * 1024, 500000}, // 100MB buffer, no fsync
		{"Balanced", "batch", false, 10 * 1024 * 1024, 200000},          // 10MB buffer, batch sync
		{"MaxDurability", "immediate", true, 1024, 50000},               // 1KB buffer, immediate fsync
	}

	for _, cfg_test := range configurations {
		b.Run(cfg_test.name, func(b *testing.B) {
			cfg := createBenchmarkConfig(b)
			cfg.Storage.WAL.SyncMode = cfg_test.syncMode
			cfg.Storage.WAL.ForceFsync = cfg_test.forceFsync
			cfg.Storage.MemoryBuffer = cfg_test.memBuffer
			cfg.Storage.WAL.BatchSyncSize = 50

			manager, err := New(cfg, zerolog.New(nil).Level(zerolog.Disabled))
			if err != nil {
				b.Fatalf("Failed to create manager: %v", err)
			}
			defer manager.StopManager()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err = manager.Start(ctx)
			if err != nil {
				b.Fatalf("Failed to start manager: %v", err)
			}

			b.ResetTimer()
			start := time.Now()

			for i := 0; i < b.N; i++ {
				msg := &Message{
					ID:        uint64(i + 1),
					Topic:     fmt.Sprintf("durability/%s", cfg_test.name),
					Payload:   []byte(fmt.Sprintf("durability vs throughput test %d", i)),
					QoS:       1,
					ClientID:  "durability-client",
					Timestamp: time.Now(),
				}

				err := manager.persistMessage(msg)
				if err != nil {
					b.Fatalf("Failed to persist message: %v", err)
				}
			}

			duration := time.Since(start)
			opsPerSec := float64(b.N) / duration.Seconds()

			b.StopTimer()

			// Ensure all messages are persisted for final measurement
			if cfg_test.syncMode != "immediate" {
				manager.ForceFlush()
			}

			b.Logf("%s: %.0f ops/sec (target: %d ops/sec)",
				cfg_test.name, opsPerSec, cfg_test.expectedOps)

			// Report whether we met performance expectations
			if opsPerSec < float64(cfg_test.expectedOps)*0.5 { // Allow 50% tolerance
				b.Logf("Warning: Performance below 50%% of target for %s", cfg_test.name)
			}
		})
	}
}

func createBenchmarkConfig(b *testing.B) *config.Config {
	tempDir := b.TempDir()

	return &config.Config{
		Storage: config.StorageConfig{
			DataDir:         tempDir + "/data",
			WALDir:          tempDir + "/wal",
			MemoryBuffer:    1024 * 1024, // 1MB
			WALSyncInterval: 100 * time.Millisecond,
			WALNoSync:       false,
			WAL: config.WALConfig{
				SyncMode:                "periodic",
				SyncInterval:            100 * time.Millisecond,
				BatchSyncSize:           10,
				ForceFsync:              false,
				CrashRecoveryValidation: false, // Disabled for benchmarks
			},
			Compaction: config.CompactionConfig{
				MaxMessageAge:     24 * time.Hour,
				MaxWALSize:        100 * 1024 * 1024, // 100MB
				CheckInterval:     1 * time.Hour,     // Disabled for benchmarks
				ConcurrentWorkers: 1,
				BatchSize:         1000,
			},
		},
	}
}
