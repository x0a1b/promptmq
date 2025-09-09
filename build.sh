#!/bin/bash

# PromptMQ Build Script
# High-performance MQTT broker with enterprise-grade features

set -e  # Exit on any error

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
APP_NAME="promptmq"
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")

# Build flags
LDFLAGS="-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.Commit=${COMMIT} -s -w"
BUILD_DIR="./bin"
COVERAGE_DIR="./coverage"

print_header() {
    echo -e "${BLUE}"
    echo "=================================================="
    echo "        PromptMQ Build System"
    echo "  High-Performance MQTT Broker"
    echo "=================================================="
    echo -e "${NC}"
    echo "Version: ${VERSION}"
    echo "Build Time: ${BUILD_TIME}"
    echo "Commit: ${COMMIT}"
    echo ""
}

print_step() {
    echo -e "${YELLOW}▶ $1${NC}"
}

print_success() {
    echo -e "${GREEN}✓ $1${NC}"
}

print_error() {
    echo -e "${RED}✗ $1${NC}"
}

# Check Go version
check_go_version() {
    print_step "Checking Go version..."
    if ! command -v go &> /dev/null; then
        print_error "Go is not installed"
        exit 1
    fi
    
    GO_VERSION=$(go version | cut -d' ' -f3 | sed 's/go//')
    MIN_VERSION="1.21"
    
    if [ "$(printf '%s\n' "$MIN_VERSION" "$GO_VERSION" | sort -V | head -n1)" != "$MIN_VERSION" ]; then
        print_error "Go version $GO_VERSION is too old. Minimum required: $MIN_VERSION"
        exit 1
    fi
    
    print_success "Go version $GO_VERSION is compatible"
}

# Clean previous builds
clean() {
    print_step "Cleaning previous builds..."
    rm -rf "$BUILD_DIR"
    rm -rf "$COVERAGE_DIR"
    go clean -cache
    go clean -modcache 2>/dev/null || true
    print_success "Clean completed"
}

# Download dependencies
deps() {
    print_step "Downloading dependencies..."
    go mod download
    go mod tidy
    go mod verify
    print_success "Dependencies updated"
}

# Run tests
test() {
    print_step "Running tests..."
    mkdir -p "$COVERAGE_DIR"
    
    # Unit tests with coverage
    go test -v -race -coverprofile="$COVERAGE_DIR/coverage.out" ./...
    
    # Generate coverage report
    go tool cover -html="$COVERAGE_DIR/coverage.out" -o "$COVERAGE_DIR/coverage.html"
    
    # Coverage summary
    COVERAGE=$(go tool cover -func="$COVERAGE_DIR/coverage.out" | grep total | awk '{print $3}')
    print_success "Tests completed - Coverage: $COVERAGE"
}

# Run benchmarks
benchmark() {
    print_step "Running benchmarks..."
    echo "Storage Performance Benchmarks:"
    go test -bench=BenchmarkStorage -benchmem -run=^$ ./internal/storage/...
    
    echo ""
    echo "Durability Benchmarks:"
    go test -bench=BenchmarkDurability -benchmem -run=^$ ./internal/storage/...
    
    echo ""
    echo "WAL Performance Benchmarks:"
    go test -bench=BenchmarkWAL -benchmem -run=^$ ./internal/storage/...
    
    print_success "Benchmarks completed"
}

# Lint code
lint() {
    print_step "Running linters..."
    
    # Check if golangci-lint is installed
    if ! command -v golangci-lint &> /dev/null; then
        print_step "Installing golangci-lint..."
        go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
    fi
    
    golangci-lint run
    print_success "Linting completed"
}

# Build binary
build() {
    print_step "Building $APP_NAME..."
    mkdir -p "$BUILD_DIR"
    
    # Build for current platform
    CGO_ENABLED=0 go build -ldflags "$LDFLAGS" -o "$BUILD_DIR/$APP_NAME" .
    
    # Make executable
    chmod +x "$BUILD_DIR/$APP_NAME"
    
    # Show binary info
    echo ""
    echo "Binary Information:"
    ls -lh "$BUILD_DIR/$APP_NAME"
    
    print_success "Build completed: $BUILD_DIR/$APP_NAME"
}

