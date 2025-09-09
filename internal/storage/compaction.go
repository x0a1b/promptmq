package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/zohaib-hassan/promptmq/internal/config"
)

// CompactionManager handles WAL file compaction with priority-based scheduling
type CompactionManager struct {
	cfg              *config.Config
	logger           zerolog.Logger
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	metrics          StorageMetrics
	storage          *Manager
	
	// Priority queues for compaction candidates
	criticalQueue    chan string // Size-exceeded topics (priority)
	normalQueue      chan string // Age-exceeded topics
	
	// Worker management
	activeWorkers    int
	workerMu         sync.RWMutex
	
	// Compaction metrics
	totalCompactions   uint64
	totalSpaceReclaimed uint64
	totalMessagesRemoved uint64
	
	// Shutdown management
	stopOnce sync.Once
	compactionDuration  time.Duration
	mu                 sync.RWMutex
}

// CompactionCandidate represents a WAL file that needs compaction
type CompactionCandidate struct {
	Topic        string
	WalPath      string
	CurrentSize  uint64
	OldestMessage time.Time
	Priority     CompactionPriority
}

// CompactionPriority defines compaction urgency levels
type CompactionPriority int

const (
	CriticalPriority CompactionPriority = iota // Size exceeded
	NormalPriority                             // Age exceeded
)

// NewCompactionManager creates a new compaction manager
func NewCompactionManager(cfg *config.Config, logger zerolog.Logger, storage *Manager, metrics StorageMetrics) *CompactionManager {
	ctx, cancel := context.WithCancel(context.Background())
	
	return &CompactionManager{
		cfg:           cfg,
		logger:        logger.With().Str("component", "compaction").Logger(),
		ctx:           ctx,
		cancel:        cancel,
		metrics:       metrics,
		storage:       storage,
		criticalQueue: make(chan string, 100), // Buffer for critical compactions
		normalQueue:   make(chan string, 100), // Buffer for normal compactions
	}
}

// Start begins the compaction manager background processes
func (cm *CompactionManager) Start() error {
	cm.logger.Info().
		Dur("max_message_age", cm.cfg.Storage.Compaction.MaxMessageAge).
		Uint64("max_wal_size", cm.cfg.Storage.Compaction.MaxWALSize).
		Dur("check_interval", cm.cfg.Storage.Compaction.CheckInterval).
		Int("concurrent_workers", cm.cfg.Storage.Compaction.ConcurrentWorkers).
		Msg("Starting WAL compaction manager")

	// Start background scanner
	cm.wg.Add(1)
	go cm.scanLoop()

	// Start worker pool
	for i := 0; i < cm.cfg.Storage.Compaction.ConcurrentWorkers; i++ {
		cm.wg.Add(1)
		go cm.worker(i)
	}

	return nil
}

// Stop gracefully shuts down the compaction manager
func (cm *CompactionManager) Stop() error {
	var err error
	cm.stopOnce.Do(func() {
		cm.logger.Info().Msg("Stopping WAL compaction manager")
		
		cm.cancel()
		cm.wg.Wait()
		
		// Close channels safely
		close(cm.criticalQueue)
		close(cm.normalQueue)
	})
	return err
	
	cm.logger.Info().
		Uint64("total_compactions", cm.totalCompactions).
		Uint64("space_reclaimed_mb", cm.totalSpaceReclaimed/1024/1024).
		Uint64("messages_removed", cm.totalMessagesRemoved).
		Dur("total_compaction_time", cm.compactionDuration).
		Msg("Compaction manager stopped")
	
	return nil
}

// scanLoop periodically scans for WAL files needing compaction
func (cm *CompactionManager) scanLoop() {
	defer cm.wg.Done()
	
	interval := cm.cfg.Storage.Compaction.CheckInterval
	if interval <= 0 {
		interval = 5 * time.Minute // Default fallback
	}
	
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	
	// Run initial scan
	cm.scanForCandidates()
	
	for {
		select {
		case <-cm.ctx.Done():
			return
		case <-ticker.C:
			cm.scanForCandidates()
		}
	}
}

