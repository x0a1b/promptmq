package storage

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/x0a1b/promptmq/internal/config"
)

func TestMain(m *testing.M) {
	// Run tests
	code := m.Run()
	os.Exit(code)
}

func createTestConfig(t *testing.T) *config.Config {
	tmpDir, err := os.MkdirTemp("", "promptmq-test-*")
	require.NoError(t, err)

	t.Cleanup(func() {
		os.RemoveAll(tmpDir)
	})

	return &config.Config{
		Storage: config.StorageConfig{
			DataDir:         filepath.Join(tmpDir, "data"),
			WALDir:          filepath.Join(tmpDir, "wal"),
			MemoryBuffer:    1024 * 1024, // 1MB
			WALSyncInterval: 100 * time.Millisecond,
			WALNoSync:       false,
		},
	}
}

func createTestConfigBench() *config.Config {
	tmpDir, _ := os.MkdirTemp("", "promptmq-bench-*")

	return &config.Config{
		Storage: config.StorageConfig{
			DataDir:         filepath.Join(tmpDir, "data"),
			WALDir:          filepath.Join(tmpDir, "wal"),
			MemoryBuffer:    1024 * 1024, // 1MB
			WALSyncInterval: 100 * time.Millisecond,
			WALNoSync:       false,
		},
	}
}

func createTestLogger() zerolog.Logger {
	return zerolog.New(os.Stderr).Level(zerolog.Disabled)
}

func TestNew(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, manager)

	// Check directories were created
	assert.DirExists(t, cfg.Storage.DataDir)
	assert.DirExists(t, cfg.Storage.WALDir)

	// Clean up
	require.NoError(t, manager.StopManager())
}

func TestManagerStartStop(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the manager
	err = manager.Start(ctx)
	require.NoError(t, err)

	// Stop the manager
	err = manager.StopManager()
	require.NoError(t, err)
}

func TestMemoryBuffer(t *testing.T) {
	buffer := NewMemoryBuffer(1024) // 1KB buffer

	// Test adding messages
	msg1 := &Message{
		ID:       1,
		Topic:    "test/topic",
		Payload:  []byte("small payload"),
		QoS:      1,
		Retain:   false,
		ClientID: "client1",
	}

	// Should succeed
	ok := buffer.Add(msg1)
	assert.True(t, ok)
	assert.Equal(t, 1, buffer.Count())
	assert.Greater(t, buffer.Size(), uint64(0))

	// Add a large message that should exceed buffer
	largeMsg := &Message{
		ID:       2,
		Topic:    "test/topic",
		Payload:  make([]byte, 2048), // 2KB payload
		QoS:      1,
		Retain:   false,
		ClientID: "client2",
	}

	// Should fail due to buffer size limit
	ok = buffer.Add(largeMsg)
	assert.False(t, ok)
	assert.Equal(t, 1, buffer.Count()) // Still 1 message

	// Test flush
	messages := buffer.Flush()
	assert.Len(t, messages, 1)
	assert.Equal(t, msg1.ID, messages[0].ID)
	assert.Equal(t, 0, buffer.Count())
	assert.Equal(t, uint64(0), buffer.Size())
}

func TestMessageSerialization(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	originalMsg := &Message{
		ID:        12345,
		Topic:     "test/topic/with/slashes",
		Payload:   []byte("Hello, World! This is a test payload with special characters: éñ中文"),
		QoS:       2,
		Retain:    true,
		ClientID:  "client-123",
		Timestamp: time.Now(),
	}

	// Serialize
	data, err := manager.serializeMessage(originalMsg)
	require.NoError(t, err)
	assert.Greater(t, len(data), 0)

	// Deserialize
	deserializedMsg, err := manager.deserializeMessage(data)
	require.NoError(t, err)

	// Compare
	assert.Equal(t, originalMsg.ID, deserializedMsg.ID)
	assert.Equal(t, originalMsg.Topic, deserializedMsg.Topic)
	assert.Equal(t, originalMsg.Payload, deserializedMsg.Payload)
	assert.Equal(t, originalMsg.QoS, deserializedMsg.QoS)
	assert.Equal(t, originalMsg.Retain, deserializedMsg.Retain)
	assert.Equal(t, originalMsg.ClientID, deserializedMsg.ClientID)
	// Timestamps might have slight differences due to nanosecond precision
	assert.WithinDuration(t, originalMsg.Timestamp, deserializedMsg.Timestamp, time.Microsecond)
}

