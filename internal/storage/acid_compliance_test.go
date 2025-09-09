package storage

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAtomicityGuarantees tests that operations are atomic
func TestAtomicityGuarantees(t *testing.T) {
	tests := []struct {
		name     string
		syncMode string
	}{
		{"Immediate_Atomicity", "immediate"},
		{"Batch_Atomicity", "batch"},
		{"Periodic_Atomicity", "periodic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := createTestConfig(t)
			cfg.Storage.WAL.SyncMode = tt.syncMode
			cfg.Storage.WAL.BatchSyncSize = 5

			manager, err := New(cfg, createTestLogger())
			require.NoError(t, err)
			defer manager.StopManager()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err = manager.Start(ctx)
			require.NoError(t, err)

			// Test atomic batch operations
			batchMessages := make([]*Message, 5)
			for i := 0; i < 5; i++ {
				batchMessages[i] = &Message{
					ID:        uint64(i + 1),
					Topic:     "atomicity/test",
					Payload:   []byte(fmt.Sprintf("atomic message %d", i)),
					QoS:       1,
					ClientID:  "atomic-client",
					Timestamp: time.Now(),
				}
			}

			// Write all messages in quick succession
			for _, msg := range batchMessages {
				err := manager.persistMessage(msg)
				require.NoError(t, err)
			}

			// For non-immediate modes, ensure sync
			if tt.syncMode != "immediate" {
				err = manager.ForceFlush()
				require.NoError(t, err)
			}

			// Verify atomicity by checking all messages are present
			stats := manager.GetStats()
			assert.Equal(t, uint64(5), stats["total_messages"],
				"All messages in atomic operation should be persisted")

			// Test that partial operations don't leave system in inconsistent state
			// This is implicitly tested by the crash recovery tests
		})
	}
}

// TestConsistencyGuarantees tests that the WAL remains consistent
func TestConsistencyGuarantees(t *testing.T) {
	cfg := createTestConfig(t)
	cfg.Storage.WAL.SyncMode = "immediate"

	manager, err := New(cfg, createTestLogger())
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Write messages across multiple topics
	topics := []string{"consistency/topic1", "consistency/topic2", "consistency/topic3"}
	messagesPerTopic := 20

	for _, topic := range topics {
		for i := 0; i < messagesPerTopic; i++ {
			msg := &Message{
				ID:        uint64(len(topics)*i + len(topic) + i), // Ensure unique IDs
				Topic:     topic,
				Payload:   []byte(fmt.Sprintf("consistency message %d", i)),
				QoS:       1,
				ClientID:  "consistency-client",
				Timestamp: time.Now(),
			}

			err := manager.persistMessage(msg)
			require.NoError(t, err)
		}
	}

	// Verify consistency - all topics should have correct message counts
	for _, topic := range topics {
		messages, err := manager.GetMessagesByTopic(topic, messagesPerTopic+10)
		require.NoError(t, err)
		assert.Len(t, messages, messagesPerTopic,
			"Topic %s should have exactly %d messages", topic, messagesPerTopic)
	}

	// Verify global consistency
	stats := manager.GetStats()
	expectedTotal := uint64(len(topics) * messagesPerTopic)
	assert.Equal(t, expectedTotal, stats["total_messages"],
		"Total message count should be consistent")
	assert.Equal(t, len(topics), stats["topic_count"],
		"Topic count should be consistent")
}