// scanForCandidates identifies WAL files needing compaction
func (cm *CompactionManager) scanForCandidates() {
	cm.logger.Debug().Msg("Scanning for compaction candidates")
	
	candidates := []CompactionCandidate{}
	now := time.Now()
	
	// Scan all active topic WALs
	cm.storage.topicWALs.Range(func(key, value interface{}) bool {
		topic := key.(string)
		topicWAL := value.(*TopicWAL)
		
		candidate, needsCompaction := cm.evaluateTopic(topic, topicWAL, now)
		if needsCompaction {
			candidates = append(candidates, candidate)
		}
		
		return true
	})
	
	// Queue candidates by priority
	cm.queueCandidates(candidates)
	
	if len(candidates) > 0 {
		cm.logger.Info().
			Int("candidates", len(candidates)).
			Msg("Found compaction candidates")
	}
}

// evaluateTopic determines if a topic WAL needs compaction
func (cm *CompactionManager) evaluateTopic(topic string, topicWAL *TopicWAL, now time.Time) (CompactionCandidate, bool) {
	candidate := CompactionCandidate{
		Topic:   topic,
		WalPath: topicWAL.wal.path,
	}
	
	// Get WAL file size
	size, err := topicWAL.wal.Size()
	if err != nil {
		cm.logger.Error().Err(err).Str("topic", topic).Msg("Failed to get WAL size")
		return candidate, false
	}
	candidate.CurrentSize = size
	
	// Check size threshold (critical priority)
	if size > cm.cfg.Storage.Compaction.MaxWALSize {
		candidate.Priority = CriticalPriority
		cm.logger.Debug().
			Str("topic", topic).
			Uint64("size_mb", size/1024/1024).
			Uint64("max_size_mb", cm.cfg.Storage.Compaction.MaxWALSize/1024/1024).
			Msg("Topic WAL exceeds size threshold")
		return candidate, true
	}
	
	// Check age threshold (normal priority)
	oldestMessage := cm.getOldestMessageTime(topicWAL)
	if !oldestMessage.IsZero() && now.Sub(oldestMessage) > cm.cfg.Storage.Compaction.MaxMessageAge {
		candidate.Priority = NormalPriority
		candidate.OldestMessage = oldestMessage
		cm.logger.Debug().
			Str("topic", topic).
			Time("oldest_message", oldestMessage).
			Dur("age", now.Sub(oldestMessage)).
			Dur("max_age", cm.cfg.Storage.Compaction.MaxMessageAge).
			Msg("Topic WAL exceeds age threshold")
		return candidate, true
	}
	
	return candidate, false
}

// getOldestMessageTime returns the timestamp of the oldest message in WAL
func (cm *CompactionManager) getOldestMessageTime(topicWAL *TopicWAL) time.Time {
	reader := topicWAL.wal.NewReader()
	if reader == nil {
		return time.Time{}
	}
	defer reader.Close()
	
	// Read first message to get oldest timestamp
	_, data, err := reader.Next()
	if err != nil {
		return time.Time{}
	}
	
	msg, err := cm.storage.deserializeMessage(data)
	if err != nil {
		return time.Time{}
	}
	
	return msg.Timestamp
}

// queueCandidates adds compaction candidates to appropriate priority queues
func (cm *CompactionManager) queueCandidates(candidates []CompactionCandidate) {
	for _, candidate := range candidates {
		select {
		case <-cm.ctx.Done():
			return
		default:
			if candidate.Priority == CriticalPriority {
				select {
				case cm.criticalQueue <- candidate.Topic:
					cm.logger.Debug().Str("topic", candidate.Topic).Msg("Queued critical compaction")
				default:
					cm.logger.Warn().Str("topic", candidate.Topic).Msg("Critical compaction queue full")
				}
			} else {
				select {
				case cm.normalQueue <- candidate.Topic:
					cm.logger.Debug().Str("topic", candidate.Topic).Msg("Queued normal compaction")
				default:
					cm.logger.Debug().Str("topic", candidate.Topic).Msg("Normal compaction queue full, will retry")
				}
			}
		}
	}
}