func TestTopicNameSanitization(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"test/topic", "test_topic"},
		{"device:sensor+temp", "device_sensorplustemp"},
		{"path\\with\\backslashes", "path_with_backslashes"},
		{"topic#wildcard", "topichashwildcard"},
		{"special*chars?here", "special_chars_here"},
		{"unicode中文topic", "unicode中文topic"},
	}

	for _, tc := range testCases {
		result := sanitizeTopicName(tc.input)
		assert.Equal(t, tc.expected, result, "Failed for input: %s", tc.input)
	}
}

func TestPersistMessage(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	msg := &Message{
		ID:        1,
		Topic:     "test/persist",
		Payload:   []byte("test payload"),
		QoS:       1,
		Retain:    false,
		ClientID:  "client1",
		Timestamp: time.Now(),
	}

	// Persist message
	err = manager.persistMessage(msg)
	require.NoError(t, err)

	// Force flush to ensure message is written to WAL
	err = manager.ForceFlush()
	require.NoError(t, err)

	// Verify WAL file exists
	walFile := filepath.Join(cfg.Storage.WALDir, sanitizeTopicName(msg.Topic)+".wal")
	assert.FileExists(t, walFile)

	// Check statistics
	stats := manager.GetStats()
	assert.Equal(t, uint64(1), stats["total_messages"])
	assert.Greater(t, stats["total_bytes"], uint64(0))
	assert.Equal(t, 1, stats["topic_count"])
}

func TestMQTTHookInterface(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Test hook identification
	assert.Equal(t, "wal-storage", manager.ID())

	// Test Provides method
	assert.True(t, manager.Provides(mqtt.OnConnect))
	assert.True(t, manager.Provides(mqtt.OnPublish))
	assert.True(t, manager.Provides(mqtt.OnPublished))
	assert.False(t, manager.Provides(255)) // Invalid hook type

	// Test Init
	err = manager.Init(nil)
	assert.NoError(t, err)

	// Create mock client and packet
	client := &mqtt.Client{ID: "test-client"}

	publishPacket := packets.Packet{
		FixedHeader: packets.FixedHeader{
			Type:   packets.Publish,
			Qos:    1,
			Retain: false,
		},
		TopicName: "test/mqtt/hook",
		Payload:   []byte("test mqtt hook payload"),
	}

	// Test OnConnect
	err = manager.OnConnect(client, packets.Packet{})
	assert.NoError(t, err)

	// Test OnPublish
	_, err = manager.OnPublish(client, publishPacket)
	assert.NoError(t, err)

	// Verify message was persisted
	stats := manager.GetStats()
	assert.Equal(t, uint64(1), stats["total_messages"])

	// Test OnPublished (no error expected)
	manager.OnPublished(client, publishPacket)

	// Test OnDisconnect (no error expected)
	manager.OnDisconnect(client, nil, false)
}

func TestRecovery(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	// Create first manager and persist some messages
	manager1, err := New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager1.Start(ctx)
	require.NoError(t, err)

	// Persist multiple messages
	messages := []*Message{
		{ID: 1, Topic: "topic1", Payload: []byte("payload1"), ClientID: "client1", Timestamp: time.Now()},
		{ID: 2, Topic: "topic1", Payload: []byte("payload2"), ClientID: "client2", Timestamp: time.Now()},
		{ID: 3, Topic: "topic2", Payload: []byte("payload3"), ClientID: "client3", Timestamp: time.Now()},
	}

	for _, msg := range messages {
		err = manager1.persistMessage(msg)
		require.NoError(t, err)
	}

	// Force flush and stop
	err = manager1.ForceFlush()
	require.NoError(t, err)
	err = manager1.StopManager()
	require.NoError(t, err)

	// Create second manager with same config and test recovery
	manager2, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager2.Stop()

	err = manager2.Start(ctx)
	require.NoError(t, err)

	// Check that messages were recovered
	stats := manager2.GetStats()
	assert.Equal(t, uint64(3), stats["total_messages"])
	assert.Equal(t, 2, stats["topic_count"])
	assert.Equal(t, uint64(4), stats["next_message_id"]) // Should be max ID + 1
}