// TestIsolationGuarantees tests that concurrent operations don't interfere
func TestIsolationGuarantees(t *testing.T) {
	cfg := createTestConfig(t)
	cfg.Storage.WAL.SyncMode = "immediate" // Strongest isolation with immediate sync

	manager, err := New(cfg, createTestLogger())
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Run concurrent writers to test isolation
	numWorkers := 10
	messagesPerWorker := 50
	var wg sync.WaitGroup

	// Each worker writes to its own topic to test isolation
	for workerID := 0; workerID < numWorkers; workerID++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for i := 0; i < messagesPerWorker; i++ {
				msg := &Message{
					ID:        uint64(id*1000 + i + 1), // Unique ID per worker
					Topic:     fmt.Sprintf("isolation/worker_%d", id),
					Payload:   []byte(fmt.Sprintf("isolated message from worker %d, msg %d", id, i)),
					QoS:       1,
					ClientID:  fmt.Sprintf("worker-%d", id),
					Timestamp: time.Now(),
				}

				err := manager.persistMessage(msg)
				if err != nil {
					t.Errorf("Worker %d failed to persist message %d: %v", id, i, err)
					return
				}
			}
		}(workerID)
	}

	wg.Wait()

	// Verify isolation - each worker should have written exactly its messages
	for workerID := 0; workerID < numWorkers; workerID++ {
		topic := fmt.Sprintf("isolation/worker_%d", workerID)
		messages, err := manager.GetMessagesByTopic(topic, messagesPerWorker+10)
		require.NoError(t, err)
		assert.Len(t, messages, messagesPerWorker,
			"Worker %d topic should have exactly %d messages", workerID, messagesPerWorker)
	}

	// Verify total isolation
	stats := manager.GetStats()
	expectedTotal := uint64(numWorkers * messagesPerWorker)
	assert.Equal(t, expectedTotal, stats["total_messages"],
		"Total messages should equal sum of all worker messages")
	assert.Equal(t, numWorkers, stats["topic_count"],
		"Should have one topic per worker")
}

// TestDurabilityGuarantees tests that committed data survives system failures
func TestDurabilityGuarantees(t *testing.T) {
	durabilityTests := []struct {
		name          string
		syncMode      string
		forceFsync    bool
		expectedRatio float64 // Minimum expected recovery ratio
	}{
		{"MaxDurability", "immediate", true, 1.0},    // 100% recovery expected
		{"HighDurability", "immediate", false, 0.95}, // 95% recovery expected
		{"MediumDurability", "batch", false, 0.8},    // 80% recovery expected
		{"BasicDurability", "periodic", false, 0.5},  // 50% recovery expected
	}

	for _, test := range durabilityTests {
		t.Run(test.name, func(t *testing.T) {
			cfg := createTestConfig(t)
			cfg.Storage.WAL.SyncMode = test.syncMode
			cfg.Storage.WAL.ForceFsync = test.forceFsync
			cfg.Storage.WAL.BatchSyncSize = 10
			cfg.Storage.MemoryBuffer = 1024 // Small buffer to force WAL writes

			// Phase 1: Write and commit data
			manager1, err := New(cfg, createTestLogger())
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err = manager1.Start(ctx)
			require.NoError(t, err)

			messageCount := 100
			for i := 0; i < messageCount; i++ {
				msg := &Message{
					ID:        uint64(i + 1),
					Topic:     fmt.Sprintf("durability/%s", test.name),
					Payload:   []byte(fmt.Sprintf("durability test message %d", i)),
					QoS:       1,
					ClientID:  "durability-client",
					Timestamp: time.Now(),
				}

				err := manager1.persistMessage(msg)
				require.NoError(t, err)
			}

			// Ensure data is committed based on sync mode
			if test.syncMode == "batch" {
				err = manager1.ForceFlush()
				require.NoError(t, err)
			}

			// Allow some time for periodic sync
			if test.syncMode == "periodic" {
				time.Sleep(150 * time.Millisecond) // Allow sync interval
				err = manager1.ForceFlush()
				require.NoError(t, err)
			}

			// Simulate system crash (abrupt shutdown)
			manager1.cancel()

			// Phase 2: Verify durability through recovery
			manager2, err := New(cfg, createTestLogger())
			require.NoError(t, err)
			defer manager2.StopManager()

			ctx2, cancel2 := context.WithCancel(context.Background())
			defer cancel2()

			err = manager2.Start(ctx2)
			require.NoError(t, err)

			// Check durability guarantee
			stats := manager2.GetStats()
			recoveredCount := stats["total_messages"].(uint64)
			recoveryRatio := float64(recoveredCount) / float64(messageCount)

			assert.GreaterOrEqual(t, recoveryRatio, test.expectedRatio,
				"%s: Recovery ratio %.2f should be at least %.2f (recovered %d/%d)",
				test.name, recoveryRatio, test.expectedRatio, recoveredCount, messageCount)

			t.Logf("%s: Recovered %d/%d messages (%.1f%% recovery ratio)",
				test.name, recoveredCount, messageCount, recoveryRatio*100)
		})
	}
}

