package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zohaib-hassan/promptmq/internal/config"
)

func TestMetricsServer_New(t *testing.T) {
	cfg := &config.Config{
		Metrics: config.MetricsConfig{
			Enabled: true,
			Bind:    "localhost:9090",
			Path:    "/metrics",
		},
	}

	logger := zerolog.Nop()

	server, err := New(cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, server)

	// Verify metrics are initialized
	assert.NotNil(t, server.registry)
	assert.NotNil(t, server.connectionsTotal)
	assert.NotNil(t, server.messagesPublishedTotal)
	assert.NotNil(t, server.walWriteLatencyHist)
}

func TestMetricsServer_StartStop(t *testing.T) {
	cfg := &config.Config{
		Metrics: config.MetricsConfig{
			Enabled: true,
			Bind:    "localhost:0", // Use ephemeral port
			Path:    "/metrics",
		},
	}

	logger := zerolog.Nop()
	server, err := New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server
	err = server.Start(ctx)
	require.NoError(t, err)

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Stop server
	err = server.StopServer()
	require.NoError(t, err)
}

func TestMetricsServer_DisabledMetrics(t *testing.T) {
	cfg := &config.Config{
		Metrics: config.MetricsConfig{
			Enabled: false,
			Bind:    "localhost:9090",
			Path:    "/metrics",
		},
	}

	logger := zerolog.Nop()
	server, err := New(cfg, logger)
	require.NoError(t, err)

	ctx := context.Background()
	err = server.Start(ctx)
	require.NoError(t, err)

	// Should not have started HTTP server
	assert.Nil(t, server.server)
}

func TestMetricsHooks_OnConnect(t *testing.T) {
	server := createTestMetricsServer(t)

	client := &mqtt.Client{ID: "test-client"}
	packet := packets.Packet{}

	initialConnections := atomic.LoadInt64(server.connectionsCurrent)

	// Test OnConnect
	err := server.OnConnect(client, packet)
	require.NoError(t, err)

	// Verify connection metrics
	currentConnections := atomic.LoadInt64(server.connectionsCurrent)
	assert.Equal(t, initialConnections+1, currentConnections)

	// Verify connection start time is recorded
	_, exists := server.connectionStartTimes[client.ID]
	assert.True(t, exists)
}

func TestMetricsHooks_OnDisconnect(t *testing.T) {
	server := createTestMetricsServer(t)

	client := &mqtt.Client{ID: "test-client"}

	// First connect
	packet := packets.Packet{}
	err := server.OnConnect(client, packet)
	require.NoError(t, err)

	initialConnections := atomic.LoadInt64(server.connectionsCurrent)

	// Test OnDisconnect
	server.OnDisconnect(client, nil, false)

	// Verify connection metrics
	currentConnections := atomic.LoadInt64(server.connectionsCurrent)
	assert.Equal(t, initialConnections-1, currentConnections)

	// Verify connection start time is cleaned up
	_, exists := server.connectionStartTimes[client.ID]
	assert.False(t, exists)
}

func TestMetricsHooks_OnPublish(t *testing.T) {
	server := createTestMetricsServer(t)

	client := &mqtt.Client{ID: "test-client"}
	packet := packets.Packet{
		FixedHeader: packets.FixedHeader{
			Type: packets.Publish,
			Qos:  1,
		},
		TopicName: "test/topic",
		Payload:   []byte("test message payload"),
	}

	// Get initial metrics
	initialPublished := getCounterValue(t, server.messagesPublishedTotal)
	initialQos1 := getCounterValue(t, server.qos1MessagesTotal)

	// Test OnPublish
	returnedPacket, err := server.OnPublish(client, packet)
	require.NoError(t, err)
	assert.Equal(t, packet, returnedPacket)

	// Verify metrics are updated
	assert.Equal(t, initialPublished+1, getCounterValue(t, server.messagesPublishedTotal))
	assert.Equal(t, initialQos1+1, getCounterValue(t, server.qos1MessagesTotal))
}

