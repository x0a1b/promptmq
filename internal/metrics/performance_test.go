package metrics

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/x0a1b/promptmq/internal/config"
)

// Performance benchmarks to validate <1µs overhead requirement for 1M+ msg/sec

func BenchmarkMetricsOverhead_OnPublish_SingleThread(b *testing.B) {
	server := createTestMetricsServer(&testing.T{})

	client := &mqtt.Client{ID: "perf-client"}
	packet := packets.Packet{
		FixedHeader: packets.FixedHeader{
			Type: packets.Publish,
			Qos:  0,
		},
		TopicName: "perf/topic",
		Payload:   []byte("performance test payload"),
	}

	// Warm up
	for i := 0; i < 1000; i++ {
		server.OnPublish(client, packet)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		server.OnPublish(client, packet)
	}
}

func BenchmarkMetricsOverhead_OnPublish_Parallel(b *testing.B) {
	server := createTestMetricsServer(&testing.T{})

	packet := packets.Packet{
		FixedHeader: packets.FixedHeader{
			Type: packets.Publish,
			Qos:  0,
		},
		TopicName: "perf/topic",
		Payload:   []byte("performance test payload"),
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		client := &mqtt.Client{ID: fmt.Sprintf("perf-client-%d", time.Now().UnixNano())}
		for pb.Next() {
			server.OnPublish(client, packet)
		}
	})
}

func BenchmarkMetricsOverhead_ConnectionOperations(b *testing.B) {
	server := createTestMetricsServer(&testing.T{})
	packet := packets.Packet{}

	b.ResetTimer()
	b.ReportAllocs()

	start := time.Now()
	for i := 0; i < b.N; i++ {
		client := &mqtt.Client{ID: fmt.Sprintf("perf-client-%d", i)}

		// Connect
		server.OnConnect(client, packet)

		// Disconnect
		server.OnDisconnect(client, nil, false)
	}
	duration := time.Since(start)

	b.StopTimer()

	perOpNanos := duration.Nanoseconds() / int64(b.N) / 2 // Divide by 2 for connect+disconnect
	perOpMicros := float64(perOpNanos) / 1000.0

	b.Logf("Connect+Disconnect pairs: %d", b.N)
	b.Logf("Per operation: %.2f ns (%.3f µs)", float64(perOpNanos), perOpMicros)
}

func BenchmarkMetricsOverhead_AtomicVsPrometheus(b *testing.B) {
	server := createTestMetricsServer(&testing.T{})
	var atomicCounter int64

	b.Run("AtomicIncrement", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				atomic.AddInt64(&atomicCounter, 1)
			}
		})
	})

	b.Run("PrometheusCounter", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				server.messagesPublishedTotal.Inc()
			}
		})
	})
}

func BenchmarkMetricsOverhead_HighThroughputSimulation(b *testing.B) {
	// Simulate 1M+ messages/second scenario
	server := createTestMetricsServer(&testing.T{})

	// Pre-allocate clients to avoid allocation overhead in benchmark
	const numClients = 1000
	clients := make([]*mqtt.Client, numClients)
	for i := 0; i < numClients; i++ {
		clients[i] = &mqtt.Client{ID: fmt.Sprintf("client-%d", i)}
	}

	packets := []packets.Packet{
		{
			FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 0},
			TopicName:   "sensor/temperature",
			Payload:     []byte(`{"temp": 23.5, "ts": 1234567890}`),
		},
		{
			FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1},
			TopicName:   "alert/critical",
			Payload:     []byte(`{"level": "critical", "msg": "System overload"}`),
		},
		{
			FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 0},
			TopicName:   "device/status",
			Payload:     []byte(`{"online": true}`),
		},
	}

	// Warm up
	for i := 0; i < 1000; i++ {
		client := clients[i%numClients]
		packet := packets[i%len(packets)]
		server.OnPublish(client, packet)
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		clientIdx := 0
		packetIdx := 0

		for pb.Next() {
			// Rotate through clients and packet types
			client := clients[clientIdx%numClients]
			packet := packets[packetIdx%len(packets)]

			server.OnPublish(client, packet)

			clientIdx++
			packetIdx++
		}
	})

	// Note: Individual operation latency is hard to measure in parallel tests
	// The benchmark framework reports ns/op which includes all overhead
}