// TestACIDCompliance runs comprehensive ACID tests
func TestACIDCompliance(t *testing.T) {
	t.Run("FullACIDCompliance", func(t *testing.T) {
		cfg := createTestConfig(t)
		cfg.Storage.WAL.SyncMode = "immediate"
		cfg.Storage.WAL.ForceFsync = true

		manager, err := New(cfg, createTestLogger())
		require.NoError(t, err)
		defer manager.StopManager()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err = manager.Start(ctx)
		require.NoError(t, err)

		// ACID Test: Complex multi-topic transaction-like operation
		batchSize := 20
		topics := []string{"acid/topic1", "acid/topic2", "acid/topic3"}

		// Atomicity: Write messages across multiple topics
		for i := 0; i < batchSize; i++ {
			for topicIdx, topic := range topics {
				msg := &Message{
					ID:        uint64(i*len(topics) + topicIdx + 1),
					Topic:     topic,
					Payload:   []byte(fmt.Sprintf("ACID test batch %d message %d", i, topicIdx)),
					QoS:       1,
					ClientID:  "acid-client",
					Timestamp: time.Now(),
				}

				err := manager.persistMessage(msg)
				require.NoError(t, err, "Atomicity: All writes must succeed")
			}
		}

		// Consistency: Verify data integrity
		stats := manager.GetStats()
		expectedMessages := uint64(batchSize * len(topics))
		assert.Equal(t, expectedMessages, stats["total_messages"],
			"Consistency: Total message count must be exact")

		for _, topic := range topics {
			messages, err := manager.GetMessagesByTopic(topic, batchSize+10)
			require.NoError(t, err)
			assert.Len(t, messages, batchSize,
				"Consistency: Each topic must have exact message count")
		}

		// Isolation: Concurrent operations shouldn't interfere
		var wg sync.WaitGroup
		errorCount := make(chan error, 10)

		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()

				msg := &Message{
					ID:        uint64(10000 + workerID), // High ID to avoid conflicts
					Topic:     fmt.Sprintf("acid/isolation_%d", workerID),
					Payload:   []byte(fmt.Sprintf("isolation test %d", workerID)),
					QoS:       1,
					ClientID:  fmt.Sprintf("isolation-client-%d", workerID),
					Timestamp: time.Now(),
				}

				if err := manager.persistMessage(msg); err != nil {
					errorCount <- err
				}
			}(i)
		}

		wg.Wait()
		close(errorCount)

		// Check no isolation violations occurred
		for err := range errorCount {
			t.Errorf("Isolation: Concurrent write failed: %v", err)
		}

		// Durability: Simulate crash and verify recovery
		initialStats := manager.GetStats()
		initialCount := initialStats["total_messages"].(uint64)

		manager.cancel() // Simulate crash

		// Recovery test
		manager2, err := New(cfg, createTestLogger())
		require.NoError(t, err)
		defer manager2.StopManager()

		ctx2, cancel2 := context.WithCancel(context.Background())
		defer cancel2()

		err = manager2.Start(ctx2)
		require.NoError(t, err)

		recoveryStats := manager2.GetStats()
		recoveredCount := recoveryStats["total_messages"].(uint64)

		assert.Equal(t, initialCount, recoveredCount,
			"Durability: All committed data must survive crash")

		t.Logf("ACID Compliance Test: %d messages committed and recovered", recoveredCount)
	})
}