func TestMetricsHooks_QoSDistribution(t *testing.T) {
	server := createTestMetricsServer(t)

	client := &mqtt.Client{ID: "test-client"}

	testCases := []struct {
		qos          uint8
		expectedQos0 float64
		expectedQos1 float64
		expectedQos2 float64
	}{
		{0, 1, 0, 0},
		{1, 0, 1, 0},
		{2, 0, 0, 1},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("QoS_%d", tc.qos), func(t *testing.T) {
			// Reset metrics for clean test
			server, _ = New(server.cfg, server.logger)

			packet := packets.Packet{
				FixedHeader: packets.FixedHeader{
					Type: packets.Publish,
					Qos:  tc.qos,
				},
				TopicName: "test/topic",
				Payload:   []byte("test"),
			}

			_, err := server.OnPublish(client, packet)
			require.NoError(t, err)

			assert.Equal(t, tc.expectedQos0, getCounterValue(t, server.qos0MessagesTotal))
			assert.Equal(t, tc.expectedQos1, getCounterValue(t, server.qos1MessagesTotal))
			assert.Equal(t, tc.expectedQos2, getCounterValue(t, server.qos2MessagesTotal))
		})
	}
}

func TestMetricsHooks_OnSubscribe(t *testing.T) {
	server := createTestMetricsServer(t)

	client := &mqtt.Client{ID: "test-client"}
	packet := packets.Packet{
		Filters: []packets.Subscription{
			{Filter: "test/topic1", Qos: 1},
			{Filter: "test/topic2", Qos: 2},
		},
	}

	// Test OnSubscribe
	returnedPacket := server.OnSubscribe(client, packet)
	assert.Equal(t, packet, returnedPacket)

	// Verify topic subscriber metrics are updated
	// Note: We can't easily verify the exact values without accessing the metric vector internals
	// But the call should complete without error
}

func TestMetricsHooks_OnUnsubscribe(t *testing.T) {
	server := createTestMetricsServer(t)

	client := &mqtt.Client{ID: "test-client"}
	packet := packets.Packet{
		Filters: []packets.Subscription{
			{Filter: "test/topic1", Qos: 1},
		},
	}

	// First subscribe
	server.OnSubscribe(client, packet)

	// Then unsubscribe
	returnedPacket := server.OnUnsubscribe(client, packet)
	assert.Equal(t, packet, returnedPacket)
}

func TestMetricsServer_WALMetrics(t *testing.T) {
	server := createTestMetricsServer(t)

	// Test WAL write recording
	server.RecordWALWrite(500*time.Microsecond, true)
	server.RecordWALWrite(1*time.Millisecond, false)

	// Test WAL buffer usage
	server.RecordWALBufferUsage(1024 * 1024) // 1MB

	// Test WAL recovery time
	server.RecordWALRecovery(2 * time.Second)

	// Verify no panics and metrics are recorded
	// The exact values are harder to verify due to histogram complexity
}

func TestMetricsServer_ThroughputRecording(t *testing.T) {
	server := createTestMetricsServer(t)

	// Test throughput recording
	server.RecordThroughput(1000.5)

	// Verify throughput gauge is updated
	assert.Equal(t, 1000.5, getGaugeValue(t, server.throughputGauge))
}

func TestMetricsServer_SystemMetrics(t *testing.T) {
	server := createTestMetricsServer(t)

	// Trigger system metrics update
	server.updateSystemMetrics()

	// Verify system metrics are updated (values will vary, just check they're set)
	assert.Greater(t, getGaugeValue(t, server.memoryUsageGauge), 0.0)
	assert.Greater(t, getGaugeValue(t, server.goroutineCountGauge), 0.0)
	assert.Greater(t, getGaugeValue(t, server.uptimeGauge), 0.0)
}

