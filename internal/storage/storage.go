package storage

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/badger/v4"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/rs/zerolog"

	"github.com/zohaib-hassan/promptmq/internal/config"
)

// Message represents a persisted MQTT message
type Message struct {
	ID        uint64    `json:"id"`
	Topic     string    `json:"topic"`
	Payload   []byte    `json:"payload"`
	QoS       byte      `json:"qos"`
	Retain    bool      `json:"retain"`
	ClientID  string    `json:"client_id"`
	Timestamp time.Time `json:"timestamp"`
}

// TopicWAL manages WAL for a specific topic
type TopicWAL struct {
	topic      string
	wal        *WAL
	lastOffset atomic.Uint64
	memBuffer  *MemoryBuffer
	mu         sync.RWMutex
}

// MemoryBuffer holds messages in memory before they overflow to disk
type MemoryBuffer struct {
	messages []*Message
	size     atomic.Uint64
	maxSize  uint64
	mu       sync.RWMutex
}

// StorageMetrics defines the interface for storage metrics recording
type StorageMetrics interface {
	RecordWALWrite(latency time.Duration, success bool)
	RecordWALBufferUsage(bytes int64)
	RecordWALRecovery(duration time.Duration)
}

// NoOpMetrics is a no-op implementation for when metrics are disabled
type NoOpMetrics struct{}

func (n *NoOpMetrics) RecordWALWrite(time.Duration, bool) {}
func (n *NoOpMetrics) RecordWALBufferUsage(int64)         {}
func (n *NoOpMetrics) RecordWALRecovery(time.Duration)    {}

// Manager implements enterprise-grade WAL persistence for MQTT messages
type Manager struct {
	cfg           *config.Config
	logger        zerolog.Logger
	db            *badger.DB
	topicWALs     sync.Map // map[string]*TopicWAL
	memBuffer     *MemoryBuffer
	syncTicker    *time.Ticker
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	messageID     atomic.Uint64
	totalMessages atomic.Uint64
	totalBytes    atomic.Uint64
	metrics       StorageMetrics // For WAL performance metrics
}

// NewMemoryBuffer creates a new memory buffer
func NewMemoryBuffer(maxSize uint64) *MemoryBuffer {
	return &MemoryBuffer{
		messages: make([]*Message, 0),
		maxSize:  maxSize,
	}
}

// Add adds a message to the memory buffer
func (mb *MemoryBuffer) Add(msg *Message) bool {
	mb.mu.Lock()
	defer mb.mu.Unlock()

	msgSize := uint64(len(msg.Payload) + len(msg.Topic) + len(msg.ClientID) + 64) // approximate overhead
	if mb.size.Load()+msgSize > mb.maxSize {
		return false // buffer full
	}

	mb.messages = append(mb.messages, msg)
	mb.size.Add(msgSize)
	return true
}

// Flush returns all messages and clears the buffer
func (mb *MemoryBuffer) Flush() []*Message {
	mb.mu.Lock()
	defer mb.mu.Unlock()

	messages := make([]*Message, len(mb.messages))
	copy(messages, mb.messages)

	mb.messages = mb.messages[:0]
	mb.size.Store(0)

	return messages
}

// Size returns the current buffer size
func (mb *MemoryBuffer) Size() uint64 {
	return mb.size.Load()
}

// Count returns the number of messages in the buffer
func (mb *MemoryBuffer) Count() int {
	mb.mu.RLock()
	defer mb.mu.RUnlock()
	return len(mb.messages)
}

// New creates a new storage manager
func New(cfg *config.Config, logger zerolog.Logger) (*Manager, error) {
	return NewWithMetrics(cfg, logger, &NoOpMetrics{})
}

// NewWithMetrics creates a new storage manager with metrics integration
func NewWithMetrics(cfg *config.Config, logger zerolog.Logger, metrics StorageMetrics) (*Manager, error) {
	// Create data and WAL directories
	if err := os.MkdirAll(cfg.Storage.DataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}
	if err := os.MkdirAll(cfg.Storage.WALDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create WAL directory: %w", err)
	}

	// Open BadgerDB for metadata and indexing
	opts := badger.DefaultOptions(cfg.Storage.DataDir)
	opts.Logger = &badgerLogger{logger: logger}

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open BadgerDB: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	m := &Manager{
		cfg:       cfg,
		logger:    logger.With().Str("component", "storage").Logger(),
		db:        db,
		memBuffer: NewMemoryBuffer(cfg.Storage.MemoryBuffer),
		ctx:       ctx,
		cancel:    cancel,
		metrics:   metrics,
	}

	return m, nil
}

