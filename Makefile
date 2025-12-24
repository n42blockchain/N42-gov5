BUILD_TIME := $(shell date +"%Y-%m-%d %H:%M:%S")
#GIT_COMMIT := $(shell git show -s --pretty=format:%h)
GO_VERSION := $(shell go version)
BUILD_PATH := ./build/bin/
APP_NAME := n42
APP_PATH := ./cmd/n42
SHELL := /bin/bash
GO = go
#LDFLAGS := -ldflags "-w -s -X github.com/n42blockchain/N42/version.BuildNumber=${GIT_COMMIT} -X 'github.com/n42blockchain/N42/version.BuildTime=${BUILD_TIME}' -X 'github.com/n42blockchain/N42/version.GoVersion=${GO_VERSION}'"


# Variables below for building on host OS, and are ignored for docker
#
# Pipe error below to /dev/null since Makefile structure kind of expects
# Go to be available, but with docker it's not strictly necessary
CGO_CFLAGS := $(shell $(GO) env CGO_CFLAGS 2>/dev/null) # don't lose default
CGO_CFLAGS += -DMDBX_FORCE_ASSERTIONS=0 # Enable MDBX's asserts by default in 'devel' branch and disable in releases
#CGO_CFLAGS += -DMDBX_DISABLE_VALIDATION=1 # This feature is not ready yet
#CGO_CFLAGS += -DMDBX_ENABLE_PROFGC=0 # Disabled by default, but may be useful for performance debugging
#CGO_CFLAGS += -DMDBX_ENABLE_PGOP_STAT=0 # Disabled by default, but may be useful for performance debugging
#CGO_CFLAGS += -DMDBX_ENV_CHECKPID=0 # Erigon doesn't do fork() syscall
CGO_CFLAGS += -O
CGO_CFLAGS += -D__BLST_PORTABLE__
CGO_CFLAGS += -Wno-unknown-warning-option -Wno-enum-int-mismatch -Wno-strict-prototypes
#CGO_CFLAGS += -Wno-error=strict-prototypes

GIT_COMMIT ?= $(shell git rev-list -1 HEAD)
GIT_BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD)
GIT_TAG    ?= $(shell git describe --tags '--match=v*' --dirty)
PACKAGE = github.com/n42blockchain/N42

BUILD_TAGS = nosqlite,noboltdb
GO_FLAGS += -trimpath -tags $(BUILD_TAGS) -buildvcs=false
GO_FLAGS += -ldflags  "-X ${PACKAGE}/params.GitCommit=${GIT_COMMIT} -X ${PACKAGE}/params.GitBranch=${GIT_BRANCH} -X ${PACKAGE}/params.GitTag=${GIT_TAG}"
GOBUILD = CGO_CFLAGS="$(CGO_CFLAGS)" go build -v $(GO_FLAGS)


# if using volume-mounting data dir, then must exist on host OS
DOCKER_UID ?= $(shell id -u)
DOCKER_GID ?= $(shell id -g)


# == mobiles
#OSFLAG=$(shell uname -sm)

ANDROID_SDK=$(ANDROID_HOME)
NDK_VERSION=25.2.9519653
NDK_HOME=$(ANDROID_SDK)/ndk/$(NDK_VERSION)
#ANDROID_SDK=/Users/mac/Library/Android/sdk
MOBILE_GO_FLAGS = -ldflags "-X ${PACKAGE}/cmd/evmsdk/common.VERSION=${GIT_COMMIT}"
MOBILE_PACKAGE= $(shell pwd)/cmd/evmsdk
BUILD_MOBILE_PATH = ./build/mobile/


# --build-arg UID=${DOCKER_UID} --build-arg GID=${DOCKER_GID}

## go-version:                        print and verify go version
go-version:
	@if [ $(shell go version | cut -c 16-17) -lt 18 ]; then \
		echo "minimum required Golang version is 1.18"; \
		exit 1 ;\
	fi
gen:
	@echo "Generate go code ..."
	go generate ./...
	@echo "Generate done!"
deps: go-version
	@echo "setup go deps..."
	go mod tidy
	@echo "deps done!"

n42: deps
	@echo "start build $(APP_NAME)..."
	#go build -v ${LDFLAGS} -o $(BUILD_PATH)$(APP_NAME)  ${APP_PATH}
	$(GOBUILD) -o $(BUILD_PATH)$(APP_NAME)  ${APP_PATH}
	@echo "Compile done!"

images:
	@echo "docker images build ..."
	DOCKER_BUILDKIT=1 docker build -t n42/n42:local .
	@echo "Compile done!"

up:
	@echo "docker compose up $(APP_NAME) ..."
	docker-compose  --project-name $(APP_NAME) up -d
	docker-compose  --project-name $(APP_NAME) logs -f
down:
	@echo "docker compose down $(APP_NAME) ..."
	docker-compose  --project-name $(APP_NAME) down
	docker volume ls -q | grep 'N42' | xargs -I % docker volume rm %
	@echo "done!"