// worker is a compaction worker that processes topics from priority queues
func (cm *CompactionManager) worker(workerID int) {
	defer cm.wg.Done()
	
	cm.logger.Debug().Int("worker_id", workerID).Msg("Starting compaction worker")
	
	for {
		select {
		case <-cm.ctx.Done():
			return
			
		// Process critical queue first (priority)
		case topic := <-cm.criticalQueue:
			cm.compactTopic(workerID, topic, CriticalPriority)
			
		// Process normal queue if no critical work
		case topic := <-cm.normalQueue:
			cm.compactTopic(workerID, topic, NormalPriority)
		}
	}
}

// compactTopic performs in-place compaction of a topic's WAL file
func (cm *CompactionManager) compactTopic(workerID int, topic string, priority CompactionPriority) {
	startTime := time.Now()
	logger := cm.logger.With().
		Int("worker_id", workerID).
		Str("topic", topic).
		Str("priority", cm.priorityString(priority)).
		Logger()
	
	logger.Info().Msg("Starting WAL compaction")
	
	cm.updateActiveWorkers(1)
	defer cm.updateActiveWorkers(-1)
	
	// Get topic WAL
	topicWALInterface, exists := cm.storage.topicWALs.Load(topic)
	if !exists {
		logger.Warn().Msg("Topic WAL not found, skipping compaction")
		return
	}
	topicWAL := topicWALInterface.(*TopicWAL)
	
	// Perform atomic compaction
	messagesRemoved, spaceReclaimed, err := cm.performCompaction(topicWAL, logger)
	duration := time.Since(startTime)
	
	if err != nil {
		logger.Error().Err(err).Dur("duration", duration).Msg("WAL compaction failed")
		return
	}
	
	// Update metrics
	cm.updateCompactionMetrics(messagesRemoved, spaceReclaimed, duration)
	
	logger.Info().
		Uint64("messages_removed", messagesRemoved).
		Uint64("space_reclaimed_mb", spaceReclaimed/1024/1024).
		Dur("duration", duration).
		Msg("WAL compaction completed")
}