// Start initializes the storage manager and starts background processes
func (m *Manager) Start(ctx context.Context) error {
	m.logger.Info().
		Str("data_dir", m.cfg.Storage.DataDir).
		Str("wal_dir", m.cfg.Storage.WALDir).
		Uint64("memory_buffer", m.cfg.Storage.MemoryBuffer).
		Dur("sync_interval", m.cfg.Storage.WALSyncInterval).
		Msg("Starting storage manager")

	// Recover from existing WAL files
	if err := m.recover(); err != nil {
		return fmt.Errorf("failed to recover from WAL: %w", err)
	}

	// Start sync ticker if sync is enabled
	if !m.cfg.Storage.WALNoSync {
		m.syncTicker = time.NewTicker(m.cfg.Storage.WALSyncInterval)
		m.wg.Add(1)
		go m.syncLoop()
	}

	// Start memory buffer flush routine
	m.wg.Add(1)
	go m.bufferFlushLoop()

	// Start metrics reporting loop
	m.wg.Add(1)
	go m.metricsLoop()

	return nil
}

// StopManager gracefully shuts down the storage manager
func (m *Manager) StopManager() error {
	m.logger.Info().Msg("Stopping storage manager")

	// Cancel context to stop background routines
	m.cancel()

	// Stop sync ticker
	if m.syncTicker != nil {
		m.syncTicker.Stop()
	}

	// Wait for background goroutines to finish
	m.wg.Wait()

	// Flush remaining messages in memory buffer
	if err := m.flushMemoryBuffer(); err != nil {
		m.logger.Error().Err(err).Msg("Failed to flush memory buffer during shutdown")
	}

	// Close all topic WALs
	m.topicWALs.Range(func(key, value interface{}) bool {
		topicWAL := value.(*TopicWAL)
		if err := topicWAL.wal.Close(); err != nil {
			m.logger.Error().Err(err).Str("topic", key.(string)).Msg("Failed to close topic WAL")
		}
		return true
	})

	// Close BadgerDB
	if err := m.db.Close(); err != nil {
		m.logger.Error().Err(err).Msg("Failed to close BadgerDB")
		return err
	}

	m.logger.Info().
		Uint64("total_messages", m.totalMessages.Load()).
		Uint64("total_bytes", m.totalBytes.Load()).
		Msg("Storage manager stopped")

	return nil
}

// getOrCreateTopicWAL gets or creates a WAL for a specific topic
func (m *Manager) getOrCreateTopicWAL(topic string) (*TopicWAL, error) {
	if topicWAL, exists := m.topicWALs.Load(topic); exists {
		return topicWAL.(*TopicWAL), nil
	}

	// Create new WAL for this topic
	walPath := filepath.Join(m.cfg.Storage.WALDir, sanitizeTopicName(topic)+".wal")

	walInstance, err := NewWAL(walPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create WAL for topic %s: %w", topic, err)
	}

	topicWAL := &TopicWAL{
		topic:     topic,
		wal:       walInstance,
		memBuffer: NewMemoryBuffer(m.cfg.Storage.MemoryBuffer / 10), // 10% per topic
	}

	// Store in map
	actual, loaded := m.topicWALs.LoadOrStore(topic, topicWAL)
	if loaded {
		// Another goroutine created it first, close our WAL and use theirs
		walInstance.Close()
		return actual.(*TopicWAL), nil
	}

	m.logger.Debug().Str("topic", topic).Str("wal_path", walPath).Msg("Created new topic WAL")
	return topicWAL, nil
}

// persistMessage persists a message to the appropriate topic WAL
func (m *Manager) persistMessage(msg *Message) error {
	// Always increment totalMessages for any persisted message
	m.totalMessages.Add(1)

	// Try to add to memory buffer first
	if m.memBuffer.Add(msg) {
		// Message added to memory buffer
		return nil
	}

	// Memory buffer is full, write directly to WAL
	return m.writeMessageToWAL(msg)
}