func TestConcurrentAccess(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Simulate concurrent writes from multiple goroutines
	const numGoroutines = 10
	const messagesPerGoroutine = 100

	var wg sync.WaitGroup
	errorChan := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < messagesPerGoroutine; j++ {
				msg := &Message{
					ID:        uint64(goroutineID*messagesPerGoroutine + j + 1),
					Topic:     "concurrent/test",
					Payload:   []byte("concurrent message"),
					QoS:       1,
					Retain:    false,
					ClientID:  "concurrent-client",
					Timestamp: time.Now(),
				}

				if err := manager.persistMessage(msg); err != nil {
					errorChan <- err
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errorChan)

	// Check for errors
	for err := range errorChan {
		t.Errorf("Concurrent write error: %v", err)
	}

	// Force flush to ensure all messages are persisted
	err = manager.ForceFlush()
	require.NoError(t, err)

	// Verify all messages were persisted
	stats := manager.GetStats()
	expectedMessages := uint64(numGoroutines * messagesPerGoroutine)
	assert.Equal(t, expectedMessages, stats["total_messages"])
}

func TestGetMessagesByTopic(t *testing.T) {
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	topic := "test/query"

	// Persist messages
	messages := []*Message{
		{ID: 1, Topic: topic, Payload: []byte("msg1"), ClientID: "client1", Timestamp: time.Now()},
		{ID: 2, Topic: topic, Payload: []byte("msg2"), ClientID: "client2", Timestamp: time.Now()},
		{ID: 3, Topic: topic, Payload: []byte("msg3"), ClientID: "client3", Timestamp: time.Now()},
	}

	for _, msg := range messages {
		err = manager.persistMessage(msg)
		require.NoError(t, err)
	}

	// Force flush
	err = manager.ForceFlush()
	require.NoError(t, err)

	// Query messages
	retrievedMessages, err := manager.GetMessagesByTopic(topic, 10)
	require.NoError(t, err)
	assert.Len(t, retrievedMessages, 3)

	// Verify message content
	for i, msg := range retrievedMessages {
		assert.Equal(t, messages[i].ID, msg.ID)
		assert.Equal(t, messages[i].Topic, msg.Topic)
		assert.Equal(t, messages[i].Payload, msg.Payload)
	}

	// Test limit
	limitedMessages, err := manager.GetMessagesByTopic(topic, 2)
	require.NoError(t, err)
	assert.Len(t, limitedMessages, 2)

	// Test non-existent topic
	emptyMessages, err := manager.GetMessagesByTopic("nonexistent", 10)
	require.NoError(t, err)
	assert.Len(t, emptyMessages, 0)
}

func TestWALSync(t *testing.T) {
	cfg := createTestConfig(t)
	cfg.Storage.WALSyncInterval = 50 * time.Millisecond // Fast sync for testing
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	// Persist a message
	msg := &Message{
		ID:        1,
		Topic:     "sync/test",
		Payload:   []byte("sync test payload"),
		QoS:       1,
		Retain:    false,
		ClientID:  "sync-client",
		Timestamp: time.Now(),
	}

	err = manager.persistMessage(msg)
	require.NoError(t, err)

	// Wait for sync to happen
	time.Sleep(100 * time.Millisecond)

	// Verify message is persisted
	stats := manager.GetStats()
	assert.Equal(t, uint64(1), stats["total_messages"])
}

func TestErrorHandling(t *testing.T) {
	// Test invalid serialization data
	cfg := createTestConfig(t)
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	// Test deserialization with invalid data
	invalidData := []byte{0x01, 0x02, 0x03} // Too short
	_, err = manager.deserializeMessage(invalidData)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "too short")

	// Test deserialization with corrupted length fields
	corruptedData := make([]byte, 100)
	// Fill first 16 bytes with valid data (ID + timestamp)
	binary.LittleEndian.PutUint64(corruptedData[0:8], 123)
	binary.LittleEndian.PutUint64(corruptedData[8:16], uint64(time.Now().UnixNano()))
	// Set invalid topic length at offset 16
	corruptedData[16] = 0xFF
	corruptedData[17] = 0xFF
	corruptedData[18] = 0xFF
	corruptedData[19] = 0xFF
	_, err = manager.deserializeMessage(corruptedData)
	assert.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "length exceeds data")
	}
}

