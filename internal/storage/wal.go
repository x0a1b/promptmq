package storage

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// SyncMode defines WAL synchronization strategies
type SyncMode int

const (
	SyncPeriodic  SyncMode = iota // Current behavior - sync at intervals
	SyncImmediate                 // fsync after every write (SQLite-like)
	SyncBatch                     // fsync after N messages or timeout
)

// WAL implements a high-performance Write-Ahead Log
type WAL struct {
	file          *os.File
	writer        *bufio.Writer
	mu            sync.RWMutex
	offset        uint64
	path          string
	noSync        bool
	syncMode      SyncMode
	batchCounter  atomic.Int64
	batchSyncSize int64
}

// WALReader provides sequential access to WAL entries
type WALReader struct {
	file   *os.File
	reader *bufio.Reader
	offset uint64
}

// WALEntry represents a single entry in the WAL
type WALEntry struct {
	Offset uint64
	Data   []byte
}

const (
	// WAL entry format: [length(4)][checksum(4)][data...]
	walHeaderSize = 8         // 4 bytes length + 4 bytes checksum
	walBufferSize = 64 * 1024 // 64KB buffer
)

// NewWAL creates a new WAL instance with default sync mode
func NewWAL(path string) (*WAL, error) {
	return NewWALWithMode(path, SyncPeriodic, 0)
}

// NewWALWithMode creates a new WAL instance with specified sync mode
func NewWALWithMode(path string, syncMode SyncMode, batchSize int64) (*WAL, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open WAL file: %w", err)
	}

	// Get current file size to set offset
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to stat WAL file: %w", err)
	}

	wal := &WAL{
		file:          file,
		writer:        bufio.NewWriterSize(file, walBufferSize),
		offset:        uint64(stat.Size()),
		path:          path,
		syncMode:      syncMode,
		batchSyncSize: batchSize,
	}

	// Initialize batch counter
	wal.batchCounter.Store(0)

	return wal, nil
}

// Write appends data to the WAL and returns the offset
func (w *WAL) Write(data []byte) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(data) == 0 {
		return 0, fmt.Errorf("cannot write empty data")
	}

	// Calculate checksum (simple XOR checksum for performance)
	checksum := calculateChecksum(data)

	// Write entry header: [length][checksum]
	header := make([]byte, walHeaderSize)
	binary.LittleEndian.PutUint32(header[0:4], uint32(len(data)))
	binary.LittleEndian.PutUint32(header[4:8], checksum)

	// Write header
	if _, err := w.writer.Write(header); err != nil {
		return 0, fmt.Errorf("failed to write WAL header: %w", err)
	}

	// Write data
	if _, err := w.writer.Write(data); err != nil {
		return 0, fmt.Errorf("failed to write WAL data: %w", err)
	}

	currentOffset := w.offset
	w.offset += uint64(walHeaderSize + len(data))

	// Handle sync mode
	switch w.syncMode {
	case SyncImmediate:
		// Immediate sync for maximum durability (SQLite-like)
		if err := w.writer.Flush(); err != nil {
			return 0, fmt.Errorf("failed to flush WAL writer in immediate mode: %w", err)
		}
		if !w.noSync {
			if err := w.file.Sync(); err != nil {
				return 0, fmt.Errorf("failed to sync WAL file in immediate mode: %w", err)
			}
		}
	case SyncBatch:
		// Batch sync after N messages
		if w.batchSyncSize > 0 {
			current := w.batchCounter.Add(1)
			if current >= w.batchSyncSize {
				if err := w.writer.Flush(); err != nil {
					return 0, fmt.Errorf("failed to flush WAL writer in batch mode: %w", err)
				}
				if !w.noSync {
					if err := w.file.Sync(); err != nil {
						return 0, fmt.Errorf("failed to sync WAL file in batch mode: %w", err)
					}
				}
				w.batchCounter.Store(0) // Reset counter after successful sync
			}
		} else {
			// Fallback to immediate sync if batch size is invalid
			if err := w.writer.Flush(); err != nil {
				return 0, fmt.Errorf("failed to flush WAL writer (batch fallback): %w", err)
			}
			if !w.noSync {
				if err := w.file.Sync(); err != nil {
					return 0, fmt.Errorf("failed to sync WAL file (batch fallback): %w", err)
				}
			}
		}
	case SyncPeriodic:
		// No immediate sync - handled by background sync loop
		// This provides best performance but lowest durability guarantees
	default:
		// Unknown sync mode - fallback to periodic
		// This ensures system doesn't fail on invalid configuration
	}

	return currentOffset, nil
}

