# clipshare — 局域网剪贴板共享（Go 单模块，无需 cgo）
#
# 两个目标平台都是 CGO_ENABLED=0：
#   - Linux：托盘走 DBus StatusNotifierItem，剪贴板调 xclip/xsel/wl-clipboard 外部命令；
#   - Windows：托盘与剪贴板都走 Win32 API。
# 因此交叉编译不需要 C 工具链，任一平台的 Go 1.24 都能产出两边的二进制。

VERSION ?= 0.1.0
# 允许按需覆盖架构：make build-linux GOARCH=arm64
GOARCH ?= amd64

BIN_DIR := bin
# -X main.version 与 cmd/clipshare 里的 `var version` 对齐（--version 读它）。
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build-linux build-windows test vet fmt clean

# 默认目标：先静态检查与测试，再产出两平台二进制。
all: vet test build-linux build-windows

build-linux:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/clipshare ./cmd/clipshare

build-windows:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=windows GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/clipshare.exe ./cmd/clipshare

# -race 只能在本机执行，交叉编译的产物不参与测试。
test:
	go test ./... -race -count=1

vet:
	go vet ./...

# 注意：gofmt -w 会直接改写文件（只动 Go 源码）。
fmt:
	gofmt -l -w .

clean:
	rm -rf $(BIN_DIR)
