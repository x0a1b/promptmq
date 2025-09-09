package metrics

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"

	"github.com/zohaib-hassan/promptmq/internal/config"
)

// Server implements both HTTP metrics server and Mochi MQTT Hook interface
type Server struct {
	cfg    *config.Config
	logger zerolog.Logger
	server *http.Server

	// Prometheus registry and metrics
	registry *prometheus.Registry

	// MQTT Connection metrics (atomic counters for zero-allocation updates)
	connectionsCurrent     *int64
	connectionsTotal       prometheus.Counter
	disconnectionsTotal    prometheus.Counter
	connectionDurationHist prometheus.Histogram

	// Message metrics
	messagesPublishedTotal prometheus.Counter
	messagesReceivedTotal  prometheus.Counter
	bytesPublishedTotal    prometheus.Counter
	bytesReceivedTotal     prometheus.Counter
	messageLatencyHist     prometheus.Histogram
	messageSizeHist        prometheus.Histogram

	// QoS distribution metrics
	qos0MessagesTotal prometheus.Counter
	qos1MessagesTotal prometheus.Counter
	qos2MessagesTotal prometheus.Counter

	// Topic metrics
	topicMessageCounts   *prometheus.CounterVec
	topicByteCounts      *prometheus.CounterVec
	topicSubscriberGauge *prometheus.GaugeVec

	// Storage/WAL metrics
	walWriteLatencyHist prometheus.Histogram
	walWriteErrorsTotal prometheus.Counter
	walBufferUsageGauge prometheus.Gauge
	walRecoveryTimeHist prometheus.Histogram
	persistenceOpsTotal *prometheus.CounterVec

	// System metrics
	memoryUsageGauge    prometheus.Gauge
	goroutineCountGauge prometheus.Gauge
	gcDurationHist      prometheus.Histogram
	uptimeGauge         prometheus.Gauge

	// Performance metrics
	throughputGauge       prometheus.Gauge
	processingLatencyHist prometheus.Histogram

	// Internal state
	startTime            time.Time
	lastGCPause          uint64
	connectionStartTimes map[string]time.Time // Client ID -> connection start time
}

// Ensure Server implements mqtt.Hook interface at compile time
var _ mqtt.Hook = (*Server)(nil)

// New creates a new metrics server instance
func New(cfg *config.Config, logger zerolog.Logger) (*Server, error) {
	// Create custom Prometheus registry to avoid global state conflicts
	registry := prometheus.NewRegistry()

	// Initialize atomic counter for current connections
	connectionsCurrent := int64(0)

	s := &Server{
		cfg:                  cfg,
		logger:               logger.With().Str("component", "metrics").Logger(),
		registry:             registry,
		connectionsCurrent:   &connectionsCurrent,
		startTime:            time.Now(),
		connectionStartTimes: make(map[string]time.Time),
	}

	// Initialize all Prometheus metrics
	if err := s.initMetrics(); err != nil {
		return nil, fmt.Errorf("failed to initialize metrics: %w", err)
	}

	return s, nil
}