// performCompaction executes the atomic compaction process
func (cm *CompactionManager) performCompaction(topicWAL *TopicWAL, logger zerolog.Logger) (uint64, uint64, error) {
	// Acquire write lock for this topic WAL to ensure safety
	topicWAL.mu.Lock()
	defer topicWAL.mu.Unlock()
	
	originalPath := topicWAL.wal.path
	tempPath := originalPath + ".compact"
	cutoffTime := time.Now().Add(-cm.cfg.Storage.Compaction.MaxMessageAge)
	
	// Create temporary compacted WAL file
	tempWAL, err := NewWAL(tempPath)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to create temp WAL: %w", err)
	}
	defer func() {
		tempWAL.Close()
		os.Remove(tempPath) // Clean up on any error
	}()
	
	// Read from original WAL and write valid messages to temp WAL
	reader := topicWAL.wal.NewReader()
	if reader == nil {
		return 0, 0, fmt.Errorf("failed to create WAL reader")
	}
	defer reader.Close()
	
	var messagesProcessed, messagesRemoved, messagesRetained uint64
	originalSize, _ := topicWAL.wal.Size()
	
	batchWriter := tempWAL.NewBatchWriter(cm.cfg.Storage.Compaction.BatchSize)
	
	for {
		_, data, err := reader.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return 0, 0, fmt.Errorf("failed to read WAL entry: %w", err)
		}
		
		messagesProcessed++
		
		// Deserialize to check timestamp
		msg, err := cm.storage.deserializeMessage(data)
		if err != nil {
			logger.Warn().Err(err).Msg("Skipping corrupted message during compaction")
			messagesRemoved++
			continue
		}
		
		// Keep message if it's newer than cutoff time
		if msg.Timestamp.After(cutoffTime) {
			if err := batchWriter.Add(data); err != nil {
				return 0, 0, fmt.Errorf("failed to write message to temp WAL: %w", err)
			}
			messagesRetained++
		} else {
			messagesRemoved++
		}
		
		// Flush batch periodically for memory efficiency
		if messagesProcessed%uint64(cm.cfg.Storage.Compaction.BatchSize) == 0 {
			if err := batchWriter.Flush(); err != nil {
				return 0, 0, fmt.Errorf("failed to flush batch: %w", err)
			}
		}
	}
	
	// Final flush
	if err := batchWriter.Flush(); err != nil {
		return 0, 0, fmt.Errorf("failed to final flush: %w", err)
	}
	
	// Sync temp WAL to disk
	if err := tempWAL.Sync(); err != nil {
		return 0, 0, fmt.Errorf("failed to sync temp WAL: %w", err)
	}
	
	// Close temp WAL before file operations
	if err := tempWAL.Close(); err != nil {
		return 0, 0, fmt.Errorf("failed to close temp WAL: %w", err)
	}
	
	// Close original WAL
	if err := topicWAL.wal.Close(); err != nil {
		logger.Warn().Err(err).Msg("Failed to close original WAL")
	}
	
	// Atomic file replacement
	if err := os.Rename(tempPath, originalPath); err != nil {
		// Try to reopen original WAL on rename failure
		topicWAL.wal, _ = NewWAL(originalPath)
		return 0, 0, fmt.Errorf("failed to replace WAL file: %w", err)
	}
	
	// Reopen the compacted WAL file
	newWAL, err := NewWAL(originalPath)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to reopen compacted WAL: %w", err)
	}
	topicWAL.wal = newWAL
	
	newSize, _ := topicWAL.wal.Size()
	spaceReclaimed := originalSize - newSize
	
	logger.Debug().
		Uint64("messages_processed", messagesProcessed).
		Uint64("messages_retained", messagesRetained).
		Uint64("messages_removed", messagesRemoved).
		Uint64("original_size_mb", originalSize/1024/1024).
		Uint64("new_size_mb", newSize/1024/1024).
		Uint64("space_reclaimed_mb", spaceReclaimed/1024/1024).
		Msg("Compaction statistics")
	
	return messagesRemoved, spaceReclaimed, nil
}

// Helper methods

func (cm *CompactionManager) updateActiveWorkers(delta int) {
	cm.workerMu.Lock()
	defer cm.workerMu.Unlock()
	cm.activeWorkers += delta
}

func (cm *CompactionManager) updateCompactionMetrics(messagesRemoved, spaceReclaimed uint64, duration time.Duration) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.totalCompactions++
	cm.totalMessagesRemoved += messagesRemoved
	cm.totalSpaceReclaimed += spaceReclaimed
	cm.compactionDuration += duration
}

func (cm *CompactionManager) priorityString(priority CompactionPriority) string {
	switch priority {
	case CriticalPriority:
		return "critical"
	case NormalPriority:
		return "normal"
	default:
		return "unknown"
	}
}

// GetCompactionStats returns compaction statistics
func (cm *CompactionManager) GetCompactionStats() map[string]interface{} {
	cm.mu.RLock()
	cm.workerMu.RLock()
	defer cm.mu.RUnlock()
	defer cm.workerMu.RUnlock()
	
	return map[string]interface{}{
		"total_compactions":      cm.totalCompactions,
		"messages_removed":       cm.totalMessagesRemoved,
		"space_reclaimed_bytes":  cm.totalSpaceReclaimed,
		"total_duration_ms":      cm.compactionDuration.Milliseconds(),
		"active_workers":         cm.activeWorkers,
		"critical_queue_depth":   len(cm.criticalQueue),
		"normal_queue_depth":     len(cm.normalQueue),
	}
}