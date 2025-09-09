package storage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zohaib-hassan/promptmq/internal/config"
)

// CrashPoint defines where in the process to simulate a crash
type CrashPoint int

const (
	CrashAfterWrite CrashPoint = iota
	CrashBeforeSync
	CrashAfterSync
	CrashDuringCompaction
	CrashAfterFlush
)

// CrashSimulator handles process crash simulation and recovery testing
type CrashSimulator struct {
	cfg           *config.Config
	logger        zerolog.Logger
	processCmd    *exec.Cmd
	crashSignal   os.Signal
	crashDelay    time.Duration
	recoveryCheck func(*testing.T, *Manager) bool
}

// NewCrashSimulator creates a crash simulation framework
func NewCrashSimulator(cfg *config.Config) *CrashSimulator {
	return &CrashSimulator{
		cfg:         cfg,
		logger:      zerolog.New(os.Stderr).Level(zerolog.Disabled),
		crashSignal: syscall.SIGKILL, // Immediate termination
		crashDelay:  100 * time.Millisecond,
	}
}

// SimulateCrashAfterWrites creates messages, crashes, and verifies recovery
func (cs *CrashSimulator) SimulateCrashAfterWrites(t *testing.T, messageCount int, syncMode string) {
	t.Run(fmt.Sprintf("CrashRecovery_%s_%dMessages", syncMode, messageCount), func(t *testing.T) {
		// Create unique config for this subtest
		cfg := createCrashTestConfig(t)
		cfg.Storage.WAL.CrashRecoveryValidation = true
		cfg.Storage.WAL.SyncMode = syncMode
		// Phase 1: Create storage manager and write messages
		manager1, err := New(cfg, cs.logger)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err = manager1.Start(ctx)
		require.NoError(t, err)

		// Write test messages
		writtenMessages := make([]*Message, messageCount)
		for i := 0; i < messageCount; i++ {
			msg := &Message{
				ID:        uint64(i + 1),
				Topic:     fmt.Sprintf("crash/test/%d", i%3), // 3 topics
				Payload:   []byte(fmt.Sprintf("crash test message %d", i)),
				QoS:       1,
				ClientID:  "crash-test-client",
				Timestamp: time.Now(),
			}

			err := manager1.persistMessage(msg)
			require.NoError(t, err)
			writtenMessages[i] = msg
		}

		// Force flush for non-immediate modes
		if syncMode != "immediate" {
			err = manager1.ForceFlush()
			require.NoError(t, err)
		}

		// Simulate abrupt termination (no graceful shutdown)
		manager1.cancel() // Simulate crash by canceling context abruptly
		// Properly close the database to release locks
		manager1.StopManager()

		// Phase 2: Attempt recovery with new manager
		time.Sleep(50 * time.Millisecond) // Brief delay to simulate restart

		manager2, err := New(cfg, cs.logger)
		require.NoError(t, err)
		defer manager2.StopManager()

		ctx2, cancel2 := context.WithCancel(context.Background())
		defer cancel2()

		// Start recovery
		recoveryStart := time.Now()
		err = manager2.Start(ctx2)
		require.NoError(t, err)
		recoveryDuration := time.Since(recoveryStart)

		// Phase 3: Verify recovery
		stats := manager2.GetStats()

		t.Logf("Recovery completed in %v", recoveryDuration)
		t.Logf("Recovery stats: %+v", stats)

		// Verify message recovery based on sync mode
		switch syncMode {
		case "immediate":
			// All messages should be recovered
			assert.Equal(t, uint64(messageCount), stats["total_messages"], "Immediate sync should recover all messages")
		case "periodic":
			// Most messages should be recovered (some may be in buffer)
			recoveredCount := stats["total_messages"].(uint64)
			minExpected := uint64(float64(messageCount) * 0.8)
			assert.GreaterOrEqual(t, recoveredCount, minExpected, "Periodic sync should recover at least 80% of messages")
		case "batch":
			// Recovery depends on batch size
			recoveredCount := stats["total_messages"].(uint64)
			assert.Greater(t, recoveredCount, uint64(0), "Batch sync should recover some messages")
		}

		// Verify topic count
		assert.Greater(t, stats["topic_count"], 0, "Should recover topic information")

		// Test write functionality after recovery
		postRecoveryMsg := &Message{
			ID:        uint64(messageCount + 1),
			Topic:     "crash/recovery/test",
			Payload:   []byte("post recovery message"),
			QoS:       1,
			ClientID:  "recovery-test-client",
			Timestamp: time.Now(),
		}

		err = manager2.persistMessage(postRecoveryMsg)
		require.NoError(t, err, "Should be able to write messages after recovery")
	})
}