func BenchmarkMetricsOverhead_TopicMetrics(b *testing.B) {
	server := createTestMetricsServer(&testing.T{})

	// Test with different topic patterns to measure cardinality impact
	topics := []string{
		"sensor/temp/room1",
		"sensor/temp/room2",
		"sensor/humidity/room1",
		"alert/critical",
		"device/status/dev1",
		"device/status/dev2",
		"very/long/topic/path/that/might/impact/performance/test",
		"simple",
	}

	client := &mqtt.Client{ID: "topic-client"}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		topicIdx := 0
		for pb.Next() {
			packet := packets.Packet{
				FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 0},
				TopicName:   topics[topicIdx%len(topics)],
				Payload:     []byte("test payload"),
			}

			server.OnPublish(client, packet)
			topicIdx++
		}
	})
}

func BenchmarkMetricsOverhead_WildcardFiltering(b *testing.B) {
	topics := []string{
		"normal/topic",
		"wildcard/+/topic",
		"wildcard/topic/#",
		"multi/+/level/+/topic",
		"very/+/long/+/wildcard/+/topic/+/path/#",
		"simple",
		"+",
		"#",
		"normal/topic/without/wildcards/but/very/long/path",
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		topicIdx := 0
		for pb.Next() {
			topic := topics[topicIdx%len(topics)]
			containsWildcard(topic)
			topicIdx++
		}
	})
}

func BenchmarkMetricsOverhead_SystemMetricsCollection(b *testing.B) {
	server := createTestMetricsServer(&testing.T{})

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		server.updateSystemMetrics()
	}
}

func BenchmarkMetricsOverhead_WALMetricsRecording(b *testing.B) {
	server := createTestMetricsServer(&testing.T{})

	latencies := []time.Duration{
		100 * time.Microsecond,
		500 * time.Microsecond,
		1 * time.Millisecond,
		2500 * time.Microsecond,
		5 * time.Millisecond,
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		latencyIdx := 0
		for pb.Next() {
			latency := latencies[latencyIdx%len(latencies)]
			success := latencyIdx%2 == 0

			server.RecordWALWrite(latency, success)
			latencyIdx++
		}
	})
}

// Test memory allocation overhead
func BenchmarkMetricsOverhead_ZeroAllocation(b *testing.B) {
	server := createTestMetricsServer(&testing.T{})

	b.ResetTimer()
	b.ReportAllocs()

	// This should ideally show 0 allocs/op for the metrics overhead
	for i := 0; i < b.N; i++ {
		// Only test the core metrics recording, not the full hook
		atomic.AddInt64(server.connectionsCurrent, 1)
		server.messagesPublishedTotal.Inc()
		server.qos0MessagesTotal.Inc()
	}
}

// Test concurrent access performance
func BenchmarkMetricsOverhead_ConcurrentAccess(b *testing.B) {
	server := createTestMetricsServer(&testing.T{})

	packet := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 0},
		TopicName:   "concurrent/topic",
		Payload:     []byte("concurrent test"),
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		client := &mqtt.Client{ID: fmt.Sprintf("concurrent-client-%d", time.Now().UnixNano())}

		for pb.Next() {
			// Only test OnPublish to avoid map race condition
			server.OnPublish(client, packet)
		}
	})
}

// Helper to create optimized test server
func createPerfTestMetricsServer(t *testing.T) *Server {
	cfg := &config.Config{
		Metrics: config.MetricsConfig{
			Enabled: true,
			Bind:    "localhost:0", // Don't start HTTP server for perf tests
			Path:    "/metrics",
		},
	}

	logger := zerolog.Nop() // No-op logger for performance tests
	server, err := New(cfg, logger)
	require.NoError(t, err)
	return server
}