// Sync flushes and syncs the WAL to disk
func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush WAL writer: %w", err)
	}

	if !w.noSync {
		if err := w.file.Sync(); err != nil {
			return fmt.Errorf("failed to sync WAL file: %w", err)
		}
	}

	return nil
}

// Close closes the WAL
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush WAL writer on close: %w", err)
	}

	if err := w.file.Close(); err != nil {
		return fmt.Errorf("failed to close WAL file: %w", err)
	}

	return nil
}

// NewReader creates a new reader for the WAL
func (w *WAL) NewReader() *WALReader {
	// Open file for reading
	file, err := os.Open(w.path)
	if err != nil {
		return nil
	}

	return &WALReader{
		file:   file,
		reader: bufio.NewReaderSize(file, walBufferSize),
		offset: 0,
	}
}

// Next reads the next entry from the WAL
func (r *WALReader) Next() (uint64, []byte, error) {
	if r.file == nil || r.reader == nil {
		return 0, nil, fmt.Errorf("reader is closed")
	}

	// Read entry header
	header := make([]byte, walHeaderSize)
	n, err := io.ReadFull(r.reader, header)
	if err != nil {
		if err == io.EOF {
			return 0, nil, err
		}
		return 0, nil, fmt.Errorf("failed to read WAL header: %w", err)
	}
	if n != walHeaderSize {
		return 0, nil, fmt.Errorf("incomplete WAL header read")
	}

	// Parse header
	length := binary.LittleEndian.Uint32(header[0:4])
	expectedChecksum := binary.LittleEndian.Uint32(header[4:8])

	// Validate length
	if length > 10*1024*1024 { // 10MB max entry size
		return 0, nil, fmt.Errorf("WAL entry too large: %d bytes", length)
	}

	// Read data
	data := make([]byte, length)
	n, err = io.ReadFull(r.reader, data)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to read WAL data: %w", err)
	}
	if n != int(length) {
		return 0, nil, fmt.Errorf("incomplete WAL data read")
	}

	// Verify checksum
	actualChecksum := calculateChecksum(data)
	if actualChecksum != expectedChecksum {
		return 0, nil, fmt.Errorf("WAL entry checksum mismatch")
	}

	currentOffset := r.offset
	r.offset += uint64(walHeaderSize + length)

	return currentOffset, data, nil
}

// Close closes the WAL reader
func (r *WALReader) Close() error {
	if r.file != nil {
		err := r.file.Close()
		r.file = nil
		r.reader = nil
		return err
	}
	return nil
}

// Seek seeks to a specific offset in the WAL
func (r *WALReader) Seek(offset uint64) error {
	if r.file == nil {
		return fmt.Errorf("reader is closed")
	}

	_, err := r.file.Seek(int64(offset), io.SeekStart)
	if err != nil {
		return fmt.Errorf("failed to seek WAL: %w", err)
	}

	r.reader.Reset(r.file)
	r.offset = offset
	return nil
}

// calculateChecksum computes a simple XOR checksum for performance
func calculateChecksum(data []byte) uint32 {
	var checksum uint32

	// Process 4 bytes at a time for better performance
	i := 0
	for i+4 <= len(data) {
		checksum ^= binary.LittleEndian.Uint32(data[i : i+4])
		i += 4
	}

	// Handle remaining bytes
	for i < len(data) {
		checksum ^= uint32(data[i]) << uint32((i%4)*8)
		i++
	}

	return checksum
}

// Compact removes entries older than the specified offset
func (w *WAL) Compact(keepFromOffset uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Create a temporary file for the compacted WAL
	tempPath := w.path + ".compact"
	tempFile, err := os.OpenFile(tempPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to create temp file for compaction: %w", err)
	}
	defer tempFile.Close()

	// Create reader to read from the current WAL
	reader := w.NewReader()
	if reader == nil {
		return fmt.Errorf("failed to create reader for compaction")
	}
	defer reader.Close()

	// Seek to the keep-from offset
	if err := reader.Seek(keepFromOffset); err != nil {
		return fmt.Errorf("failed to seek to compact offset: %w", err)
	}

	// Copy all entries from keepFromOffset onwards
	tempWriter := bufio.NewWriterSize(tempFile, walBufferSize)

	for {
		_, data, err := reader.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("error reading during compaction: %w", err)
		}

		// Write to temp file
		checksum := calculateChecksum(data)
		header := make([]byte, walHeaderSize)
		binary.LittleEndian.PutUint32(header[0:4], uint32(len(data)))
		binary.LittleEndian.PutUint32(header[4:8], checksum)

		if _, err := tempWriter.Write(header); err != nil {
			return fmt.Errorf("failed to write header during compaction: %w", err)
		}

		if _, err := tempWriter.Write(data); err != nil {
			return fmt.Errorf("failed to write data during compaction: %w", err)
		}
	}

	// Flush temp file
	if err := tempWriter.Flush(); err != nil {
		return fmt.Errorf("failed to flush temp file: %w", err)
	}

	if err := tempFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp file: %w", err)
	}

	// Close current WAL file and writer
	if err := w.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush current WAL: %w", err)
	}

	if err := w.file.Close(); err != nil {
		return fmt.Errorf("failed to close current WAL: %w", err)
	}

	// Replace the old file with the new one
	if err := os.Rename(tempPath, w.path); err != nil {
		return fmt.Errorf("failed to replace WAL file: %w", err)
	}

	// Reopen the file
	w.file, err = os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to reopen WAL file after compaction: %w", err)
	}

	w.writer = bufio.NewWriterSize(w.file, walBufferSize)

	// Update offset
	stat, err := w.file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat WAL file after compaction: %w", err)
	}
	w.offset = uint64(stat.Size())

	return nil
}