stop:
	@echo "docker compose stop $(APP_NAME) ..."
	docker-compose  --project-name $(APP_NAME) stop
	@echo "done!"
start:
	@echo "docker compose stop $(APP_NAME) ..."
	docker-compose  --project-name $(APP_NAME) start
	docker-compose  --project-name $(APP_NAME) logs -f
clean:
	go clean
	@rm -rf  build

devtools:
	env GOBIN= go install github.com/fjl/gencodec@latest
	env GOBIN= go install github.com/golang/protobuf/protoc-gen-go@latest
	env GOBIN= go install github.com/prysmaticlabs/fastssz/sszgen@latest
	env GOBIN= go install github.com/prysmaticlabs/protoc-gen-go-cast@latest


PACKAGE_NAME          := github.com/n42blockchain/N42
GOLANG_CROSS_VERSION  ?= v1.20.7

.PHONY: release-docker
release-docker:
	@docker run \
		--rm \
		--privileged \
		-e CGO_ENABLED=1 \
		-e GITHUB_TOKEN \
		-e DOCKER_USERNAME \
		-e DOCKER_PASSWORD \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v `pwd`:/go/src/$(PACKAGE_NAME) \
		-w /go/src/$(PACKAGE_NAME) \
		ghcr.io/goreleaser/goreleaser-cross:${GOLANG_CROSS_VERSION} \
		--clean --skip-validate

		@docker image push --all-tags n42blockchain/n42


#== mobiles start
mobile: clean mobile-dir android ios

mobile-dir:
	#go get golang.org/x/mobile/bind/objc
	mkdir -p $(BUILD_MOBILE_PATH)/android
ios:
	GOOS=ios CGO_ENABLED=1 GOARCH=arm64 gomobile bind ${MOBILE_GO_FLAGS}  -o $(BUILD_MOBILE_PATH)/evmsdk.xcframework -target=ios/arm64  $(MOBILE_PACKAGE)
android:
	CGO_ENABLED=1 ANDROID_HOME=$(ANDROID_SDK) ANDROID_NDK_HOME=$(NDK_HOME) gomobile bind -x ${MOBILE_GO_FLAGS} -androidapi 23 -o $(BUILD_MOBILE_PATH)/android/evmsdk.aar -target=android/arm -v $(MOBILE_PACKAGE)
#   ANDROID_NDK_CC=$(NDK_HOME)/bin/arm-linux-androideabi-gcc NDK_CC=$(NDK_HOME)/bin/arm-linux-androideabi-gcc
# GOARCH=arm GOOS=android CGO_ENABLED=1
open-output:
	open ./mobile

#== mobiles end

.PHONY: build test test-short race-core fmt vet lint bench-smoke ci
.PHONY: race bench cover check install tidy help test-cover test-verbose
.PHONY: version version-bump version-minor version-major

# =============================================================================
# 核心目标 (Core Targets)
# =============================================================================

# 全仓编译（不触发 go mod tidy）
build: go-version
	@echo "==> go build ./..."
	$(GO) build $(GO_FLAGS) ./...

# 全仓测试（不触发 go mod tidy）
test: go-version
	@echo "==> go test ./..."
	$(GO) test ./...

# 更快的单测（可选）
test-short: go-version
	@echo "==> go test -short ./..."
	$(GO) test -short ./...

# 详细测试输出
test-verbose: go-version
	@echo "==> go test -v ./..."
	$(GO) test -v ./...

# =============================================================================
# Race 检测 (Race Detection)
# =============================================================================

# 核心包 race（可按需调整包名）
RACE_PKGS ?= ./internal/vm ./modules/state ./internal ./internal/sync
race-core: go-version
	@echo "==> go test -race $(RACE_PKGS)"
	$(GO) test -race $(RACE_PKGS)

# 全仓 race 检测（较慢，用于全面检查）
race: go-version
	@echo "==> go test -race ./..."
	$(GO) test -race -timeout 30m ./...

# =============================================================================
# 代码质量 (Code Quality)
# =============================================================================

# fmt/vet（轻量、强烈建议）
fmt:
	@echo "==> gofmt -w"
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

vet:
	@echo "==> go vet ./..."
	$(GO) vet ./...

# lint：如果没装 golangci-lint，就给出提示并退出 2（避免"假通过"）
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not found. Install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
		exit 2; \
	}
	golangci-lint run ./...

# 组合检查：fmt + vet + lint
check: fmt vet lint
	@echo "==> All checks passed!"

# =============================================================================
# 基准测试 (Benchmarks)
# =============================================================================

# bench-smoke：最小可重复基线（即便没有 Benchmark 也会正常通过）
BENCH_PKGS ?= ./modules/state ./internal/vm ./internal
bench-smoke: go-version
	@echo "==> go test -bench (smoke) $(BENCH_PKGS)"
	$(GO) test $(BENCH_PKGS) -run ^$$ -bench . -benchmem -count=1

# 完整基准测试
bench: go-version
	@echo "==> go test -bench ./..."
	$(GO) test -run ^$$ -bench . -benchmem ./...

