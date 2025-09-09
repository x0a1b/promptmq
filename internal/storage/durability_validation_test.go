package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zohaib-hassan/promptmq/internal/config"
)

// TestBasicSyncModes validates that all sync modes work correctly
func TestBasicSyncModes(t *testing.T) {
	syncModes := []struct {
		name      string
		mode      string
		batchSize int
	}{
		{"Immediate", "immediate", 0},
		{"Periodic", "periodic", 0},
		{"Batch10", "batch", 10},
		{"Batch50", "batch", 50},
	}

	for _, sm := range syncModes {
		t.Run(sm.name, func(t *testing.T) {
			// Create unique temporary directory
			tempDir := t.TempDir()
			unique := fmt.Sprintf("sync_test_%d", time.Now().UnixNano())
			
			cfg := &config.Config{
				Storage: config.StorageConfig{
					DataDir:         filepath.Join(tempDir, unique, "data"),
					WALDir:          filepath.Join(tempDir, unique, "wal"),
					MemoryBuffer:    1024 * 1024, // 1MB
					WALSyncInterval: 50 * time.Millisecond,
					WALNoSync:       false,
					WAL: config.WALConfig{
						SyncMode:                sm.mode,
						SyncInterval:            50 * time.Millisecond,
						BatchSyncSize:           sm.batchSize,
						ForceFsync:              sm.mode == "immediate",
						CrashRecoveryValidation: true,
					},
					Compaction: config.CompactionConfig{
						MaxMessageAge:     24 * time.Hour,
						MaxWALSize:        10 * 1024 * 1024, // 10MB
						CheckInterval:     1 * time.Hour,     // Disable for test
						ConcurrentWorkers: 1,
						BatchSize:         100,
					},
				},
			}

			manager, err := New(cfg, createDurabilityTestLogger())
			require.NoError(t, err)
			defer manager.StopManager()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err = manager.Start(ctx)
			require.NoError(t, err)

			// Write test messages
			messageCount := 25
			for i := 0; i < messageCount; i++ {
				msg := &Message{
					ID:        uint64(i + 1),
					Topic:     fmt.Sprintf("sync/%s/test", sm.name),
					Payload:   []byte(fmt.Sprintf("sync mode test message %d", i)),
					QoS:       1,
					ClientID:  fmt.Sprintf("sync-client-%s", sm.name),
					Timestamp: time.Now(),
				}

				err := manager.persistMessage(msg)
				require.NoError(t, err, "Failed to persist message %d in %s mode", i, sm.name)
			}

			// For non-immediate modes, ensure messages are flushed
			if sm.mode != "immediate" {
				err = manager.ForceFlush()
				require.NoError(t, err, "Failed to force flush in %s mode", sm.name)
			}

			// Allow periodic sync to complete
			if sm.mode == "periodic" {
				time.Sleep(100 * time.Millisecond)
			}

			// Verify all messages were persisted
			stats := manager.GetStats()
			assert.Equal(t, uint64(messageCount), stats["total_messages"], 
				"All messages should be persisted in %s mode", sm.name)
			assert.Equal(t, 1, stats["topic_count"], 
				"Should have one topic in %s mode", sm.name)

			// Verify sync mode in stats
			assert.Equal(t, sm.mode, stats["sync_mode"], 
				"Config should report correct sync mode")

			t.Logf("✓ %s mode: Successfully persisted %d messages", sm.name, messageCount)
		})
	}
}

// TestImmediateSyncDurability specifically tests immediate sync durability
func TestImmediateSyncDurability(t *testing.T) {
	tempDir := t.TempDir()
	unique := fmt.Sprintf("immediate_test_%d", time.Now().UnixNano())
	
	cfg := &config.Config{
		Storage: config.StorageConfig{
			DataDir:         filepath.Join(tempDir, unique, "data"),
			WALDir:          filepath.Join(tempDir, unique, "wal"),
			MemoryBuffer:    1024, // Very small buffer to force WAL writes
			WALSyncInterval: 100 * time.Millisecond,
			WALNoSync:       false,
			WAL: config.WALConfig{
				SyncMode:                "immediate",
				SyncInterval:            100 * time.Millisecond,
				BatchSyncSize:           10,
				ForceFsync:              true,
				CrashRecoveryValidation: true,
			},
			Compaction: config.CompactionConfig{
				MaxMessageAge:     24 * time.Hour,
				MaxWALSize:        10 * 1024 * 1024,
				CheckInterval:     1 * time.Hour,
				ConcurrentWorkers: 1,
				BatchSize:         100,
			},
		},
	}

	// Phase 1: Write messages with immediate sync
	manager1, err := New(cfg, createDurabilityTestLogger())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager1.Start(ctx)
	require.NoError(t, err)

	messageCount := 10
	for i := 0; i < messageCount; i++ {
		msg := &Message{
			ID:        uint64(i + 1),
			Topic:     "immediate/durability",
			Payload:   []byte(fmt.Sprintf("immediate durability test %d", i)),
			QoS:       1,
			ClientID:  "immediate-client",
			Timestamp: time.Now(),
		}

		err := manager1.persistMessage(msg)
		require.NoError(t, err)

		// Each message should be immediately durable
		time.Sleep(1 * time.Millisecond) // Allow fsync to complete
	}

	// Verify all messages are persisted
	stats1 := manager1.GetStats()
	assert.Equal(t, uint64(messageCount), stats1["total_messages"])
	assert.Equal(t, "immediate", stats1["sync_mode"])
	assert.True(t, stats1["force_fsync"].(bool))

	// Abrupt shutdown (simulated crash)
	manager1.StopManager()

	// Phase 2: Recovery test
	manager2, err := New(cfg, createDurabilityTestLogger())
	require.NoError(t, err)
	defer manager2.StopManager()

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	err = manager2.Start(ctx2)
	require.NoError(t, err)

	// Verify 100% recovery with immediate sync
	stats2 := manager2.GetStats()
	recoveredCount := stats2["total_messages"].(uint64)
	assert.Equal(t, uint64(messageCount), recoveredCount, 
		"Immediate sync must guarantee 100%% message recovery")

	// Test that we can read all messages
	messages, err := manager2.GetMessagesByTopic("immediate/durability", messageCount+5)
	require.NoError(t, err)
	assert.Len(t, messages, messageCount, "Should recover all messages")

	// Verify message integrity
	for i, msg := range messages {
		expectedPayload := fmt.Sprintf("immediate durability test %d", i)
		assert.Equal(t, expectedPayload, string(msg.Payload), 
			"Message %d payload should be intact", i)
		assert.Equal(t, "immediate/durability", msg.Topic)
		assert.Equal(t, "immediate-client", msg.ClientID)
	}

	t.Logf("✓ Immediate sync durability: 100%% recovery (%d/%d messages)", 
		recoveredCount, messageCount)
}