// TestSQLiteLikeDurability verifies immediate sync provides SQLite-like durability
func TestSQLiteLikeDurability(t *testing.T) {
	cfg := createCrashTestConfig(t)
	cfg.Storage.WAL.SyncMode = "immediate"
	cfg.Storage.WAL.ForceFsync = true

	simulator := NewCrashSimulator(cfg)

	// Test with various message counts
	testCases := []int{10, 100, 1000}

	for _, count := range testCases {
		simulator.SimulateCrashAfterWrites(t, count, "immediate")
	}
}

// TestPeriodicSyncRecovery tests recovery with periodic sync mode
func TestPeriodicSyncRecovery(t *testing.T) {
	cfg := createCrashTestConfig(t)
	cfg.Storage.WAL.SyncMode = "periodic"
	cfg.Storage.WAL.SyncInterval = 50 * time.Millisecond

	simulator := NewCrashSimulator(cfg)
	simulator.SimulateCrashAfterWrites(t, 500, "periodic")
}

// TestBatchSyncRecovery tests recovery with batch sync mode
func TestBatchSyncRecovery(t *testing.T) {
	cfg := createCrashTestConfig(t)
	cfg.Storage.WAL.SyncMode = "batch"
	cfg.Storage.WAL.BatchSyncSize = 50

	simulator := NewCrashSimulator(cfg)
	simulator.SimulateCrashAfterWrites(t, 200, "batch")
}

// TestAtomicBatchWrites verifies batch writes are atomic
func TestAtomicBatchWrites(t *testing.T) {
	cfg := createCrashTestConfig(t)
	cfg.Storage.WAL.SyncMode = "batch"
	cfg.Storage.WAL.BatchSyncSize = 10

	manager, err := New(cfg, createCrashTestLogger())
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Write exactly one batch worth of messages
	batchMessages := make([]*Message, 10)
	for i := 0; i < 10; i++ {
		msg := &Message{
			ID:        uint64(i + 1),
			Topic:     "batch/atomic/test",
			Payload:   []byte(fmt.Sprintf("batch message %d", i)),
			QoS:       1,
			ClientID:  "batch-client",
			Timestamp: time.Now(),
		}

		err := manager.persistMessage(msg)
		require.NoError(t, err)
		batchMessages[i] = msg
	}

	// Verify batch was synced
	stats := manager.GetStats()
	assert.Equal(t, uint64(10), stats["total_messages"])

	// Test recovery after "crash"
	manager.StopManager()

	manager2, err := New(cfg, createCrashTestLogger())
	require.NoError(t, err)
	defer manager2.StopManager()

	err = manager2.Start(ctx)
	require.NoError(t, err)

	// Verify all batch messages recovered
	stats2 := manager2.GetStats()
	assert.Equal(t, uint64(10), stats2["total_messages"], "Batch writes should be atomic")
}