func TestCompaction(t *testing.T) {
	cfg := createTestConfig(t)
	// Set aggressive compaction for testing
	cfg.Storage.Compaction.MaxMessageAge = 100 * time.Millisecond
	cfg.Storage.Compaction.MaxWALSize = 1024 // 1KB for easy testing
	cfg.Storage.Compaction.CheckInterval = 10 * time.Millisecond

	logger := createTestLogger()
	manager, err := New(cfg, logger)
	require.NoError(t, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(t, err)

	topic := "compact/test"

	// Add old messages that should be compacted
	oldTime := time.Now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		msg := &Message{
			ID:        uint64(i + 1),
			Topic:     topic,
			Payload:   []byte(fmt.Sprintf("old message %d", i)),
			ClientID:  "client",
			Timestamp: oldTime,
		}
		err = manager.persistMessage(msg)
		require.NoError(t, err)
	}

	// Add new messages that should be kept
	newTime := time.Now()
	for i := 0; i < 3; i++ {
		msg := &Message{
			ID:        uint64(i + 6),
			Topic:     topic,
			Payload:   []byte(fmt.Sprintf("new message %d", i)),
			ClientID:  "client",
			Timestamp: newTime,
		}
		err = manager.persistMessage(msg)
		require.NoError(t, err)
	}

	// Force flush to ensure messages are written to WAL
	err = manager.ForceFlush()
	require.NoError(t, err)

	// Wait for compaction to trigger (age-based)
	time.Sleep(200 * time.Millisecond)

	// Check compaction stats
	compactionStats := manager.compaction.GetCompactionStats()
	assert.Contains(t, compactionStats, "total_compactions")
	assert.Contains(t, compactionStats, "messages_removed")

	// Test compaction of non-existent topic (should still error with manual compaction)
	err = manager.CompactTopic("nonexistent", time.Now())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "topic WAL not found")
}

func BenchmarkMessageSerialization(b *testing.B) {
	cfg := createTestConfigBench()
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(b, err)
	defer manager.StopManager()

	msg := &Message{
		ID:        12345,
		Topic:     "benchmark/topic",
		Payload:   make([]byte, 1024), // 1KB payload
		QoS:       1,
		Retain:    false,
		ClientID:  "benchmark-client",
		Timestamp: time.Now(),
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			data, err := manager.serializeMessage(msg)
			if err != nil {
				b.Error(err)
			}
			_, err = manager.deserializeMessage(data)
			if err != nil {
				b.Error(err)
			}
		}
	})
}

func BenchmarkPersistMessage(b *testing.B) {
	cfg := createTestConfigBench()
	cfg.Storage.WALNoSync = true // Disable sync for benchmark
	logger := createTestLogger()

	manager, err := New(cfg, logger)
	require.NoError(b, err)
	defer manager.StopManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = manager.Start(ctx)
	require.NoError(b, err)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			msg := &Message{
				ID:        uint64(i + 1),
				Topic:     "benchmark/persist",
				Payload:   []byte("benchmark payload"),
				QoS:       1,
				Retain:    false,
				ClientID:  "benchmark-client",
				Timestamp: time.Now(),
			}

			err := manager.persistMessage(msg)
			if err != nil {
				b.Error(err)
			}
			i++
		}
	})
}

func BenchmarkCompactionOverhead(b *testing.B) {
	cfg := createTestConfigBench()
	cfg.Storage.WALNoSync = true

	// Baseline: No compaction
	cfg.Storage.Compaction.MaxMessageAge = 24 * time.Hour
	cfg.Storage.Compaction.MaxWALSize = 1024 * 1024 * 1024 // 1GB
	cfg.Storage.Compaction.CheckInterval = time.Hour

	logger := createTestLogger()

	b.Run("NoCompaction", func(b *testing.B) {
		manager, err := New(cfg, logger)
		require.NoError(b, err)
		defer manager.StopManager()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err = manager.Start(ctx)
		require.NoError(b, err)

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				msg := &Message{
					ID:        uint64(i + 1),
					Topic:     fmt.Sprintf("bench/topic/%d", i%10),
					Payload:   make([]byte, 100),
					QoS:       1,
					ClientID:  "bench-client",
					Timestamp: time.Now(),
				}
				err := manager.persistMessage(msg)
				if err != nil {
					b.Error(err)
				}
				i++
			}
		})
	})

	// With compaction enabled
	cfg.Storage.Compaction.MaxMessageAge = 10 * time.Millisecond
	cfg.Storage.Compaction.MaxWALSize = 10240 // 10KB
	cfg.Storage.Compaction.CheckInterval = 10 * time.Millisecond

	b.Run("WithCompaction", func(b *testing.B) {
		manager, err := New(cfg, logger)
		require.NoError(b, err)
		defer manager.StopManager()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err = manager.Start(ctx)
		require.NoError(b, err)

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				msg := &Message{
					ID:        uint64(i + 1),
					Topic:     fmt.Sprintf("bench/topic/%d", i%10),
					Payload:   make([]byte, 100),
					QoS:       1,
					ClientID:  "bench-client",
					Timestamp: time.Now(),
				}
				err := manager.persistMessage(msg)
				if err != nil {
					b.Error(err)
				}
				i++
			}
		})

		// Report compaction effectiveness
		stats := manager.compaction.GetCompactionStats()
		b.Logf("Compaction stats: %+v", stats)
	})
}