// Size returns the current size of the WAL file
func (w *WAL) Size() (uint64, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.offset, nil
}

// SetNoSync disables fsync calls for better performance (less durability)
func (w *WAL) SetNoSync(noSync bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.noSync = noSync
}

// SetSyncMode changes the sync mode at runtime
func (w *WAL) SetSyncMode(syncMode SyncMode, batchSize int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.syncMode = syncMode
	w.batchSyncSize = batchSize
	w.batchCounter.Store(0) // Reset batch counter
}

// GetSyncMode returns the current sync mode
func (w *WAL) GetSyncMode() SyncMode {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.syncMode
}

// ForceBatchSync forces a sync in batch mode regardless of counter
func (w *WAL) ForceBatchSync() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.syncMode != SyncBatch {
		return fmt.Errorf("force batch sync only available in batch mode")
	}

	if err := w.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush WAL writer during force batch sync: %w", err)
	}

	if !w.noSync {
		if err := w.file.Sync(); err != nil {
			return fmt.Errorf("failed to sync WAL file during force batch sync: %w", err)
		}
	}

	w.batchCounter.Store(0) // Reset counter
	return nil
}

// BatchWriter provides batched writing capabilities for better performance
type BatchWriter struct {
	wal     *WAL
	entries [][]byte
	maxSize int
}

// NewBatchWriter creates a new batch writer
func (w *WAL) NewBatchWriter(maxBatchSize int) *BatchWriter {
	return &BatchWriter{
		wal:     w,
		entries: make([][]byte, 0, maxBatchSize),
		maxSize: maxBatchSize,
	}
}

// Add adds an entry to the batch
func (bw *BatchWriter) Add(data []byte) error {
	bw.entries = append(bw.entries, data)

	if len(bw.entries) >= bw.maxSize {
		return bw.Flush()
	}

	return nil
}

// Flush writes all batched entries to the WAL
func (bw *BatchWriter) Flush() error {
	if len(bw.entries) == 0 {
		return nil
	}

	bw.wal.mu.Lock()
	defer bw.wal.mu.Unlock()

	// Write all entries in the batch
	for _, data := range bw.entries {
		checksum := calculateChecksum(data)
		header := make([]byte, walHeaderSize)
		binary.LittleEndian.PutUint32(header[0:4], uint32(len(data)))
		binary.LittleEndian.PutUint32(header[4:8], checksum)

		if _, err := bw.wal.writer.Write(header); err != nil {
			return fmt.Errorf("failed to write batch header: %w", err)
		}

		if _, err := bw.wal.writer.Write(data); err != nil {
			return fmt.Errorf("failed to write batch data: %w", err)
		}

		bw.wal.offset += uint64(walHeaderSize + len(data))
	}

	// Clear the batch
	bw.entries = bw.entries[:0]

	return nil
}

// Stats returns WAL statistics
func (w *WAL) Stats() map[string]interface{} {
	w.mu.RLock()
	defer w.mu.RUnlock()

	syncModeStr := "unknown"
	switch w.syncMode {
	case SyncImmediate:
		syncModeStr = "immediate"
	case SyncBatch:
		syncModeStr = "batch"
	case SyncPeriodic:
		syncModeStr = "periodic"
	}

	return map[string]interface{}{
		"path":             w.path,
		"size":             w.offset,
		"nosync":           w.noSync,
		"sync_mode":        syncModeStr,
		"batch_size":       w.batchSyncSize,
		"batch_counter":    w.batchCounter.Load(),
		"pending_batch":    w.batchCounter.Load() > 0,
	}
}
