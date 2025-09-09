# STAGE 2: WAL Compaction Implementation Plan

## Overview
Implement production-ready WAL compaction system with transparent, reliable operation and zero write blocking.

## Core Requirements
- **Never block writes** - compaction runs entirely in background
- **Hybrid retention**: 2 hours OR 100MB per topic WAL file
- **Priority-based**: Size-exceeded WALs compact first, then age-exceeded
- **Transparent operation**: Zero user configuration required
- **Performance target**: < 5% throughput impact, < 10% latency increase

## Implementation Phases

### Phase 1: Configuration & Infrastructure
- Add compaction settings to `internal/config/config.go`
- Default values:
  - `max_message_age`: 2h
  - `max_wal_size`: 100MB  
  - `check_interval`: 5m
  - `concurrent_compactions`: 2
  - `compaction_batch_size`: 1000

### Phase 2: Compaction Core Engine
- **Priority Queue**: Size-exceeded WALs first, then age-exceeded
- **Background Scanner**: Every 5 minutes, identifies compaction candidates
- **In-Place Compaction**: Atomic `.tmp` → `.wal` file replacement
- **Worker Pool**: Configurable concurrent compaction workers

### Phase 3: Reliability & Safety (Critical)
#### WAL Reliability During Compaction:
- **Concurrent Safety**: Per-topic RW locks prevent corruption
- **Crash Recovery**: Original WAL preserved until compaction complete
- **Atomic Operations**: File swap via atomic rename operation
- **Write Continuity**: New messages continue to original WAL during compaction
- **Offset Tracking**: Update in-memory pointers after successful swap

#### Key Safety Mechanisms:
1. **Read-Write Isolation**: Compaction reads while writes continue
2. **Failure Recovery**: Cleanup incomplete `.tmp` files on startup
3. **Data Integrity**: Checksum validation during compaction
4. **Zero Message Loss**: Original WAL kept until new WAL verified

### Phase 4: Observability & Metrics
- Compaction duration, messages removed, space reclaimed
- Queue depths and processing rates per priority level
- Performance impact metrics (throughput, latency, CPU, memory)
- Simple logging: "Compacted topic X: removed Y messages, reclaimed Z MB"

### Phase 5: Performance Benchmarking
#### Before/After Comparison Metrics:
- **Message Throughput**: msgs/sec with compaction ON vs OFF
- **Write Latency**: P50, P95, P99 latencies during compaction
- **Memory Usage**: RAM impact of compaction system
- **CPU Overhead**: Background scanner and worker CPU usage
- **I/O Impact**: Disk patterns during compaction cycles

#### Benchmark Scenarios:
1. **Steady State**: Normal operation, no compaction needed
2. **Light Compaction**: Few topics exceeding thresholds
3. **Heavy Compaction**: Many topics simultaneously compacting
4. **Burst Load**: High write volume during active compaction
5. **Mixed Workload**: Various topic sizes with different compaction needs

#### Performance Acceptance Criteria:
- **Throughput Loss**: < 5% impact on message/sec during compaction
- **Latency Increase**: < 10% increase in P95 write latency  
- **Memory Overhead**: < 50MB additional RAM for compaction system
- **CPU Impact**: < 5% additional CPU usage during compaction

### Phase 6: Testing & Validation
- Unit tests for compaction logic and safety mechanisms
- Integration tests with high write load during compaction
- Crash recovery tests with incomplete compactions
- Performance regression tests against baseline (651K+ msg/sec)
- Long-running stability tests (24h+ with continuous compaction)

## Implementation Files to Modify
1. `internal/config/config.go` - Add compaction configuration
2. `internal/storage/storage.go` - Add compaction manager and logic
3. `internal/storage/compaction.go` - New file for compaction implementation
4. `internal/storage/wal.go` - Add compaction support to WAL
5. `internal/storage/storage_test.go` - Add compaction tests
6. `internal/storage/performance_test.go` - Add compaction benchmarks

## Success Criteria
- ✅ All existing WAL tests pass
- ✅ New compaction tests pass (unit + integration)
- ✅ Performance benchmarks meet acceptance criteria
- ✅ Zero message loss under all failure scenarios
- ✅ Production-ready code quality and documentation
- ✅ Transparent operation requiring zero user configuration

## Rollback Plan
- Compaction can be disabled via configuration
- Original WAL implementation remains unchanged
- Graceful degradation if compaction fails (WALs grow but system continues)