# =============================================================================
# 覆盖率 (Coverage)
# =============================================================================

COVER_PKGS ?= ./internal/... ./modules/... ./pkg/... ./log/... ./conf/...

# 覆盖率测试
cover: go-version
	@echo "==> go test -cover $(COVER_PKGS)"
	$(GO) test -cover $(COVER_PKGS)

# 生成覆盖率报告 (HTML)
test-cover: go-version
	@echo "==> Generating coverage report..."
	@mkdir -p build/coverage
	$(GO) test -coverprofile=build/coverage/coverage.out $(COVER_PKGS)
	$(GO) tool cover -html=build/coverage/coverage.out -o build/coverage/coverage.html
	@echo "==> Coverage report: build/coverage/coverage.html"

# =============================================================================
# 其他工具 (Utilities)
# =============================================================================

# 整理依赖
tidy:
	@echo "==> go mod tidy"
	$(GO) mod tidy

# 安装到 $GOPATH/bin
install: go-version
	@echo "==> go install ./cmd/n42"
	$(GO) install $(GO_FLAGS) ./cmd/n42

# =============================================================================
# CI 目标 (CI Targets)
# =============================================================================

# 推荐 CI 用这个
ci: build test vet

# 完整 CI（包含 lint 和 race）
ci-full: build test vet lint race-core

# =============================================================================
# 帮助信息 (Help)
# =============================================================================

help:
	@echo ""
	@echo "N42 Makefile 目标:"
	@echo ""
	@echo "  构建 (Build):"
	@echo "    n42           - 编译 n42 二进制文件 (带依赖检查)"
	@echo "    build         - 全仓编译 (不触发 go mod tidy)"
	@echo "    install       - 安装到 \$$GOPATH/bin"
	@echo "    clean         - 清理构建产物"
	@echo ""
	@echo "  测试 (Test):"
	@echo "    test          - 运行全部测试"
	@echo "    test-short    - 快速测试 (-short 标志)"
	@echo "    test-verbose  - 详细测试输出"
	@echo "    test-cover    - 生成覆盖率报告 (HTML)"
	@echo "    cover         - 显示覆盖率摘要"
	@echo ""
	@echo "  Race 检测:"
	@echo "    race          - 全仓 race 检测 (较慢)"
	@echo "    race-core     - 核心包 race 检测"
	@echo ""
	@echo "  代码质量:"
	@echo "    fmt           - 格式化代码 (gofmt)"
	@echo "    vet           - 静态分析 (go vet)"
	@echo "    lint          - Lint 检查 (golangci-lint)"
	@echo "    check         - 组合检查 (fmt + vet + lint)"
	@echo ""
	@echo "  基准测试:"
	@echo "    bench         - 完整基准测试"
	@echo "    bench-smoke   - 快速基准测试 (核心包)"
	@echo ""
	@echo "  CI:"
	@echo "    ci            - 标准 CI (build + test + vet)"
	@echo "    ci-full       - 完整 CI (+ lint + race)"
	@echo ""
	@echo "  其他:"
	@echo "    deps          - 安装依赖 (go mod tidy)"
	@echo "    tidy          - 整理依赖"
	@echo "    devtools      - 安装开发工具"
	@echo "    gen           - 生成代码"
	@echo "    help          - 显示此帮助信息"
	@echo ""
	@echo "  Docker:"
	@echo "    images        - 构建 Docker 镜像"
	@echo "    up            - 启动 Docker Compose"
	@echo "    down          - 停止并清理 Docker"
	@echo "    stop          - 停止 Docker"
	@echo "    start         - 启动 Docker"
	@echo ""
	@echo "  Mobile:"
	@echo "    mobile        - 构建移动端 (Android + iOS)"
	@echo "    android       - 构建 Android"
	@echo "    ios           - 构建 iOS"
	@echo ""
	@echo "  版本管理 (Version):"
	@echo "    version       - 显示当前版本"
	@echo "    version-bump  - 递增构建号 (5.1.486 -> 5.1.487)"
	@echo "    version-minor - 递增小版本 (5.1.486 -> 5.2.0)"
	@echo "    version-major - 递增大版本 (5.1.486 -> 6.0.0)"
	@echo ""

# =============================================================================
# 版本管理 (Version Management)
# =============================================================================

# 显示当前版本
version:
	@cat VERSION 2>/dev/null || echo "VERSION file not found"
	@echo "Git: $(GIT_COMMIT) ($(GIT_BRANCH))"

# 递增构建号 (每次 build)
version-bump:
	@chmod +x scripts/bump_version.sh
	@./scripts/bump_version.sh build

# 递增小版本 (功能更新)
version-minor:
	@chmod +x scripts/bump_version.sh
	@./scripts/bump_version.sh minor

# 递增大版本 (年度更新)
version-major:
	@chmod +x scripts/bump_version.sh
	@./scripts/bump_version.sh major

# 带版本递增的发布构建
release: version-bump build
	@echo "==> Release build complete: $$(cat VERSION)"