// writeMessageToWAL writes a message directly to the WAL
func (m *Manager) writeMessageToWAL(msg *Message) error {
	topicWAL, err := m.getOrCreateTopicWAL(msg.Topic)
	if err != nil {
		return err
	}

	// Serialize message
	data, err := m.serializeMessage(msg)
	if err != nil {
		return fmt.Errorf("failed to serialize message: %w", err)
	}

	// Write to WAL with metrics timing
	startTime := time.Now()
	topicWAL.mu.Lock()
	offset, err := topicWAL.wal.Write(data)
	writeLatency := time.Since(startTime)
	if err != nil {
		topicWAL.mu.Unlock()
		m.metrics.RecordWALWrite(writeLatency, false) // Record failed write
		return fmt.Errorf("failed to write to WAL: %w", err)
	}
	topicWAL.lastOffset.Store(offset)
	topicWAL.mu.Unlock()

	// Record successful write metrics
	m.metrics.RecordWALWrite(writeLatency, true)

	// Update metadata in BadgerDB
	if err := m.updateMessageMetadata(msg, offset); err != nil {
		m.logger.Error().Err(err).Msg("Failed to update message metadata")
		// Don't return error as message is already in WAL
	}

	// Update byte statistics (message count updated in OnPublish)
	m.totalBytes.Add(uint64(len(data)))

	return nil
}

// serializeMessage converts a message to bytes for WAL storage
func (m *Manager) serializeMessage(msg *Message) ([]byte, error) {
	// Simple binary format: [id(8)][timestamp(8)][topic_len(4)][topic][payload_len(4)][payload][qos(1)][retain(1)][client_id_len(4)][client_id]
	topicBytes := []byte(msg.Topic)
	clientIDBytes := []byte(msg.ClientID)

	size := 8 + 8 + 4 + len(topicBytes) + 4 + len(msg.Payload) + 1 + 1 + 4 + len(clientIDBytes)
	buf := make([]byte, size)

	offset := 0

	// ID
	binary.LittleEndian.PutUint64(buf[offset:], msg.ID)
	offset += 8

	// Timestamp
	binary.LittleEndian.PutUint64(buf[offset:], uint64(msg.Timestamp.UnixNano()))
	offset += 8

	// Topic
	binary.LittleEndian.PutUint32(buf[offset:], uint32(len(topicBytes)))
	offset += 4
	copy(buf[offset:], topicBytes)
	offset += len(topicBytes)

	// Payload
	binary.LittleEndian.PutUint32(buf[offset:], uint32(len(msg.Payload)))
	offset += 4
	copy(buf[offset:], msg.Payload)
	offset += len(msg.Payload)

	// QoS
	buf[offset] = msg.QoS
	offset++

	// Retain
	if msg.Retain {
		buf[offset] = 1
	} else {
		buf[offset] = 0
	}
	offset++

	// Client ID
	binary.LittleEndian.PutUint32(buf[offset:], uint32(len(clientIDBytes)))
	offset += 4
	copy(buf[offset:], clientIDBytes)

	return buf, nil
}

// deserializeMessage converts bytes from WAL back to a message
func (m *Manager) deserializeMessage(data []byte) (*Message, error) {
	if len(data) < 26 { // minimum size
		return nil, fmt.Errorf("invalid message data: too short")
	}

	offset := 0
	msg := &Message{}

	// ID
	msg.ID = binary.LittleEndian.Uint64(data[offset:])
	offset += 8

	// Timestamp
	msg.Timestamp = time.Unix(0, int64(binary.LittleEndian.Uint64(data[offset:])))
	offset += 8

	// Topic
	topicLen := binary.LittleEndian.Uint32(data[offset:])
	offset += 4
	if offset+int(topicLen) > len(data) {
		return nil, fmt.Errorf("invalid message data: topic length exceeds data")
	}
	msg.Topic = string(data[offset : offset+int(topicLen)])
	offset += int(topicLen)

	// Payload
	if offset+4 > len(data) {
		return nil, fmt.Errorf("invalid message data: no payload length")
	}
	payloadLen := binary.LittleEndian.Uint32(data[offset:])
	offset += 4
	if offset+int(payloadLen) > len(data) {
		return nil, fmt.Errorf("invalid message data: payload length exceeds data")
	}
	msg.Payload = make([]byte, payloadLen)
	copy(msg.Payload, data[offset:offset+int(payloadLen)])
	offset += int(payloadLen)

	// QoS
	if offset >= len(data) {
		return nil, fmt.Errorf("invalid message data: no QoS")
	}
	msg.QoS = data[offset]
	offset++

	// Retain
	if offset >= len(data) {
		return nil, fmt.Errorf("invalid message data: no retain flag")
	}
	msg.Retain = data[offset] == 1
	offset++

	// Client ID
	if offset+4 > len(data) {
		return nil, fmt.Errorf("invalid message data: no client ID length")
	}
	clientIDLen := binary.LittleEndian.Uint32(data[offset:])
	offset += 4
	if offset+int(clientIDLen) > len(data) {
		return nil, fmt.Errorf("invalid message data: client ID length exceeds data")
	}
	msg.ClientID = string(data[offset : offset+int(clientIDLen)])

	return msg, nil
}