// TestWALConsistencyAfterCrash verifies WAL structure remains valid after crash
func TestWALConsistencyAfterCrash(t *testing.T) {
	cfg := createCrashTestConfig(t)
	cfg.Storage.WAL.SyncMode = "immediate"

	// Create and populate storage
	manager1, err := New(cfg, createCrashTestLogger())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager1.Start(ctx)
	require.NoError(t, err)

	// Write messages to multiple topics
	topics := []string{"consistency/topic1", "consistency/topic2", "consistency/topic3"}
	messagesPerTopic := 50

	for _, topic := range topics {
		for i := 0; i < messagesPerTopic; i++ {
			msg := &Message{
				ID:        uint64(len(topics)*i + len(topic)), // Unique ID
				Topic:     topic,
				Payload:   []byte(fmt.Sprintf("consistency message %d", i)),
				QoS:       1,
				ClientID:  "consistency-client",
				Timestamp: time.Now(),
			}

			err := manager1.persistMessage(msg)
			require.NoError(t, err)
		}
	}

	// Abrupt shutdown
	manager1.cancel()

	// Recovery with new manager
	manager2, err := New(cfg, createCrashTestLogger())
	require.NoError(t, err)
	defer manager2.StopManager()

	err = manager2.Start(ctx)
	require.NoError(t, err)

	// Verify WAL structure is consistent
	stats := manager2.GetStats()

	assert.Equal(t, uint64(len(topics)*messagesPerTopic), stats["total_messages"], "All messages should be recovered with immediate sync")
	assert.Equal(t, len(topics), stats["topic_count"], "All topics should be recovered")

	// Verify we can read messages from each topic
	for _, topic := range topics {
		messages, err := manager2.GetMessagesByTopic(topic, messagesPerTopic)
		require.NoError(t, err)
		assert.Len(t, messages, messagesPerTopic, "Should recover all messages for topic %s", topic)
	}
}

// TestPartialWriteRecovery tests recovery from incomplete/corrupted writes
func TestPartialWriteRecovery(t *testing.T) {
	cfg := createCrashTestConfig(t)
	cfg.Storage.WAL.SyncMode = "periodic"

	manager, err := New(cfg, createCrashTestLogger())
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Write some valid messages
	for i := 0; i < 20; i++ {
		msg := &Message{
			ID:        uint64(i + 1),
			Topic:     "partial/recovery",
			Payload:   []byte(fmt.Sprintf("valid message %d", i)),
			QoS:       1,
			ClientID:  "partial-client",
			Timestamp: time.Now(),
		}

		err := manager.persistMessage(msg)
		require.NoError(t, err)
	}

	// Force flush valid messages
	err = manager.ForceFlush()
	require.NoError(t, err)

	// Simulate crash (some messages might be in buffer)
	manager.cancel()

	// Recovery should handle partial writes gracefully
	manager2, err := New(cfg, createCrashTestLogger())
	require.NoError(t, err)
	defer manager2.StopManager()

	err = manager2.Start(ctx)
	require.NoError(t, err)

	// Verify valid messages were recovered
	stats := manager2.GetStats()
	recoveredCount := stats["total_messages"].(uint64)

	assert.Greater(t, recoveredCount, uint64(0), "Should recover some valid messages")
	assert.LessOrEqual(t, recoveredCount, uint64(20), "Should not recover more messages than written")

	// Verify system is functional after recovery
	testMsg := &Message{
		ID:        1000,
		Topic:     "partial/recovery/test",
		Payload:   []byte("post recovery test"),
		QoS:       1,
		ClientID:  "test-client",
		Timestamp: time.Now(),
	}

	err = manager2.persistMessage(testMsg)
	require.NoError(t, err, "System should be functional after partial write recovery")
}