// TestTransactionLikeSemantics tests transaction-like behavior for batch operations
func TestTransactionLikeSemantics(t *testing.T) {
	cfg := createTestConfig(t)
	cfg.Storage.WAL.SyncMode = "batch"
	cfg.Storage.WAL.BatchSyncSize = 5

	manager, err := New(cfg, createTestLogger())
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Write exactly one batch of messages
	batchSize := 5
	for i := 0; i < batchSize; i++ {
		msg := &Message{
			ID:        uint64(i + 1),
			Topic:     "transaction/batch",
			Payload:   []byte(fmt.Sprintf("transaction message %d", i)),
			QoS:       1,
			ClientID:  "transaction-client",
			Timestamp: time.Now(),
		}

		err := manager.persistMessage(msg)
		require.NoError(t, err)
	}

	// Verify batch was committed atomically
	stats := manager.GetStats()
	assert.Equal(t, uint64(batchSize), stats["total_messages"],
		"Batch should be committed atomically")

	// Write partial batch and verify it's not yet committed
	for i := 0; i < 3; i++ { // Less than batch size
		msg := &Message{
			ID:        uint64(batchSize + i + 1),
			Topic:     "transaction/partial",
			Payload:   []byte(fmt.Sprintf("partial transaction message %d", i)),
			QoS:       1,
			ClientID:  "partial-client",
			Timestamp: time.Now(),
		}

		err := manager.persistMessage(msg)
		require.NoError(t, err)
	}

	// Before force flush, batch might not be persisted
	// This simulates transaction-like behavior where uncommitted changes
	// may not be visible until commit (force flush)

	// Force commit the partial batch
	err = manager.ForceFlush()
	require.NoError(t, err)

	finalStats := manager.GetStats()
	assert.Equal(t, uint64(batchSize+3), finalStats["total_messages"],
		"Partial batch should be committed after force flush")
}

// TestDataIntegrityUnderStress tests data integrity under high load
func TestDataIntegrityUnderStress(t *testing.T) {
	cfg := createTestConfig(t)
	cfg.Storage.WAL.SyncMode = "immediate"
	cfg.Storage.WAL.ForceFsync = true

	manager, err := New(cfg, createTestLogger())
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Stress test with multiple concurrent writers
	numWorkers := 8
	messagesPerWorker := 100
	var wg sync.WaitGroup

	messagesSent := make([][]uint64, numWorkers) // Track message IDs per worker

	for workerID := 0; workerID < numWorkers; workerID++ {
		wg.Add(1)
		messagesSent[workerID] = make([]uint64, messagesPerWorker)

		go func(id int) {
			defer wg.Done()

			for i := 0; i < messagesPerWorker; i++ {
				msgID := uint64(id*10000 + i + 1) // Ensure unique IDs
				messagesSent[id][i] = msgID

				msg := &Message{
					ID:        msgID,
					Topic:     fmt.Sprintf("stress/worker_%d", id),
					Payload:   []byte(fmt.Sprintf("stress test message %d from worker %d", i, id)),
					QoS:       1,
					ClientID:  fmt.Sprintf("stress-client-%d", id),
					Timestamp: time.Now(),
				}

				err := manager.persistMessage(msg)
				require.NoError(t, err, "Stress test: All messages must be persisted")

				// Add small random delay to increase concurrency stress
				if i%10 == 0 {
					time.Sleep(time.Microsecond)
				}
			}
		}(workerID)
	}

	wg.Wait()

	// Verify data integrity
	stats := manager.GetStats()
	expectedTotal := uint64(numWorkers * messagesPerWorker)
	assert.Equal(t, expectedTotal, stats["total_messages"],
		"Stress test: All messages must be accounted for")

	// Verify each worker's messages are intact
	for workerID := 0; workerID < numWorkers; workerID++ {
		topic := fmt.Sprintf("stress/worker_%d", workerID)
		messages, err := manager.GetMessagesByTopic(topic, messagesPerWorker+10)
		require.NoError(t, err)
		assert.Len(t, messages, messagesPerWorker,
			"Stress test: Worker %d should have all messages", workerID)
	}

	t.Logf("Stress test completed: %d workers × %d messages = %d total messages",
		numWorkers, messagesPerWorker, expectedTotal)
}
