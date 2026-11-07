# 从源码构建

*从主 [README](../README.md) 移出，该文件保留每节的简短版本。*

## 安装

### 前置条件

- Go 1.23 或更高版本（模块声明 `go 1.23.9`）
- `make`（可选——仅为便捷目标；使用纯 `go` 命令也能完成一切）
- 对 ClickHouse HTTP(S) 接口的网络访问
- 对 ClickHouse 配置目录的读访问（可选，仅在采集配置时需要）

### 使用 Make 构建（推荐）

仓库附带一个 `Makefile`，封装了常见工作流。在仓库根目录执行：

```bash
make deps        # go mod download && go mod tidy
make build       # 生成 ./bin/clickhouse-diagnostic
make run         # 按需构建并以交互方式运行二进制
make test        # go test -v ./...
make fmt         # go fmt ./...
make lint        # golangci-lint run（需要已安装 golangci-lint）
make clean       # 删除 ./bin、./clickhouse_results、./configuration、*.tar.gz
make help        # 列出所有目标
```

### 直接使用 `go` 构建

如果你不想使用 `make`：

```bash
# 获取并整理依赖
go mod download
go mod tidy

# 在 ./clickhouse-diagnostic 构建二进制
go build -o clickhouse-diagnostic ./cmd

# 或安装到 $GOPATH/bin（或 $GOBIN），使其位于 PATH 中
go install ./cmd
```

### 跨平台发布构建

`make release` 为我们发布的所有平台生成二进制：

```bash
make release
# 输出（位于 ./bin/）：
#   clickhouse-diagnostic-linux-amd64
#   clickhouse-diagnostic-darwin-amd64
#   clickhouse-diagnostic-darwin-arm64
#   clickhouse-diagnostic-windows-amd64.exe
```

不使用 `make` 交叉编译单个目标：

```bash
GOOS=linux  GOARCH=amd64 go build -o bin/clickhouse-diagnostic-linux-amd64  ./cmd
GOOS=darwin GOARCH=arm64 go build -o bin/clickhouse-diagnostic-darwin-arm64 ./cmd
```

构建产物是静态链接的 Go 二进制——将文件复制到目标主机即可运行，无需运行时依赖。