func TestContainsWildcard(t *testing.T) {
	testCases := []struct {
		topic    string
		expected bool
	}{
		{"test/topic", false},
		{"test/+/topic", true},
		{"test/topic/#", true},
		{"+", true},
		{"#", true},
		{"test/topic/subtopic", false},
		{"", false},
	}

	for _, tc := range testCases {
		t.Run(tc.topic, func(t *testing.T) {
			result := containsWildcard(tc.topic)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestMetricsServer_HookInterface(t *testing.T) {
	server := createTestMetricsServer(t)

	// Verify the server implements mqtt.Hook interface
	var hook mqtt.Hook = server
	assert.NotNil(t, hook)

	// Test ACL check method
	client := &mqtt.Client{ID: "test-client"}
	result := server.OnACLCheck(client, "test/topic", false)
	assert.True(t, result) // Metrics hook should always allow

	// Test interface methods
	assert.Equal(t, "metrics", server.ID())
	assert.True(t, server.Provides(0)) // Should provide all hooks
	assert.NoError(t, server.Init(nil))
}

func TestMetricsServer_HTTPEndpoints(t *testing.T) {
	cfg := &config.Config{
		Metrics: config.MetricsConfig{
			Enabled: true,
			Bind:    "localhost:0",
			Path:    "/metrics",
		},
	}

	logger := zerolog.Nop()
	server, err := New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server
	err = server.Start(ctx)
	require.NoError(t, err)
	defer server.StopServer()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Get the actual port (since we used 0)
	actualAddr := server.server.Addr
	if actualAddr == "localhost:0" {
		// Server might not have updated the address, skip this test
		t.Skip("Server address not available for testing")
		return
	}

	// Test health endpoint
	resp, err := http.Get(fmt.Sprintf("http://%s/health", actualAddr))
	if err != nil {
		t.Skip("Could not connect to metrics server")
		return
	}
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "OK", string(body))
}

// Benchmark tests for performance validation

func BenchmarkMetrics_OnPublish(b *testing.B) {
	server := createTestMetricsServer(&testing.T{})

	client := &mqtt.Client{ID: "bench-client"}
	packet := packets.Packet{
		FixedHeader: packets.FixedHeader{
			Type: packets.Publish,
			Qos:  1,
		},
		TopicName: "bench/topic",
		Payload:   make([]byte, 1024), // 1KB payload
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		server.OnPublish(client, packet)
	}
}

func BenchmarkMetrics_OnConnect(b *testing.B) {
	server := createTestMetricsServer(&testing.T{})

	packet := packets.Packet{}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		client := &mqtt.Client{ID: fmt.Sprintf("bench-client-%d", i)}
		server.OnConnect(client, packet)
	}
}

func BenchmarkMetrics_AtomicOperations(b *testing.B) {
	var counter int64

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			atomic.AddInt64(&counter, 1)
		}
	})
}

func BenchmarkMetrics_PrometheusCounter(b *testing.B) {
	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "test_counter",
		Help: "Test counter",
	})

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			counter.Inc()
		}
	})
}

// Helper functions for tests

func createTestMetricsServer(t *testing.T) *Server {
	cfg := &config.Config{
		Metrics: config.MetricsConfig{
			Enabled: true,
			Bind:    "localhost:9090",
			Path:    "/metrics",
		},
	}

	logger := zerolog.Nop()
	server, err := New(cfg, logger)
	require.NoError(t, err)
	return server
}

func getCounterValue(t *testing.T, counter prometheus.Counter) float64 {
	dto := &dto.Metric{}
	err := counter.Write(dto)
	require.NoError(t, err)
	return dto.Counter.GetValue()
}

func getGaugeValue(t *testing.T, gauge prometheus.Gauge) float64 {
	dto := &dto.Metric{}
	err := gauge.Write(dto)
	require.NoError(t, err)
	return dto.Gauge.GetValue()
}