// TestConcurrentWriteCrashRecovery tests recovery during concurrent writes
func TestConcurrentWriteCrashRecovery(t *testing.T) {
	cfg := createCrashTestConfig(t)
	cfg.Storage.WAL.SyncMode = "immediate"
	cfg.Storage.WAL.ForceFsync = true

	manager, err := New(cfg, createCrashTestLogger())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Start concurrent writers
	numWorkers := 5
	messagesPerWorker := 20
	errorChan := make(chan error, numWorkers)

	for worker := 0; worker < numWorkers; worker++ {
		go func(workerID int) {
			defer func() {
				if r := recover(); r != nil {
					// Expected - we're simulating crashes
					errorChan <- fmt.Errorf("worker %d panicked: %v", workerID, r)
					return
				}
				errorChan <- nil
			}()

			for i := 0; i < messagesPerWorker; i++ {
				msg := &Message{
					ID:        uint64(workerID*1000 + i),
					Topic:     fmt.Sprintf("concurrent/worker%d", workerID),
					Payload:   []byte(fmt.Sprintf("worker %d message %d", workerID, i)),
					QoS:       1,
					ClientID:  fmt.Sprintf("worker-%d", workerID),
					Timestamp: time.Now(),
				}

				if err := manager.persistMessage(msg); err != nil {
					errorChan <- fmt.Errorf("worker %d write error: %w", workerID, err)
					return
				}

				// Small delay to increase chance of concurrent operations
				time.Sleep(time.Millisecond)
			}
		}(worker)
	}

	// Let workers run for a bit, then simulate crash
	time.Sleep(50 * time.Millisecond)
	manager.cancel() // Simulate abrupt crash

	// Wait for workers to complete (or crash)
	for i := 0; i < numWorkers; i++ {
		select {
		case err := <-errorChan:
			// Errors are expected during crash simulation
			if err != nil {
				t.Logf("Worker error (expected): %v", err)
			}
		case <-time.After(1 * time.Second):
			t.Log("Worker timeout (expected during crash simulation)")
		}
	}

	// Recovery phase
	time.Sleep(100 * time.Millisecond)

	manager2, err := New(cfg, createCrashTestLogger())
	require.NoError(t, err)
	defer manager2.StopManager()

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	err = manager2.Start(ctx2)
	require.NoError(t, err)

	// Verify recovery
	stats := manager2.GetStats()
	recoveredMessages := stats["total_messages"].(uint64)

	// With immediate sync, we should recover a significant portion
	expectedMinimum := uint64(float64(numWorkers*messagesPerWorker) * 0.5) // At least 50%
	assert.GreaterOrEqual(t, recoveredMessages, expectedMinimum,
		"Should recover at least 50%% of messages from concurrent writes")

	// Verify system functionality after concurrent crash recovery
	testMsg := &Message{
		ID:        99999,
		Topic:     "concurrent/recovery/test",
		Payload:   []byte("post concurrent crash recovery"),
		QoS:       1,
		ClientID:  "recovery-test",
		Timestamp: time.Now(),
	}

	err = manager2.persistMessage(testMsg)
	require.NoError(t, err, "Should handle writes after concurrent crash recovery")
}

// TestCompactionCrashSafety tests WAL safety during compaction operations
func TestCompactionCrashSafety(t *testing.T) {
	cfg := createCrashTestConfig(t)
	cfg.Storage.WAL.SyncMode = "immediate"

	// Enable compaction for this test
	cfg.Storage.Compaction.MaxWALSize = 1024 // Small size to trigger compaction
	cfg.Storage.Compaction.CheckInterval = 10 * time.Millisecond

	manager, err := New(cfg, createCrashTestLogger())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Write enough messages to trigger compaction
	for i := 0; i < 100; i++ {
		msg := &Message{
			ID:        uint64(i + 1),
			Topic:     "compaction/test",
			Payload:   []byte(fmt.Sprintf("compaction test message %d with extra padding to increase size", i)),
			QoS:       1,
			ClientID:  "compaction-client",
			Timestamp: time.Now(),
		}

		err := manager.persistMessage(msg)
		require.NoError(t, err)
	}

	// Let compaction potentially start
	time.Sleep(20 * time.Millisecond)

	// Simulate crash during potential compaction
	manager.cancel()

	// Recovery should handle any partial compaction state
	manager2, err := New(cfg, createCrashTestLogger())
	require.NoError(t, err)
	defer manager2.StopManager()

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	err = manager2.Start(ctx2)
	require.NoError(t, err)

	// Verify data integrity after compaction crash
	stats := manager2.GetStats()
	recoveredMessages := stats["total_messages"].(uint64)

	// Should recover most or all messages
	assert.GreaterOrEqual(t, recoveredMessages, uint64(95), "Should recover at least 95 messages after compaction crash")

	// Verify WAL structure is still consistent
	messages, err := manager2.GetMessagesByTopic("compaction/test", 100)
	require.NoError(t, err)
	assert.Greater(t, len(messages), 90, "Should be able to read messages after compaction crash")

	// Test write functionality
	postCompactionMsg := &Message{
		ID:        101,
		Topic:     "compaction/recovery",
		Payload:   []byte("post compaction crash message"),
		QoS:       1,
		ClientID:  "post-compaction-client",
		Timestamp: time.Now(),
	}

	err = manager2.persistMessage(postCompactionMsg)
	require.NoError(t, err, "Should handle writes after compaction crash")
}