// initMetrics initializes all Prometheus metrics with optimal bucket configurations
func (s *Server) initMetrics() error {
	var err error

	// Connection metrics
	s.connectionsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "promptmq_connections_total",
		Help: "Total number of client connections established",
	})

	s.disconnectionsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "promptmq_disconnections_total",
		Help: "Total number of client disconnections",
	})

	// Optimized buckets for connection duration (seconds)
	s.connectionDurationHist = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "promptmq_connection_duration_seconds",
		Help:    "Duration of client connections in seconds",
		Buckets: []float64{1, 10, 30, 60, 300, 600, 1800, 3600, 7200, 14400, 28800}, // 1s to 8h
	})

	// Message metrics
	s.messagesPublishedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "promptmq_messages_published_total",
		Help: "Total number of messages published",
	})

	s.messagesReceivedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "promptmq_messages_received_total",
		Help: "Total number of messages received",
	})

	s.bytesPublishedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "promptmq_bytes_published_total",
		Help: "Total bytes of messages published",
	})

	s.bytesReceivedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "promptmq_bytes_received_total",
		Help: "Total bytes of messages received",
	})

	// Optimized buckets for message latency (microseconds for <10ms latency requirement)
	s.messageLatencyHist = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "promptmq_message_processing_latency_microseconds",
		Help:    "Message processing latency in microseconds",
		Buckets: []float64{100, 500, 1000, 2500, 5000, 10000, 25000, 50000, 100000}, // 0.1ms to 100ms
	})

	// Message size buckets (bytes)
	s.messageSizeHist = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "promptmq_message_size_bytes",
		Help:    "Size of MQTT messages in bytes",
		Buckets: []float64{64, 256, 1024, 4096, 16384, 65536, 262144, 1048576}, // 64B to 1MB
	})

	// QoS metrics
	s.qos0MessagesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "promptmq_qos0_messages_total",
		Help: "Total QoS 0 messages",
	})

	s.qos1MessagesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "promptmq_qos1_messages_total",
		Help: "Total QoS 1 messages",
	})

	s.qos2MessagesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "promptmq_qos2_messages_total",
		Help: "Total QoS 2 messages",
	})

	// Topic metrics with limited cardinality for performance
	s.topicMessageCounts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "promptmq_topic_messages_total",
			Help: "Total messages per topic",
		}, []string{"topic"},
	)

	s.topicByteCounts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "promptmq_topic_bytes_total",
			Help: "Total bytes per topic",
		}, []string{"topic"},
	)

	s.topicSubscriberGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "promptmq_topic_subscribers",
			Help: "Current number of subscribers per topic",
		}, []string{"topic"},
	)

	// Storage/WAL metrics with optimized latency buckets
	s.walWriteLatencyHist = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "promptmq_wal_write_latency_microseconds",
		Help:    "WAL write latency in microseconds",
		Buckets: []float64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 25000}, // 50µs to 25ms
	})

	s.walWriteErrorsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "promptmq_wal_write_errors_total",
		Help: "Total WAL write errors",
	})

	s.walBufferUsageGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "promptmq_wal_buffer_usage_bytes",
		Help: "Current WAL buffer usage in bytes",
	})

	s.walRecoveryTimeHist = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "promptmq_wal_recovery_duration_seconds",
		Help:    "WAL recovery duration in seconds",
		Buckets: []float64{0.1, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0, 60.0}, // 100ms to 1min
	})

	s.persistenceOpsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "promptmq_persistence_operations_total",
			Help: "Total persistence operations by type",
		}, []string{"operation", "status"}, // operation: write/read/delete, status: success/error
	)

	// System metrics
	s.memoryUsageGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "promptmq_memory_usage_bytes",
		Help: "Current memory usage in bytes",
	})

	s.goroutineCountGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "promptmq_goroutines_total",
		Help: "Current number of goroutines",
	})

	s.gcDurationHist = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "promptmq_gc_duration_seconds",
		Help:    "Garbage collection duration in seconds",
		Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5}, // 0.1ms to 500ms
	})

	s.uptimeGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "promptmq_uptime_seconds",
		Help: "Server uptime in seconds",
	})

	// Performance metrics
	s.throughputGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "promptmq_throughput_messages_per_second",
		Help: "Current message throughput (messages per second)",
	})

	s.processingLatencyHist = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "promptmq_processing_latency_microseconds",
		Help:    "End-to-end message processing latency in microseconds",
		Buckets: []float64{100, 500, 1000, 2500, 5000, 10000, 25000, 50000, 100000}, // 0.1ms to 100ms
	})

	// Register all metrics with the custom registry
	metrics := []prometheus.Collector{
		s.connectionsTotal,
		s.disconnectionsTotal,
		s.connectionDurationHist,
		s.messagesPublishedTotal,
		s.messagesReceivedTotal,
		s.bytesPublishedTotal,
		s.bytesReceivedTotal,
		s.messageLatencyHist,
		s.messageSizeHist,
		s.qos0MessagesTotal,
		s.qos1MessagesTotal,
		s.qos2MessagesTotal,
		s.topicMessageCounts,
		s.topicByteCounts,
		s.topicSubscriberGauge,
		s.walWriteLatencyHist,
		s.walWriteErrorsTotal,
		s.walBufferUsageGauge,
		s.walRecoveryTimeHist,
		s.persistenceOpsTotal,
		s.memoryUsageGauge,
		s.goroutineCountGauge,
		s.gcDurationHist,
		s.uptimeGauge,
		s.throughputGauge,
		s.processingLatencyHist,
	}

	for _, metric := range metrics {
		if err = s.registry.Register(metric); err != nil {
			return fmt.Errorf("failed to register metric: %w", err)
		}
	}

	// Add custom collector for current connections (atomic counter)
	currentConnGauge := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "promptmq_connections_current",
		Help: "Current number of active client connections",
	}, func() float64 {
		return float64(atomic.LoadInt64(s.connectionsCurrent))
	})

	if err = s.registry.Register(currentConnGauge); err != nil {
		return fmt.Errorf("failed to register current connections gauge: %w", err)
	}

	return nil
}

