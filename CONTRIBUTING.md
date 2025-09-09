# Contributing to PromptMQ

We welcome contributions to PromptMQ! This document provides guidelines for contributing to the project.

## 🚀 Getting Started

### Prerequisites
- Go 1.21 or higher
- Git
- Make (optional, for convenience commands)

### Development Setup

1. **Fork and clone the repository**
```bash
git clone https://github.com/your-username/promptmq.git
cd promptmq
```

2. **Install dependencies**
```bash
./build.sh deps
```

3. **Run tests to ensure everything works**
```bash
./build.sh test
```

4. **Start development server**
```bash
./build.sh dev
```

## 🔧 Development Workflow

### Code Style and Standards

- **Go Code**: Follow standard Go conventions and use `gofmt`
- **Comments**: Use clear, concise comments for public APIs
- **Error Handling**: Always handle errors appropriately
- **Testing**: Write tests for new features and bug fixes
- **Documentation**: Update documentation for user-facing changes

### Running Tests

```bash
# Run all tests
./build.sh test

# Run specific package tests
go test ./internal/storage -v

# Run benchmarks
./build.sh benchmark

# Run with race detection
go test -race ./...
```

### Code Quality

```bash
# Run linter
./build.sh lint

# Format code
go fmt ./...

# Run static analysis
go vet ./...
```

## 📝 Contribution Types

### Bug Reports

When reporting bugs, please include:
- Go version and OS
- PromptMQ version
- Configuration used
- Steps to reproduce
- Expected vs actual behavior
- Log output (with debug level if possible)

**Use our bug report template:**
```markdown
## Bug Report

**PromptMQ Version:** v1.0.0
**Go Version:** go1.22.0
**OS:** Linux/macOS/Windows

### Description
Brief description of the issue.

### Steps to Reproduce
1. Step one
2. Step two
3. Step three

### Expected Behavior
What should happen.

### Actual Behavior
What actually happens.

### Configuration
```yaml
# Include relevant config
```

### Logs
```
Include relevant log output
```
```

### Feature Requests

For new features:
- Explain the use case and benefits
- Provide examples of how it would work
- Consider performance and compatibility implications
- Discuss API design if applicable

### Performance Improvements

For performance contributions:
- Include benchmark results showing improvement
- Ensure no functionality regressions
- Consider memory usage implications
- Test under realistic workloads

## 🔀 Pull Request Process

### Before Submitting

1. **Create an issue** first for major changes
2. **Fork the repository** and create a feature branch
3. **Write tests** for your changes
4. **Update documentation** if needed
5. **Run the full test suite**
6. **Check code quality** with linter

### PR Guidelines

1. **Branch naming**: `feature/description` or `fix/description`
2. **Commit messages**: Use conventional commits format
   ```
   feat: add batch sync mode for WAL
   fix: resolve memory leak in compaction
   docs: update configuration examples
   test: add crash recovery test cases
   ```
3. **PR title**: Clear, descriptive summary
4. **PR description**: Include:
   - What changes were made
   - Why the changes were needed
   - How to test the changes
   - Any breaking changes
   - Related issues

### Example PR Template

```markdown
## Description
Brief description of changes made.

## Type of Change
- [ ] Bug fix (non-breaking change that fixes an issue)
- [ ] New feature (non-breaking change that adds functionality)
- [ ] Breaking change (fix or feature that causes existing functionality to change)
- [ ] Documentation update

## Testing
- [ ] Tests pass locally
- [ ] New tests added for changes
- [ ] Benchmarks run (if performance-related)
- [ ] Manual testing completed

## Checklist
- [ ] Code follows project style guidelines
- [ ] Self-review completed
- [ ] Documentation updated
- [ ] Commits are properly formatted
```

### Review Process

1. **Automated checks** must pass (CI, tests, linting)
2. **Code review** by maintainers
3. **Testing** in various environments
4. **Documentation review** for user-facing changes
5. **Merge** after approval

## 🏗️ Project Structure

```
promptmq/
├── cmd/                    # CLI commands and main application
├── internal/              # Private application code
│   ├── broker/           # MQTT broker core
│   ├── config/           # Configuration management
│   ├── storage/          # WAL + BadgerDB storage
│   ├── metrics/          # Monitoring and metrics
│   └── cluster/          # Clustering support
├── examples/             # Configuration examples
├── docs/                 # Documentation
├── .github/              # GitHub workflows and templates
└── test/                 # Integration tests
```

## 🧪 Testing Guidelines

### Unit Tests
- Test all public functions
- Test error conditions
- Use table-driven tests where appropriate
- Mock external dependencies

### Integration Tests  
- Test complete workflows
- Test configuration loading
- Test MQTT protocol compliance
- Test storage persistence

### Performance Tests
- Benchmark critical paths
- Test memory usage
- Test under load
- Compare against baselines

### Example Test Structure
```go
func TestStorageManager_PersistMessage(t *testing.T) {
    tests := []struct {
        name    string
        msg     *Message
        want    error
        wantErr bool
    }{
        {
            name: "valid message",
            msg:  &Message{ID: 1, Topic: "test", Payload: []byte("data")},
            want: nil,
            wantErr: false,
        },
        // Add more test cases
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Test implementation
        })
    }
}
```

## 📋 Coding Standards

### Go Best Practices
- Use meaningful variable names
- Keep functions small and focused  
- Handle errors explicitly
- Use interfaces for testability
- Avoid global variables
- Use context for cancellation

### Performance Considerations
- Minimize allocations in hot paths
- Use sync.Pool for object reuse
- Prefer composition over inheritance
- Use buffered channels appropriately
- Profile before optimizing

### Security Guidelines
- Validate all inputs
- Use secure defaults
- Don't log sensitive information
- Follow cryptographic best practices
- Regular dependency updates

## 🐛 Debugging

### Local Development
```bash
# Run with debug logging
go run ./cmd/promptmq --log-level debug

# Enable race detection
go run -race ./cmd/promptmq

# Profile performance
go tool pprof http://localhost:9090/debug/pprof/profile
```

### Common Issues
- **Port conflicts**: Check if ports 1883, 8080, 9090 are available
- **Permission errors**: Ensure write access to data directories
- **Memory issues**: Check buffer sizes and compaction settings
- **Performance**: Profile CPU and memory usage

## 📈 Performance Testing

### Benchmark Guidelines
```bash
# Run storage benchmarks
go test -bench=BenchmarkStorage ./internal/storage

# Memory profiling
go test -bench=. -memprofile=mem.prof ./internal/storage

# CPU profiling
go test -bench=. -cpuprofile=cpu.prof ./internal/storage
```

### Performance Targets
- **Throughput**: > 500K msg/sec (periodic sync)
- **Latency**: < 10ms P99
- **Memory**: < 256MB baseline usage
- **Recovery**: < 5 seconds for 1M messages

## 🤝 Community

### Communication
- **Issues**: For bug reports and feature requests
- **Discussions**: For questions and ideas
- **Discord**: Real-time chat (if available)
- **Email**: For security issues

### Code of Conduct
- Be respectful and inclusive
- Help others learn and contribute
- Focus on constructive feedback
- Assume positive intent

## 📄 License

By contributing to PromptMQ, you agree that your contributions will be licensed under the MIT License.

## 🙏 Recognition

Contributors are recognized in:
- Release notes
- Contributors file
- Documentation acknowledgments
- Project README

Thank you for contributing to PromptMQ! 🚀