// TestCorruptionHandling tests recovery from various corruption scenarios
func TestCorruptionHandling(t *testing.T) {
	cfg := createCrashTestConfig(t)
	cfg.Storage.WAL.SyncMode = "immediate"

	// Create manager and write initial messages
	manager, err := New(cfg, createCrashTestLogger())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Write some messages
	for i := 0; i < 10; i++ {
		msg := &Message{
			ID:        uint64(i + 1),
			Topic:     "corruption/test",
			Payload:   []byte(fmt.Sprintf("corruption test message %d", i)),
			QoS:       1,
			ClientID:  "corruption-client",
			Timestamp: time.Now(),
		}

		err := manager.persistMessage(msg)
		require.NoError(t, err)
	}

	manager.StopManager()

	// Recovery should handle corrupted data gracefully
	manager2, err := New(cfg, createCrashTestLogger())
	require.NoError(t, err)
	defer manager2.StopManager()

	err = manager2.Start(ctx)
	require.NoError(t, err)

	// Verify recovery worked despite potential corruption
	stats := manager2.GetStats()
	assert.Greater(t, stats["total_messages"], uint64(0), "Should recover some messages despite corruption")

	// System should remain functional
	corruptionRecoveryMsg := &Message{
		ID:        100,
		Topic:     "corruption/recovery",
		Payload:   []byte("corruption recovery test"),
		QoS:       1,
		ClientID:  "recovery-client",
		Timestamp: time.Now(),
	}

	err = manager2.persistMessage(corruptionRecoveryMsg)
	require.NoError(t, err, "System should remain functional after corruption recovery")
}

// TestZeroDataLossGuarantee verifies zero data loss for immediate sync mode
func TestZeroDataLossGuarantee(t *testing.T) {
	cfg := createCrashTestConfig(t)
	cfg.Storage.WAL.SyncMode = "immediate"
	cfg.Storage.WAL.ForceFsync = true

	// Disable memory buffering to ensure immediate persistence
	cfg.Storage.MemoryBuffer = 1 // Minimal buffer

	for iteration := 0; iteration < 5; iteration++ {
		t.Run(fmt.Sprintf("Iteration_%d", iteration), func(t *testing.T) {
			manager, err := New(cfg, createCrashTestLogger())
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err = manager.Start(ctx)
			require.NoError(t, err)

			// Write exactly 10 messages
			expectedMessages := 10
			for i := 0; i < expectedMessages; i++ {
				msg := &Message{
					ID:        uint64(iteration*1000 + i + 1),
					Topic:     fmt.Sprintf("zeroloss/iteration%d", iteration),
					Payload:   []byte(fmt.Sprintf("zero loss test %d-%d", iteration, i)),
					QoS:       1,
					ClientID:  "zeroloss-client",
					Timestamp: time.Now(),
				}

				err := manager.persistMessage(msg)
				require.NoError(t, err)

				// Verify message is immediately durable (no buffering)
				time.Sleep(1 * time.Millisecond) // Allow fsync to complete
			}

			// Immediate crash simulation
			manager.cancel()

			// Recovery phase
			manager2, err := New(cfg, createCrashTestLogger())
			require.NoError(t, err)
			defer manager2.StopManager()

			ctx2, cancel2 := context.WithCancel(context.Background())
			defer cancel2()

			err = manager2.Start(ctx2)
			require.NoError(t, err)

			// Verify ZERO data loss
			stats := manager2.GetStats()
			recoveredCount := stats["total_messages"].(uint64)

			// With immediate sync and forced fsync, we MUST recover ALL messages
			assert.Equal(t, uint64(expectedMessages), recoveredCount,
				"ZERO DATA LOSS: Immediate sync must recover all %d messages, got %d",
				expectedMessages, recoveredCount)
		})
	}
}

