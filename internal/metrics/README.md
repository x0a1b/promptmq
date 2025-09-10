# PromptMQ Metrics System

High-performance enterprise-grade metrics collection system for PromptMQ MQTT broker, designed for 1M+ msg/sec throughput with <10ms latency requirements.

## Features

### 🚀 Performance-Optimized
- **<1µs overhead** per message operation
- **Zero-allocation** metrics collection using atomic counters
- **Thread-safe** concurrent access with minimal contention
- **Optimized histogram buckets** for enterprise latency requirements

### 📊 Comprehensive Metrics
- **MQTT Connection Metrics**: Current connections, total connections, disconnections, connection duration
- **Message Metrics**: Messages published/received, bytes transmitted, message size distribution
- **QoS Distribution**: Separate counters for QoS 0, 1, and 2 messages  
- **Topic Metrics**: Per-topic message counts, byte counts, subscriber counts (with cardinality limits)
- **WAL Storage Metrics**: Write latency, buffer usage, recovery time, persistence operations
- **System Metrics**: Memory usage, goroutine count, GC duration, uptime
- **Performance Metrics**: Throughput rates, processing latency (P50, P95, P99)

### 🔧 Enterprise Integration
- **Prometheus-compatible** metrics endpoint at configurable path (default `/metrics`)
- **Mochi MQTT Hook interface** implementation for seamless integration
- **HTTP metrics server** with graceful shutdown and health checks
- **Configuration-driven** with enable/disable capability
- **Comprehensive logging** with structured logs

## Configuration

```go
type MetricsConfig struct {
    Enabled bool   `mapstructure:"metrics"`          // Enable/disable metrics collection
    Bind    string `mapstructure:"metrics-bind"`     // HTTP server bind address (default: 0.0.0.0:9090)
    Path    string `mapstructure:"metrics-path"`     // Metrics endpoint path (default: /metrics)
}
```

## Usage

### Basic Setup

```go
import (
    "github.com/x0a1b/promptmq/internal/config"
    "github.com/x0a1b/promptmq/internal/metrics"
)

cfg := &config.Config{
    Metrics: config.MetricsConfig{
        Enabled: true,
        Bind:    "0.0.0.0:9090",
        Path:    "/metrics",
    },
}

logger := zerolog.New(os.Stdout)
metricsServer, err := metrics.New(cfg, logger)
if err != nil {
    log.Fatal(err)
}

// Start HTTP metrics server
ctx := context.Background()
if err := metricsServer.Start(ctx); err != nil {
    log.Fatal(err)
}
```

### MQTT Integration

The metrics server implements the `mqtt.Hook` interface and automatically collects metrics when added to the MQTT server:

```go
import "github.com/mochi-mqtt/server/v2"

mqttServer := mqtt.New(&mqtt.Options{InlineClient: true})

// Add metrics hook
err := mqttServer.AddHook(metricsServer, nil)
if err != nil {
    log.Fatal(err)
}
```

### Storage Integration

For WAL storage metrics, integrate with the storage manager:

```go
// Create storage manager with metrics integration
storageManager, err := storage.NewWithMetrics(cfg, logger, metricsServer)
if err != nil {
    log.Fatal(err)
}
```

### Manual Metrics Recording

```go
// Record WAL operations
metricsServer.RecordWALWrite(500*time.Microsecond, true)
metricsServer.RecordWALBufferUsage(1024*1024) // 1MB buffer usage
metricsServer.RecordWALRecovery(2*time.Second)

// Record throughput
metricsServer.RecordThroughput(50000.0) // 50K msg/sec
```

## Metrics Reference

### Connection Metrics
- `promptmq_connections_current`: Current active client connections
- `promptmq_connections_total`: Total client connections established
- `promptmq_disconnections_total`: Total client disconnections
- `promptmq_connection_duration_seconds`: Client connection duration histogram

### Message Metrics  
- `promptmq_messages_published_total`: Total messages published
- `promptmq_messages_received_total`: Total messages received
- `promptmq_bytes_published_total`: Total bytes published
- `promptmq_bytes_received_total`: Total bytes received
- `promptmq_message_processing_latency_microseconds`: Message processing latency histogram
- `promptmq_message_size_bytes`: Message size distribution histogram

