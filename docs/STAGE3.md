# STAGE 3: Enhanced Durability & Crash Recovery Implementation Plan

## Overview
Add configurable immediate fsync option and comprehensive crash recovery testing to ensure SQLite-like ACID durability guarantees.

## Phase 1: Configuration Enhancement
### New Configuration Options:
```yaml
storage:
  wal:
    sync_mode: "periodic|immediate|batch"  # Sync strategy
    sync_interval: 100ms                   # For periodic mode
    batch_sync_size: 100                   # For batch mode
    force_fsync: true                      # Always fsync (overrides WALNoSync)
    crash_recovery_validation: true       # Enable crash recovery tests
```

### Sync Mode Behaviors:
- **periodic**: Current behavior (sync every interval)
- **immediate**: fsync after every write (SQLite-like durability)
- **batch**: fsync after N messages or timeout

## Phase 2: Immediate Sync Implementation
### WAL Enhancement:
- Add `SyncMode` enum to WAL struct
- Modify `Write()` method to conditionally sync based on mode
- Add `WriteWithSync()` method for guaranteed immediate durability
- Maintain performance metrics for each sync mode

### Storage Manager Integration:
- Route sync mode from config to WAL instances
- Add per-topic sync mode override capability
- Track sync performance impact in metrics

## Phase 3: Crash Recovery Validation Framework
### Test Infrastructure:
```go
// Crash simulation framework
type CrashSimulator struct {
    killSignals    []os.Signal
    crashPoints    []CrashPoint
    recoveryTests  []RecoveryTest
}

type CrashPoint int
const (
    CrashAfterWrite CrashPoint = iota
    CrashBeforeSync
    CrashAfterSync
    CrashDuringCompaction
    CrashAfterFlush
)
```

### Recovery Test Scenarios:
1. **Write Crash Tests**: Kill process at various points during message writing
2. **Sync Crash Tests**: Kill during sync operations
3. **Compaction Crash Tests**: Kill during WAL compaction
4. **Concurrent Crash Tests**: Kill during high-load concurrent writes
5. **Corruption Tests**: Simulate partial writes and validate recovery

## Phase 4: ACID Compliance Tests
### Transaction-like Guarantees:
- **Atomicity**: Multi-message batch writes are atomic
- **Consistency**: WAL structure remains valid after crash
- **Isolation**: Concurrent writes don't corrupt WAL
- **Durability**: Synced messages survive any crash

### Test Suite:
```go
func TestSQLiteLikeDurability(t *testing.T)
func TestAtomicBatchWrites(t *testing.T)  
func TestConcurrentWriteCrashRecovery(t *testing.T)
func TestWALConsistencyAfterCrash(t *testing.T)
func TestPartialWriteRecovery(t *testing.T)
func TestCompactionCrashSafety(t *testing.T)
```

## Phase 5: Performance Impact Analysis
### Benchmarks:
- **Immediate vs Periodic**: Throughput comparison
- **Sync Mode Overhead**: Latency impact measurement  
- **Recovery Speed**: Time to recover from various crash scenarios
- **ACID Compliance Cost**: Performance cost of full durability

### Acceptance Criteria:
- **Immediate Sync**: < 50% throughput loss acceptable for max durability
- **Recovery Time**: < 5 seconds for 1M messages
- **Data Integrity**: 100% message recovery for synced data
- **Zero Corruption**: No WAL corruption after any crash scenario

## Phase 6: Advanced Recovery Features
### Enhanced Recovery:
- **Checksum Validation**: Verify WAL integrity during recovery
- **Partial Recovery**: Recover valid messages from corrupted WAL segments
- **Recovery Progress**: Real-time recovery progress reporting
- **Auto-Repair**: Automatically fix minor WAL inconsistencies

## Implementation Order:
1. **Config Structure** → 2. **Immediate Sync** → 3. **Crash Simulator** → 4. **Recovery Tests** → 5. **ACID Tests** → 6. **Performance Benchmarks** → 7. **Advanced Recovery**

## Technical Implementation Details

### Configuration Structure:
```go
type WALConfig struct {
    SyncMode              string        `mapstructure:"sync-mode"`
    SyncInterval          time.Duration `mapstructure:"sync-interval"`
    BatchSyncSize         int           `mapstructure:"batch-sync-size"`
    ForceFsync           bool          `mapstructure:"force-fsync"`
    CrashRecoveryValidation bool        `mapstructure:"crash-recovery-validation"`
}
```

### WAL Sync Modes:
```go
type SyncMode int
const (
    SyncPeriodic SyncMode = iota  // Current behavior
    SyncImmediate                 // fsync after every write
    SyncBatch                     // fsync after N messages
)
```

### Crash Simulation Framework:
```go
type CrashSimulator struct {
    processCmd    *exec.Cmd
    crashSignal   os.Signal
    crashDelay    time.Duration
    recoveryCheck func(*testing.T) bool
}

func (cs *CrashSimulator) SimulateCrash(t *testing.T, crashPoint CrashPoint) error
func (cs *CrashSimulator) VerifyRecovery(t *testing.T) bool
func (cs *CrashSimulator) ValidateDataIntegrity(t *testing.T, expectedMessages []Message) bool
```

## Files to Modify:
1. `internal/config/config.go` - Add WAL durability config
2. `internal/storage/wal.go` - Add immediate sync support
3. `internal/storage/storage.go` - Integrate sync modes
4. `internal/storage/crash_test.go` - **New**: Crash simulation tests
5. `internal/storage/recovery_test.go` - **New**: Recovery validation tests
6. `internal/storage/durability_test.go` - **New**: ACID compliance tests
7. `internal/storage/performance_test.go` - Add durability benchmarks

## Test Coverage Requirements:
- **Unit Tests**: 100% coverage for all sync modes
- **Integration Tests**: Full crash recovery scenarios
- **Performance Tests**: Benchmark all sync modes
- **Stress Tests**: High-load concurrent crash scenarios
- **Corruption Tests**: Handle partial writes and corrupted data

## Performance Targets:
- **Immediate Sync Mode**: 
  - Target: > 100K msg/sec (down from 692K baseline)
  - Acceptable: > 50K msg/sec for maximum durability
  - Latency: < 1ms P99 write latency
- **Recovery Performance**:
  - Target: < 1 second for 100K messages
  - Acceptable: < 5 seconds for 1M messages
- **Memory Usage**: < 100MB additional RAM for durability features

## Rollback Strategy:
- All new durability features behind configuration flags
- Default behavior unchanged (backward compatible)
- Graceful degradation if durability features fail
- Option to disable crash recovery validation in production

## Success Criteria:
- ✅ SQLite-like durability option available (`sync_mode: immediate`)
- ✅ Zero data loss for immediate sync mode under any crash scenario
- ✅ 100% crash recovery success rate for all test scenarios
- ✅ All ACID properties validated through comprehensive test suite
- ✅ Performance trade-offs clearly measured and documented
- ✅ Production-ready reliability with configurable durability levels
- ✅ Backward compatibility maintained for existing deployments
- ✅ Comprehensive documentation for durability configuration options

## Risk Mitigation:
- **Performance Impact**: Benchmarks validate acceptable performance trade-offs
- **Complexity**: Phased implementation allows incremental validation
- **Reliability**: Extensive crash testing ensures production readiness
- **Compatibility**: Feature flags ensure zero impact on existing deployments