// updateMessageMetadata stores message metadata in BadgerDB for indexing
func (m *Manager) updateMessageMetadata(msg *Message, walOffset uint64) error {
	return m.db.Update(func(txn *badger.Txn) error {
		key := fmt.Sprintf("msg:%d", msg.ID)
		value := fmt.Sprintf("%s:%d", msg.Topic, walOffset)
		return txn.Set([]byte(key), []byte(value))
	})
}

// syncLoop periodically syncs WAL files
func (m *Manager) syncLoop() {
	defer m.wg.Done()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.syncTicker.C:
			m.syncAllWALs()
		}
	}
}

// syncAllWALs syncs all topic WAL files
func (m *Manager) syncAllWALs() {
	m.topicWALs.Range(func(key, value interface{}) bool {
		topicWAL := value.(*TopicWAL)
		topicWAL.mu.RLock()
		err := topicWAL.wal.Sync()
		topicWAL.mu.RUnlock()

		if err != nil {
			m.logger.Error().Err(err).Str("topic", key.(string)).Msg("Failed to sync WAL")
		}
		return true
	})
}

// bufferFlushLoop periodically flushes the memory buffer
func (m *Manager) bufferFlushLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(time.Second) // Flush every second
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			if err := m.flushMemoryBuffer(); err != nil {
				m.logger.Error().Err(err).Msg("Failed to flush memory buffer")
			}
		}
	}
}

// flushMemoryBuffer flushes all messages from memory buffer to WAL
func (m *Manager) flushMemoryBuffer() error {
	messages := m.memBuffer.Flush()
	if len(messages) == 0 {
		return nil
	}

	m.logger.Debug().Int("message_count", len(messages)).Msg("Flushing memory buffer to WAL")

	for _, msg := range messages {
		if err := m.writeMessageToWAL(msg); err != nil {
			m.logger.Error().Err(err).Uint64("msg_id", msg.ID).Msg("Failed to write message to WAL during flush")
			// Continue with other messages
		}
	}

	return nil
}

// recover recovers messages from existing WAL files on startup
func (m *Manager) recover() error {
	m.logger.Info().Msg("Starting WAL recovery")
	recoveryStart := time.Now()

	walFiles, err := filepath.Glob(filepath.Join(m.cfg.Storage.WALDir, "*.wal"))
	if err != nil {
		return fmt.Errorf("failed to find WAL files: %w", err)
	}

	for _, walFile := range walFiles {
		topic := getTopicFromWALFile(walFile)
		if err := m.recoverTopicWAL(topic, walFile); err != nil {
			m.logger.Error().Err(err).Str("wal_file", walFile).Msg("Failed to recover WAL file")
			// Continue with other files
		}
	}

	recoveryDuration := time.Since(recoveryStart)
	m.metrics.RecordWALRecovery(recoveryDuration)

	m.logger.Info().
		Uint64("total_messages", m.totalMessages.Load()).
		Int("wal_files", len(walFiles)).
		Dur("recovery_duration", recoveryDuration).
		Msg("WAL recovery completed")

	return nil
}

// recoverTopicWAL recovers messages from a specific topic WAL file
func (m *Manager) recoverTopicWAL(topic, walFile string) error {
	walInstance, err := NewWAL(walFile)
	if err != nil {
		return fmt.Errorf("failed to open WAL file %s: %w", walFile, err)
	}

	topicWAL := &TopicWAL{
		topic:     topic,
		wal:       walInstance,
		memBuffer: NewMemoryBuffer(m.cfg.Storage.MemoryBuffer / 10),
	}

	// Read all entries from WAL
	reader := walInstance.NewReader()
	messageCount := uint64(0)
	maxID := uint64(0)

	for {
		offset, data, err := reader.Next()
		if err != nil {
			if err.Error() == "EOF" || err.Error() == "no more entries" {
				break
			}
			return fmt.Errorf("failed to read WAL entry: %w", err)
		}

		msg, err := m.deserializeMessage(data)
		if err != nil {
			m.logger.Error().Err(err).Uint64("offset", offset).Msg("Failed to deserialize message during recovery")
			continue
		}

		// Update metadata in BadgerDB
		if err := m.updateMessageMetadata(msg, offset); err != nil {
			m.logger.Error().Err(err).Uint64("msg_id", msg.ID).Msg("Failed to update message metadata during recovery")
		}

		messageCount++
		if msg.ID > maxID {
			maxID = msg.ID
		}
		topicWAL.lastOffset.Store(offset)
	}

	// Update global message ID counter
	if maxID > m.messageID.Load() {
		m.messageID.Store(maxID)
	}

	// During recovery, we need to update our statistics to reflect what's on disk
	// This helps with proper functioning after restart
	m.totalMessages.Add(messageCount)
	m.topicWALs.Store(topic, topicWAL)

	m.logger.Debug().
		Str("topic", topic).
		Uint64("message_count", messageCount).
		Uint64("max_id", maxID).
		Msg("Recovered topic WAL")

	return nil
}