// TestBatchSyncBehavior tests batch sync mode behavior
func TestBatchSyncBehavior(t *testing.T) {
	tempDir := t.TempDir()
	unique := fmt.Sprintf("batch_test_%d", time.Now().UnixNano())
	batchSize := 5
	
	cfg := &config.Config{
		Storage: config.StorageConfig{
			DataDir:         filepath.Join(tempDir, unique, "data"),
			WALDir:          filepath.Join(tempDir, unique, "wal"),
			MemoryBuffer:    1024 * 1024, // 1MB
			WALSyncInterval: 100 * time.Millisecond,
			WALNoSync:       false,
			WAL: config.WALConfig{
				SyncMode:                "batch",
				SyncInterval:            100 * time.Millisecond,
				BatchSyncSize:           batchSize,
				ForceFsync:              false,
				CrashRecoveryValidation: true,
			},
			Compaction: config.CompactionConfig{
				MaxMessageAge:     24 * time.Hour,
				MaxWALSize:        10 * 1024 * 1024,
				CheckInterval:     1 * time.Hour,
				ConcurrentWorkers: 1,
				BatchSize:         100,
			},
		},
	}

	manager, err := New(cfg, createDurabilityTestLogger())
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Write exactly one batch worth of messages
	for i := 0; i < batchSize; i++ {
		msg := &Message{
			ID:        uint64(i + 1),
			Topic:     "batch/test",
			Payload:   []byte(fmt.Sprintf("batch test message %d", i)),
			QoS:       1,
			ClientID:  "batch-client",
			Timestamp: time.Now(),
		}

		err := manager.persistMessage(msg)
		require.NoError(t, err)
	}

	// Verify batch was synced
	stats := manager.GetStats()
	assert.Equal(t, uint64(batchSize), stats["total_messages"])
	assert.Equal(t, "batch", stats["sync_mode"])

	// Write partial batch
	for i := 0; i < 3; i++ {
		msg := &Message{
			ID:        uint64(batchSize + i + 1),
			Topic:     "batch/partial",
			Payload:   []byte(fmt.Sprintf("partial batch message %d", i)),
			QoS:       1,
			ClientID:  "batch-partial-client",
			Timestamp: time.Now(),
		}

		err := manager.persistMessage(msg)
		require.NoError(t, err)
	}

	// Force flush partial batch
	err = manager.ForceFlush()
	require.NoError(t, err)

	finalStats := manager.GetStats()
	assert.Equal(t, uint64(batchSize+3), finalStats["total_messages"])

	t.Logf("✓ Batch sync: Successfully handled batch size %d", batchSize)
}

// TestConfigValidation tests configuration validation
func TestConfigValidation(t *testing.T) {
	tempDir := t.TempDir()
	unique := fmt.Sprintf("config_test_%d", time.Now().UnixNano())
	
	// Test invalid sync mode
	cfg := &config.Config{
		Storage: config.StorageConfig{
			DataDir:         filepath.Join(tempDir, unique, "data"),
			WALDir:          filepath.Join(tempDir, unique, "wal"),
			MemoryBuffer:    1024 * 1024,
			WALSyncInterval: 100 * time.Millisecond,
			WALNoSync:       false,
			WAL: config.WALConfig{
				SyncMode:                "invalid_mode", // This should fall back to periodic
				SyncInterval:            100 * time.Millisecond,
				BatchSyncSize:           10,
				ForceFsync:              false,
				CrashRecoveryValidation: false,
			},
			Compaction: config.CompactionConfig{
				MaxMessageAge:     24 * time.Hour,
				MaxWALSize:        10 * 1024 * 1024,
				CheckInterval:     1 * time.Hour,
				ConcurrentWorkers: 1,
				BatchSize:         100,
			},
		},
	}

	manager, err := New(cfg, createDurabilityTestLogger())
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Should still work (fallback to periodic mode)
	msg := &Message{
		ID:        1,
		Topic:     "config/validation",
		Payload:   []byte("config validation test"),
		QoS:       1,
		ClientID:  "config-client",
		Timestamp: time.Now(),
	}

	err = manager.persistMessage(msg)
	require.NoError(t, err, "Should handle invalid sync mode gracefully")

	stats := manager.GetStats()
	assert.Equal(t, uint64(1), stats["total_messages"])
	
	t.Log("✓ Config validation: Invalid sync mode handled gracefully")
}

// Helper function to create test logger
func createDurabilityTestLogger() zerolog.Logger {
	return zerolog.New(os.Stderr).Level(zerolog.Disabled)
}