### QoS Metrics
- `promptmq_qos0_messages_total`: QoS 0 message count
- `promptmq_qos1_messages_total`: QoS 1 message count  
- `promptmq_qos2_messages_total`: QoS 2 message count

### Topic Metrics
- `promptmq_topic_messages_total{topic}`: Messages per topic
- `promptmq_topic_bytes_total{topic}`: Bytes per topic
- `promptmq_topic_subscribers{topic}`: Subscribers per topic

### Storage/WAL Metrics
- `promptmq_wal_write_latency_microseconds`: WAL write latency histogram
- `promptmq_wal_write_errors_total`: WAL write error count
- `promptmq_wal_buffer_usage_bytes`: Current WAL buffer usage
- `promptmq_wal_recovery_duration_seconds`: WAL recovery time histogram
- `promptmq_persistence_operations_total{operation,status}`: Persistence operation counts

### System Metrics
- `promptmq_memory_usage_bytes`: Current memory usage
- `promptmq_goroutines_total`: Active goroutine count
- `promptmq_gc_duration_seconds`: Garbage collection duration histogram
- `promptmq_uptime_seconds`: Server uptime

### Performance Metrics
- `promptmq_throughput_messages_per_second`: Current message throughput
- `promptmq_processing_latency_microseconds`: End-to-end processing latency histogram

## Performance Characteristics

### Benchmarks (Apple M4 Max)
- **Single-threaded OnPublish**: ~128.8 ns/op (0.13µs) - **✅ Under 1µs requirement**
- **Parallel OnPublish**: ~469.4 ns/op (0.47µs) - **✅ Under 1µs requirement**  
- **Zero-allocation operations**: ~3.0 ns/op - **✅ Zero allocations**
- **High throughput simulation**: 2.4M ops/sec - **✅ Exceeds 1M msg/sec target**

### Memory Usage
- **Zero heap allocations** during normal operation
- **Atomic counters** for thread-safe updates without locks
- **Bounded memory** growth with topic cardinality limits
- **Custom Prometheus registry** prevents global state conflicts

### Latency Impact
- **<1µs overhead** per message (meets enterprise requirement)
- **Minimal GC pressure** through zero-allocation design
- **Optimized histogram buckets** for sub-millisecond latencies
- **Efficient wildcard filtering** to prevent metric explosion

## HTTP Endpoints

- `GET /metrics` - Prometheus-compatible metrics exposition
- `GET /health` - Health check endpoint (returns "OK")

Example metrics output:
```
# HELP promptmq_messages_published_total Total number of messages published
# TYPE promptmq_messages_published_total counter
promptmq_messages_published_total 1234567

# HELP promptmq_message_processing_latency_microseconds Message processing latency in microseconds
# TYPE promptmq_message_processing_latency_microseconds histogram
promptmq_message_processing_latency_microseconds_bucket{le="100"} 945231
promptmq_message_processing_latency_microseconds_bucket{le="500"} 1198234
promptmq_message_processing_latency_microseconds_sum 1.23456789e+09
promptmq_message_processing_latency_microseconds_count 1234567
```

## Error Handling

- **Graceful degradation** when metrics are disabled
- **Non-blocking operations** - metrics failures don't affect message processing
- **Structured logging** for debugging and monitoring
- **Health checks** for monitoring system availability

## Best Practices

1. **Enable metrics in production** for observability
2. **Monitor /health endpoint** for service health
3. **Set appropriate histogram buckets** for your latency requirements
4. **Limit topic cardinality** to prevent memory issues
5. **Use Prometheus alerting** for operational monitoring

## Testing

```bash
# Run all tests
go test -v ./internal/metrics/

# Run performance benchmarks
go test -bench=. -benchmem ./internal/metrics/

# Test specific functionality
go test -run TestMetricsHooks_OnPublish ./internal/metrics/
```

The metrics system includes comprehensive unit tests and performance benchmarks to validate the <1µs overhead requirement and ensure zero-allocation operation.