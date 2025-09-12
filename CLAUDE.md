# PromptMQ Project Context for Claude

## Project Overview

PromptMQ is a **production-ready, enterprise-grade MQTT v5 broker** written in Go, designed for applications requiring extreme performance with **sub-microsecond latency** (130ns per message) and **fully configurable SQLite storage optimization**. The project has achieved **enterprise-scale performance** and is **ready for production deployment and open source publishing**.

## Rules to never break
- **Unit Tests**: 85%+ coverage across all packages
- **Integration Tests**: End-to-end MQTT client testing
- **Performance Tests**: Benchmark suites for all critical paths
- **Configuration Tests**: Validation of all SQLite parameters
- **Error Handling Tests**: Comprehensive error scenario coverage
- **Go Best Practices**: Follow standard Go conventions and idioms
- **Zero-Allocation Focus**: Optimize critical paths for minimal memory allocation
- **Comprehensive Testing**: All new code must have >90% test coverage
- **Performance First**: Any performance-critical code must have benchmarks
- **SQLite Optimization**: Configuration changes should be validated with performance tests
- **Sub-microsecond Latency**: Target <1µs for critical operations
- **Zero Allocations**: Critical paths must have 0 B/op, 0 allocs/op
- **High Throughput**: Support 1M+ msg/sec theoretical throughput
- **Memory Efficiency**: Configurable memory usage through SQLite tuning
- **Metrics Overhead**: Monitoring should add <500ns per operation
- **Cache Size**: Balance between memory usage and performance
- **Durability Modes**: Choose appropriate synchronous level for use case
- **Memory Mapping**: Optimize mmap-size based on available memory
- **Journal Mode**: Use WAL for best performance with durability
- **Timeout Configuration**: Set appropriate busy-timeout for concurrency
- Unit tests + integration tests + golden prompt tests: keep a repo of golden prompts + expected outputs; run them in CI and fail on drift beyond a threshold.
- Adversarial test suite: fuzz prompts, inject malicious instructions, try prompt-injection patterns and ensure the model obeys constraints.
- Human-in-the-loop review: require human review for higher-risk PRs (security, billing, infra, third-party API changes).
- Regression monitoring: track model quality over time (precision/recall for tasks with ground truth; human evals for open tasks).