// sanitizeTopicName converts MQTT topic to safe filename
func sanitizeTopicName(topic string) string {
	// Replace problematic characters with safe alternatives
	result := ""
	for _, r := range topic {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			result += "_"
		case '+':
			result += "plus"
		case '#':
			result += "hash"
		default:
			result += string(r)
		}
	}
	return result
}

// getTopicFromWALFile extracts topic name from WAL filename
func getTopicFromWALFile(walFile string) string {
	base := filepath.Base(walFile)
	return base[:len(base)-4] // Remove .wal extension
}

// badgerLogger adapts zerolog to Badger's logger interface
type badgerLogger struct {
	logger zerolog.Logger
}

func (l *badgerLogger) Errorf(format string, args ...interface{}) {
	l.logger.Error().Msgf(format, args...)
}

func (l *badgerLogger) Warningf(format string, args ...interface{}) {
	l.logger.Warn().Msgf(format, args...)
}

func (l *badgerLogger) Infof(format string, args ...interface{}) {
	l.logger.Info().Msgf(format, args...)
}

func (l *badgerLogger) Debugf(format string, args ...interface{}) {
	l.logger.Debug().Msgf(format, args...)
}

// MQTT Hook Interface Implementation

// ID returns the hook identifier
func (m *Manager) ID() string {
	return "wal-storage"
}

// Provides indicates which hook methods this hook provides
func (m *Manager) Provides(b byte) bool {
	// Only provide the hooks we actually need for storage functionality
	switch b {
	case mqtt.OnConnect, mqtt.OnDisconnect, mqtt.OnPublish, mqtt.OnPublished:
		return true
	default:
		return false
	}
}

// Init initializes the hook with configuration
func (m *Manager) Init(config any) error {
	m.logger.Debug().Msg("Initializing storage hook")
	return nil
}

// OnConnectAuthenticate is called to authenticate a connecting client
func (m *Manager) OnConnectAuthenticate(cl *mqtt.Client, pk packets.Packet) bool {
	// Storage manager doesn't handle authentication, allow all
	return true
}

// OnConnect is called when a client connects
func (m *Manager) OnConnect(cl *mqtt.Client, pk packets.Packet) error {
	m.logger.Debug().Str("client_id", cl.ID).Msg("Client connected")
	return nil
}

// OnDisconnect is called when a client disconnects
func (m *Manager) OnDisconnect(cl *mqtt.Client, err error, expire bool) {
	m.logger.Debug().Str("client_id", cl.ID).Bool("expire", expire).Msg("Client disconnected")
}

// OnPublish is called when a message is published (before processing)
func (m *Manager) OnPublish(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	if pk.FixedHeader.Type != packets.Publish {
		m.logger.Debug().Uint8("packet_type", uint8(pk.FixedHeader.Type)).Uint8("expected", uint8(packets.Publish)).Msg("Packet type mismatch in OnPublish")
		return pk, nil
	}

	// Create message from packet
	msg := &Message{
		ID:        m.messageID.Add(1),
		Topic:     pk.TopicName,
		Payload:   pk.Payload,
		QoS:       pk.FixedHeader.Qos,
		Retain:    pk.FixedHeader.Retain,
		ClientID:  cl.ID,
		Timestamp: time.Now(),
	}

	// Persist the message
	if err := m.persistMessage(msg); err != nil {
		m.logger.Error().Err(err).Str("topic", pk.TopicName).Msg("Failed to persist message")
		return pk, fmt.Errorf("failed to persist message: %w", err)
	}

	// Note: totalMessages already incremented in persistMessage
	// totalBytes will be updated when actually written to WAL

	m.logger.Debug().
		Uint64("msg_id", msg.ID).
		Str("topic", pk.TopicName).
		Int("payload_size", len(pk.Payload)).
		Str("client_id", cl.ID).
		Msg("Message persisted")

	return pk, nil
}