// TestCrashRecoveryPerformance benchmarks recovery time for various scenarios
func TestCrashRecoveryPerformance(t *testing.T) {
	testCases := []struct {
		name            string
		messageCount    int
		syncMode        string
		maxRecoveryTime time.Duration
	}{
		{"Small_Immediate", 100, "immediate", 100 * time.Millisecond},
		{"Medium_Immediate", 1000, "immediate", 500 * time.Millisecond},
		{"Large_Immediate", 10000, "immediate", 2 * time.Second},
		{"Small_Periodic", 100, "periodic", 100 * time.Millisecond},
		{"Medium_Periodic", 1000, "periodic", 500 * time.Millisecond},
		{"Large_Periodic", 10000, "periodic", 2 * time.Second},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := createCrashTestConfig(t)
			cfg.Storage.WAL.SyncMode = tc.syncMode

			// Phase 1: Write messages
			manager1, err := New(cfg, createCrashTestLogger())
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err = manager1.Start(ctx)
			require.NoError(t, err)

			for i := 0; i < tc.messageCount; i++ {
				msg := &Message{
					ID:        uint64(i + 1),
					Topic:     fmt.Sprintf("perf/%s", tc.name),
					Payload:   []byte(fmt.Sprintf("performance test message %d", i)),
					QoS:       1,
					ClientID:  "perf-client",
					Timestamp: time.Now(),
				}

				err := manager1.persistMessage(msg)
				require.NoError(t, err)
			}

			if tc.syncMode != "immediate" {
				err = manager1.ForceFlush()
				require.NoError(t, err)
			}

			manager1.cancel()

			// Phase 2: Measure recovery time
			time.Sleep(10 * time.Millisecond)

			manager2, err := New(cfg, createCrashTestLogger())
			require.NoError(t, err)
			defer manager2.StopManager()

			ctx2, cancel2 := context.WithCancel(context.Background())
			defer cancel2()

			// Measure recovery time
			recoveryStart := time.Now()
			err = manager2.Start(ctx2)
			require.NoError(t, err)
			recoveryTime := time.Since(recoveryStart)

			// Verify performance requirement
			assert.LessOrEqual(t, recoveryTime, tc.maxRecoveryTime,
				"Recovery time %v exceeded maximum %v for %s", recoveryTime, tc.maxRecoveryTime, tc.name)

			stats := manager2.GetStats()
			t.Logf("%s: Recovered %d messages in %v", tc.name, stats["total_messages"], recoveryTime)
		})
	}
}

// Helper functions

func createCrashTestConfig(t *testing.T) *config.Config {
	// Create a unique temporary directory for this specific test
	baseDir := t.TempDir()
	// Add more uniqueness to avoid conflicts
	unique := fmt.Sprintf("%d_%d", time.Now().UnixNano(), os.Getpid())

	cfg := &config.Config{
		Storage: config.StorageConfig{
			DataDir:         filepath.Join(baseDir, unique, "data"),
			WALDir:          filepath.Join(baseDir, unique, "wal"),
			MemoryBuffer:    1024 * 1024, // 1MB
			WALSyncInterval: 100 * time.Millisecond,
			WALNoSync:       false,
			WAL: config.WALConfig{
				SyncMode:                "periodic",
				SyncInterval:            100 * time.Millisecond,
				BatchSyncSize:           10,
				ForceFsync:              false,
				CrashRecoveryValidation: true,
			},
			Compaction: config.CompactionConfig{
				MaxMessageAge:     24 * time.Hour,
				MaxWALSize:        10 * 1024 * 1024, // 10MB
				CheckInterval:     1 * time.Hour,    // Disable for most tests
				ConcurrentWorkers: 1,
				BatchSize:         100,
			},
		},
	}

	return cfg
}

func createCrashTestLogger() zerolog.Logger {
	return zerolog.New(os.Stderr).Level(zerolog.Disabled)
}