// Start starts the HTTP metrics server
func (s *Server) Start(ctx context.Context) error {
	if !s.cfg.Metrics.Enabled {
		s.logger.Info().Msg("Metrics disabled, skipping metrics server start")
		return nil
	}

	// Create HTTP server with metrics endpoint
	mux := http.NewServeMux()
	mux.Handle(s.cfg.Metrics.Path, promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
		Registry:          s.registry,
	}))

	// Add health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	s.server = &http.Server{
		Addr:         s.cfg.Metrics.Bind,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start system metrics collection in background
	go s.collectSystemMetrics(ctx)

	// Start HTTP server in goroutine
	go func() {
		s.logger.Info().
			Str("bind", s.cfg.Metrics.Bind).
			Str("path", s.cfg.Metrics.Path).
			Msg("Starting metrics HTTP server")

		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error().Err(err).Msg("Metrics server error")
		}
	}()

	return nil
}

// StopServer gracefully shuts down the HTTP metrics server
func (s *Server) StopServer() error {
	if s.server == nil {
		return nil
	}

	s.logger.Info().Msg("Stopping metrics server")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return s.server.Shutdown(ctx)
}

// collectSystemMetrics periodically collects system-level metrics
func (s *Server) collectSystemMetrics(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second) // Collect every 5 seconds
	defer ticker.Stop()

	var lastGCStats runtime.MemStats
	runtime.ReadMemStats(&lastGCStats)
	s.lastGCPause = lastGCStats.PauseTotalNs

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.updateSystemMetrics()
		}
	}
}

// updateSystemMetrics updates system-level metrics (called periodically)
func (s *Server) updateSystemMetrics() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Memory usage
	s.memoryUsageGauge.Set(float64(m.Alloc))

	// Goroutine count
	s.goroutineCountGauge.Set(float64(runtime.NumGoroutine()))

	// GC duration (convert nanoseconds to seconds)
	currentGCPause := m.PauseTotalNs
	if currentGCPause > s.lastGCPause {
		gcDuration := float64(currentGCPause-s.lastGCPause) / float64(time.Second)
		s.gcDurationHist.Observe(gcDuration)
		s.lastGCPause = currentGCPause
	}

	// Uptime
	uptime := time.Since(s.startTime).Seconds()
	s.uptimeGauge.Set(uptime)
}

// RecordWALWrite records WAL write metrics (called by storage layer)
func (s *Server) RecordWALWrite(latency time.Duration, success bool) {
	// Convert to microseconds for precision
	latencyMicros := float64(latency.Nanoseconds()) / 1000.0
	s.walWriteLatencyHist.Observe(latencyMicros)

	if success {
		s.persistenceOpsTotal.WithLabelValues("write", "success").Inc()
	} else {
		s.persistenceOpsTotal.WithLabelValues("write", "error").Inc()
		s.walWriteErrorsTotal.Inc()
	}
}

// RecordWALBufferUsage records current WAL buffer usage
func (s *Server) RecordWALBufferUsage(bytes int64) {
	s.walBufferUsageGauge.Set(float64(bytes))
}

// RecordWALRecovery records WAL recovery time
func (s *Server) RecordWALRecovery(duration time.Duration) {
	s.walRecoveryTimeHist.Observe(duration.Seconds())
}

// RecordThroughput records current message throughput
func (s *Server) RecordThroughput(messagesPerSecond float64) {
	s.throughputGauge.Set(messagesPerSecond)
}

// GetMetricsForTesting returns metrics for testing purposes
func (s *Server) GetMetricsForTesting() map[string]interface{} {
	return map[string]interface{}{
		"connections_current": atomic.LoadInt64(s.connectionsCurrent),
		"connections_total":   s.connectionsTotal,
		"messages_published":  s.messagesPublishedTotal,
		"messages_received":   s.messagesReceivedTotal,
	}
}
