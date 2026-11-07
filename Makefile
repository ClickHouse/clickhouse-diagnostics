# ClickHouse 诊断工具 Makefile

# Variables
BINARY_NAME=clickhouse-diagnostic
GO_FILES=$(shell find . -name "*.go" -type f)
BUILD_DIR=./bin
CMD_DIR=./cmd
DIST_DIR=./dist

# 发布归档的目标平台（os/arch）
PLATFORMS=linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
# 工具从工作目录读取的运行时数据。这些内容必须与二进制一起分发，
# 因此每个发布归档都会打包它们。
DATA_DIRS=queries.cloud queries.onprem queries.gov queries.query_analysis alerts skills cluster-collect airgap docs

# 默认目标
.PHONY: all
all: build

# 下载依赖
.PHONY: deps
deps:
	go mod download
	go mod tidy
	@echo "依赖已下载并整理"

# 构建应用
.PHONY: build
build: $(BUILD_DIR)/$(BINARY_NAME)

$(BUILD_DIR)/$(BINARY_NAME): $(GO_FILES) go.mod go.sum
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)
	@echo "已成功构建 $(BINARY_NAME)"

# 运行应用
.PHONY: run
run: build
	$(BUILD_DIR)/$(BINARY_NAME)

# 清理构建产物
.PHONY: clean
clean:
	rm -rf $(BUILD_DIR)
	rm -rf $(DIST_DIR)
	rm -rf clickhouse_results
	rm -rf configuration  # v0.3.0 之前的运行会把配置写在这里；现在无害，保留用于清理旧残留
	rm -f *.tar.gz
	@echo "已清理构建产物"

# 测试应用（添加测试后）
.PHONY: test
test:
	go test -v ./...

# 格式化代码
.PHONY: fmt
fmt:
	go fmt ./...

# 代码检查（需要 golangci-lint）
.PHONY: lint
lint:
	golangci-lint run

# 为多个平台创建发布归档。
# 每个归档都把二进制与运行时数据目录（queries.*、alerts）打包在一起，
# 这样从解压后的文件夹直接运行即可——单独一个二进制会从工作目录读取
# 这些文件，否则会失败。
# Unix 目标生成 .tar.gz，Windows 目标生成 .zip，输出到 $(DIST_DIR)。
#
# CGO_ENABLED=0 让所有产物静态链接。Go 在交叉编译时会自行禁用 cgo，
# 因此六个目标中有五个本来就是静态的——但匹配发布运行器的目标
# （linux/amd64）不是交叉编译，cgo 保持开启，并动态链接到运行器的
# glibc。该二进制随后在较旧主机上会以
# "version `GLIBC_2.34' not found" 拒绝启动，甚至还没运行到我们的代码。
.PHONY: release
release: clean
	@mkdir -p $(DIST_DIR)
	@set -e; \
	for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		pkg=$(BINARY_NAME)-$$os-$$arch; \
		stage=$(DIST_DIR)/$$pkg; \
		bin=$(BINARY_NAME); \
		if [ "$$os" = "windows" ]; then bin=$(BINARY_NAME).exe; fi; \
		echo "正在打包 $$pkg..."; \
		mkdir -p $$stage; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -o $$stage/$$bin $(CMD_DIR) || exit 1; \
		cp -R $(DATA_DIRS) $$stage/; \
		cp README.md AGENTS.md $$stage/ 2>/dev/null || true; \
		if [ "$$os" = "windows" ]; then \
			(cd $(DIST_DIR) && zip -qr $$pkg.zip $$pkg); \
		else \
			tar -czf $(DIST_DIR)/$$pkg.tar.gz -C $(DIST_DIR) $$pkg; \
		fi; \
		rm -rf $$stage; \
	done
	@echo "发布归档已创建于 $(DIST_DIR)/"

# 将二进制安装到 GOPATH/bin
.PHONY: install
install:
	go install $(CMD_DIR)

# 设置示例查询
.PHONY: setup
setup:
	cp -r example-queries queries || echo "example-queries directory not found"
	@echo "设置完成——已创建 queries 目录"

# 显示帮助
.PHONY: help
help:
	@echo "可用命令："
	@echo "  deps      - 下载并整理依赖"
	@echo "  build     - 构建应用"
	@echo "  run       - 构建并运行应用"
	@echo "  setup     - 复制示例查询到 queries 目录"
	@echo "  clean     - 清理构建产物和输出文件"
	@echo "  test      - 运行测试"
	@echo "  fmt       - 格式化 Go 代码"
	@echo "  lint      - 检查 Go 代码（需要 golangci-lint）"
	@echo "  release   - 为多个平台创建发布构建"
	@echo "  install   - 将二进制安装到 GOPATH/bin"
	@echo "  help      - 显示此帮助信息"

# 从匿名化 fixture 渲染三个预览页面，这样无需实时服务器也能审查面板：
# Keeper 故障 dashboard（共享存储集群上的 Keeper 中断——Keeper Health 面板与告警）、
# 告警折叠用例（堆栈、超出上限的实例、失败与不适用的规则），以及
# 小型 fixture 管道的 schema graph。
# 写出 bin/keeper_incident_preview.html 与 bin/alerts_preview.html
# （Chart.js 来自 CDN）以及 bin/schema_graph_preview.html（完全无网络）。
.PHONY: dashboard-preview
dashboard-preview:
	mkdir -p bin
	DASHBOARD_PREVIEW_DIR=$(CURDIR)/bin go test ./internal/dashboard -run 'TestBuildHTML_(KeeperIncidentPreview|AlertsPreview)|TestBuildSchemaGraphHTML_Preview' -count=1 >/dev/null \
	  && echo "bin/keeper_incident_preview.html" && echo "bin/alerts_preview.html" \
	  && echo "bin/schema_graph_preview.html (also reachable from the preview dashboard's Schema tab)"