// OnPublished is called after a message has been published
func (m *Manager) OnPublished(cl *mqtt.Client, pk packets.Packet) {
	m.logger.Debug().Str("topic", pk.TopicName).Str("client_id", cl.ID).Msg("Message published")
}

// OnACLCheck is called to check ACL permissions (we don't handle ACL in storage)
func (m *Manager) OnACLCheck(cl *mqtt.Client, topic string, write bool) bool {
	// Storage manager doesn't handle ACL, always return true (allow)
	return true
}

// Storage Query Methods

// GetMessagesByTopic retrieves messages for a specific topic from WAL
func (m *Manager) GetMessagesByTopic(topic string, limit int) ([]*Message, error) {
	topicWAL, exists := m.topicWALs.Load(topic)
	if !exists {
		return []*Message{}, nil
	}

	wal := topicWAL.(*TopicWAL)
	reader := wal.wal.NewReader()
	messages := make([]*Message, 0, limit)
	count := 0

	for count < limit {
		_, data, err := reader.Next()
		if err != nil {
			if err.Error() == "EOF" || err.Error() == "no more entries" {
				break
			}
			return nil, fmt.Errorf("failed to read WAL entry: %w", err)
		}

		msg, err := m.deserializeMessage(data)
		if err != nil {
			m.logger.Error().Err(err).Msg("Failed to deserialize message")
			continue
		}

		messages = append(messages, msg)
		count++
	}

	return messages, nil
}

// GetStats returns storage statistics
func (m *Manager) GetStats() map[string]interface{} {
	topicCount := 0
	m.topicWALs.Range(func(key, value interface{}) bool {
		topicCount++
		return true
	})

	return map[string]interface{}{
		"total_messages":      m.totalMessages.Load(),
		"total_bytes":         m.totalBytes.Load(),
		"topic_count":         topicCount,
		"memory_buffer_size":  m.memBuffer.Size(),
		"memory_buffer_count": m.memBuffer.Count(),
		"next_message_id":     m.messageID.Load() + 1,
	}
}

// Compact performs WAL compaction for a topic (removes old entries)
func (m *Manager) CompactTopic(topic string, beforeTimestamp time.Time) error {
	_, exists := m.topicWALs.Load(topic)
	if !exists {
		return fmt.Errorf("topic WAL not found: %s", topic)
	}

	// This is a placeholder for WAL compaction logic
	// In a production system, you would implement actual compaction
	m.logger.Info().
		Str("topic", topic).
		Time("before", beforeTimestamp).
		Msg("WAL compaction requested (not implemented)")

	return nil
}

// ForceFlush forces all memory buffers to flush to WAL
func (m *Manager) ForceFlush() error {
	m.logger.Info().Msg("Force flushing all memory buffers")

	// Flush main memory buffer
	if err := m.flushMemoryBuffer(); err != nil {
		return fmt.Errorf("failed to flush main memory buffer: %w", err)
	}

	// Flush per-topic memory buffers
	m.topicWALs.Range(func(key, value interface{}) bool {
		topicWAL := value.(*TopicWAL)
		messages := topicWAL.memBuffer.Flush()
		for _, msg := range messages {
			if err := m.writeMessageToWAL(msg); err != nil {
				m.logger.Error().Err(err).Uint64("msg_id", msg.ID).Msg("Failed to write message during force flush")
			}
		}
		return true
	})

	// Force sync all WALs
	m.syncAllWALs()

	return nil
}

// metricsLoop periodically reports storage metrics
func (m *Manager) metricsLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(5 * time.Second) // Report metrics every 5 seconds
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.reportMetrics()
		}
	}
}

// reportMetrics reports current storage metrics
func (m *Manager) reportMetrics() {
	// Report memory buffer usage
	bufferUsage := int64(m.memBuffer.Size())
	m.metrics.RecordWALBufferUsage(bufferUsage)

	// Report per-topic buffer usage as well
	var totalTopicBufferUsage int64
	m.topicWALs.Range(func(key, value interface{}) bool {
		topicWAL := value.(*TopicWAL)
		totalTopicBufferUsage += int64(topicWAL.memBuffer.Size())
		return true
	})

	// Report total buffer usage (main buffer + all topic buffers)
	totalBufferUsage := bufferUsage + totalTopicBufferUsage
	m.metrics.RecordWALBufferUsage(totalBufferUsage)
}