# Build for multiple platforms
build_all() {
    print_step "Building for multiple platforms..."
    mkdir -p "$BUILD_DIR"
    
    declare -a platforms=(
        "linux/amd64"
        "linux/arm64"
        "darwin/amd64"
        "darwin/arm64"
        "windows/amd64"
    )
    
    for platform in "${platforms[@]}"; do
        GOOS=${platform%/*}
        GOARCH=${platform#*/}
        output_name="$BUILD_DIR/${APP_NAME}-${GOOS}-${GOARCH}"
        
        if [ $GOOS = "windows" ]; then
            output_name+='.exe'
        fi
        
        echo "Building for $GOOS/$GOARCH..."
        CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build \
            -ldflags "$LDFLAGS" \
            -o "$output_name" \
            .
        
        # Compress binary
        if command -v upx &> /dev/null; then
            upx --best --lzma "$output_name" 2>/dev/null || true
        fi
    done
    
    echo ""
    echo "Built binaries:"
    ls -lh "$BUILD_DIR"
    print_success "Multi-platform build completed"
}

# Install binary
install() {
    print_step "Installing $APP_NAME..."
    
    if [ ! -f "$BUILD_DIR/$APP_NAME" ]; then
        print_error "Binary not found. Run 'build' first."
        exit 1
    fi
    
    sudo cp "$BUILD_DIR/$APP_NAME" /usr/local/bin/
    print_success "Installed to /usr/local/bin/$APP_NAME"
}

# Create release package
package() {
    print_step "Creating release package..."
    
    if [ ! -d "$BUILD_DIR" ]; then
        print_error "No build directory found. Run 'build' or 'build-all' first."
        exit 1
    fi
    
    PACKAGE_DIR="$BUILD_DIR/release"
    mkdir -p "$PACKAGE_DIR"
    
    # Copy binaries
    cp -r "$BUILD_DIR"/* "$PACKAGE_DIR/" 2>/dev/null || true
    rm -rf "$PACKAGE_DIR/release" 2>/dev/null || true
    
    # Copy documentation
    cp README.md LICENSE "$PACKAGE_DIR/"
    
    # Copy example configs
    mkdir -p "$PACKAGE_DIR/examples"
    cp examples/*.yaml "$PACKAGE_DIR/examples/" 2>/dev/null || true
    
    # Create archive
    cd "$BUILD_DIR"
    tar -czf "promptmq-${VERSION}.tar.gz" release/
    cd - > /dev/null
    
    print_success "Release package created: $BUILD_DIR/promptmq-${VERSION}.tar.gz"
}

# Development mode - watch for changes
dev() {
    print_step "Starting development mode..."
    
    if ! command -v air &> /dev/null; then
        print_step "Installing air for hot reload..."
        go install github.com/cosmtrek/air@latest
    fi
    
    air -c .air.toml
}

# Docker build
docker() {
    print_step "Building Docker image..."
    docker build -t "promptmq:${VERSION}" -t "promptmq:latest" .
    print_success "Docker image built: promptmq:${VERSION}"
}

# Show help
help() {
    echo -e "${BLUE}PromptMQ Build Script${NC}"
    echo ""
    echo "Usage: $0 [command]"
    echo ""
    echo "Commands:"
    echo "  clean         Clean build artifacts and caches"
    echo "  deps          Download and update dependencies"
    echo "  test          Run tests with coverage report"
    echo "  benchmark     Run performance benchmarks"
    echo "  lint          Run code linters"
    echo "  build         Build binary for current platform"
    echo "  build-all     Build binaries for all platforms"
    echo "  install       Install binary to /usr/local/bin"
    echo "  package       Create release package"
    echo "  dev           Start development mode with hot reload"
    echo "  docker        Build Docker image"
    echo "  ci            Run full CI pipeline (clean, deps, lint, test, build)"
    echo "  release       Full release build (clean, deps, lint, test, build-all, package)"
    echo "  help          Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0 build               # Quick build for development"
    echo "  $0 ci                  # Full CI pipeline"
    echo "  $0 release             # Production release"
    echo ""
}

# CI pipeline
ci() {
    print_header
    clean
    deps
    lint
    test
    build
    print_success "CI pipeline completed successfully!"
}

# Release pipeline
release() {
    print_header
    clean
    deps
    lint
    test
    build_all
    package
    print_success "Release pipeline completed successfully!"
}

# Main execution
main() {
    case "${1:-help}" in
        clean)
            clean
            ;;
        deps)
            deps
            ;;
        test)
            test
            ;;
        benchmark)
            benchmark
            ;;
        lint)
            lint
            ;;
        build)
            check_go_version
            build
            ;;
        build-all)
            check_go_version
            build_all
            ;;
        install)
            install
            ;;
        package)
            package
            ;;
        dev)
            dev
            ;;
        docker)
            docker
            ;;
        ci)
            ci
            ;;
        release)
            release
            ;;
        help|--help|-h)
            help
            ;;
        *)
            print_error "Unknown command: $1"
            echo ""
            help
            exit 1
            ;;
    esac
}

# Execute main function
main "$@"