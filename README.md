# clipshare

局域网内多台机器共享剪贴板：**在一台机器上复制，同房间的其他机器自动同步**，每台机器各自保留可浏览的历史。

Ubuntu（X11 / Wayland）与 Windows 双平台，单个二进制、无 cgo、无云服务、零配置发现（mDNS）；多播被禁的网络用静态 seeds 兜底。

## 1. 这是什么

两台机器各跑一个进程，房间码对齐后：

```bash
# 机器 A（把 roomKey 配好之后启动）
./clipshare

# 机器 B（同一局域网，同一 roomKey）
./clipshare
```

此后在任一台复制文本，另一台的剪贴板会变成同样内容，并在两边的历史里各留一条带来源标记的记录。桌面环境下进程常驻托盘；没有托盘的环境用 `--no-tray`，改用本地历史页。

## 2. 前置依赖

| 平台 / 会话 | 需要什么 | 说明 |
|---|---|---|
| Linux + X11 | `xclip` **或** `xsel` | 两者装一个即可；程序通过外部命令读写剪贴板 |
| Linux + Wayland（`WAYLAND_DISPLAY` 非空） | `wl-clipboard`（`wl-paste` + `wl-copy`） | 必须两个命令都在 |
| Windows | 无 | 剪贴板与托盘都走 Win32 API |
| 构建时 | Go 1.24+ | 纯 Go，`CGO_ENABLED=0`，不需要 C 工具链 |

Ubuntu 安装（`xclip` / `xsel` / `wl-clipboard` 都在官方 universe 仓库）：

```bash
# X11 会话
sudo apt install xclip
# 或者
sudo apt install xsel

# Wayland 会话
sudo apt install wl-clipboard
```

**为什么不能混用**：X11 的 `xclip`/`xsel` 在 Wayland 会话里只能看到 XWayland 的剪贴板，而原生 Wayland 剪贴板是另一份数据——混用的表现是「同步过去的不是你在用的那份」。所以程序按会话变量严格选择：`WAYLAND_DISPLAY` 非空就要求 `wl-clipboard`，否则要求 `xclip`/`xsel`；两者都没有则直接报「未检测到图形会话」并退出。

## 3. 快速开始

**第一步：构建**（也可以直接用别人给的可执行文件）

```bash
make build-linux      # → bin/clipshare
make build-windows    # → bin/clipshare.exe（可在任一台机器上交叉产出）
```

**第二步：先跑一次生成配置**

| 平台 | 配置文件 | 历史文件 |
|---|---|---|
| Linux | `~/.config/clipshare/config.json` | `~/.config/clipshare/history.json` |
| Windows | `%APPDATA%\clipshare\config.json` | `%APPDATA%\clipshare\history.json` |

首次启动会生成配置，并随机一个 6 位房间码（`roomKey`）。

**第三步：对齐房间码** —— 把每台机器 `config.json` 里的 `roomKey` 改成**同一个值**（两台、三台或更多都一样）。`roomKey` 只是进房间的凭据，不参与内容加密。

**第四步：重启全部进程** —— 同一局域网内通过 mDNS（服务类型 `_clipshare._tcp`）自动互相发现。

**第五步：确认已互连**

- 托盘状态行：`与 N 台同步中`（N ≥ 1）/ `无可用节点`（只有自己）/ `已暂停`
- 或者直接问对端的 health 接口（免鉴权，默认端口 47100）：

```bash
curl -s http://192.168.1.7:47100/v1/health
```

返回四个字段：`nodeId`、`name`、`version`、`roomHash`。两台的 `roomHash` 必须相同——它等于 `sha256(roomKey)` 的前 12 位；不同就是 `roomKey` 没对齐。

> 本机历史页：托盘菜单「查看全部历史…」，地址形如 `http://127.0.0.1:47100/ui`，只绑回环地址、不鉴权。实际地址以启动日志打印的为准。

## 4. 禁多播网络怎么办（企业 Wi-Fi / 隔离 VLAN）

mDNS 依赖多播。多播被禁时，**唯一可用路径**是关掉 mDNS、改用手填的 seeds：在各台机器的 `config.json` 里改这两个字段（其余字段保持原样）：

```json
{
  "mdns": false,
  "seeds": ["192.168.1.7:47100", "192.168.1.8:47100"]
}
```

- `seeds` 每项是 `host:port`，**不要带 `http://`**。
- 不要用这段 JSON 覆盖整个文件：缺失字段会回落到默认值，`deviceId` / `roomKey` 缺失还会被**重新随机生成**，房间码就白对齐了。只改这两行。
- 程序每 30s 对每个 seed 请求一次 `GET /v1/health`，只有 `roomHash` 与本地一致、且 `nodeId` 不是自己的才会收编。
- 双向填更稳：A 填 B 的地址、B 填 A 的地址；只填一边时，另一边也能靠「被探活」反向发现，但单向配置的收敛更慢。

## 5. 安全边界（请务必先看这一段）

v1 是**内网信任模型**，没有 TLS、没有账号体系。以下每条都请当成事实：

- **明文传输**：HTTP/1.1 + JSON，同网段可被嗅探，也可被注入。
- **同房间内任何人，既能读取这条同步流，也能直接往你的剪贴板写内容**——`/v1/items` 收到即写入历史，`autoApply` 打开时还会写进本机剪贴板。
- **房间密钥只解决「谁能接入」**，不提供保密性，也不防重放。
- `/v1/health` **免鉴权**：同网段的人可以探测某个房间 hash 是否存在（但不会泄露 `roomKey` 本身）。
- **历史默认明文落盘**（`history.json`，权限 0600）：不想落盘就 `"persist": false` 或加 `--no-persist`（内存历史仍可用）。
- **Web UI 只绑 `127.0.0.1`、不做鉴权**：能访问本机这个端口的人就能读历史、写剪贴板。

不要在不可信网络（咖啡店 Wi-Fi、公共热点）里开。v2 的升级路径是在 listener/dialer 两处加自签 TLS，协议其余部分不变。

## 6. 已知行为（有意的取舍）

1. **同一内容重复复制不会再广播**：轮询用「上次读到的内容」判重，你再次复制与剪贴板当前完全相同的内容不会产生事件；远端同理，服务端按内容去重，重复条目回 `202` 后直接丢弃。代价是这条记录不会在其他机器上刷新时间戳。
2. **所有排序与显示都用本机接收时间**（`recvAt`）：跨机时钟漂移不影响顺序，但显示的是「我收到的时刻」，不是发送方的时间。
3. **图片 / 非文本**：剪贴板里没有文本时视为「无变化」，不入历史、不广播。
4. **不重试**：广播失败或超时（3s）只记日志——剪贴板内容有过期语义，延迟重发会造成「你以为复制了 A，剪贴板后来跳成 B」的时序错乱。

## 7. 配置项（`config.json`）

字段与 `internal/config/config.go` 的 `Config` 结构体一一对应。所有字段都会被写回文件；手改时只改需要的字段，**其它字段请保留原值**（缺字段会回落默认值，`deviceId` / `roomKey` 缺失会被重新生成）。

| 字段 | 类型 | 默认值 | 含义 / 校验 |
|---|---|---|---|
| `nodeName` | string | `os.Hostname()` | 托盘与 Web UI 显示的本机名 |
| `deviceId` | string | 首启随机 32 位 hex | 节点唯一标识；排除「自己」、peer 去重都靠它。**不要把两台机器改成同一个值** |
| `roomKey` | string | 首启随机 6 位（字母表去掉了 0/O/1/l/i） | 房间密钥，唯一定义「谁和谁同步」；不能为空，多机必须相同 |
| `port` | int | `47100` | HTTP 监听端口（`0.0.0.0:port`）；seeds 与 curl 都用它。取值 1–65535 |
| `seeds` | []string | `[]` | 静态节点列表，形如 `192.168.1.7:47100`（不带 scheme）；每项必须合法 host:port |
| `historyLimit` | int | `500` | 历史条数上限，超出裁掉最旧。取值 1–10000 |
| `itemMaxBytes` | int | `262144`（256 KiB） | 单条内容上限，超过则跳过（不广播、不入历史）。取值 1024–16777216 |
| `autoApply` | bool | `true` | 收到远端条目时自动写入本机剪贴板；关闭则只进历史，手动取回 |
| `mdns` | bool | `true` | 是否启用 mDNS 自动发现；关闭后只连 `seeds` |
| `persist` | bool | `true` | 历史是否明文落盘到 `history.json` |
| `pollIntervalMs` | int | `400` | 剪贴板轮询间隔（毫秒）。取值 100–5000 |
| `noTray` | bool | `false` | 不初始化托盘，等价于命令行 `--no-tray`（无 DBus / 无 AppIndicator 扩展的环境） |

配置文件损坏时：原文件备份为 `config.json.bak`，然后用默认值重建（`deviceId` / `roomKey` 会变，需要重新对齐房间码）。

## 8. 命令行参数

| flag | 作用 |
|---|---|
| `--room <key>` | 覆盖配置里的 `roomKey` |
| `--name <name>` | 覆盖 `nodeName` |
| `--port <port>` | 覆盖 `port` |
| `--seed <host:port>` | 追加一个静态 seed（在配置 `seeds` 之外） |
| `--no-tray` | 不初始化托盘，改用本地历史页 |
| `--no-persist` | 不落盘历史（等价 `persist: false`） |
| `--version` | 打印版本号（构建时由 `-ldflags -X main.version` 注入，默认 `0.1.0`） |

flag 优先级高于配置文件。完整列表与文案以 `./clipshare --help` 为准（见附录：该输出尚未复核）。

## 9. 排障

- **端口被占用**：启动报 `端口 47100 无法监听（可能已被占用），用 --port 或配置里的 port 换一个: …` → 换端口，两边一起改。
- **托盘图标不显示**：GNOME 需要 AppIndicator 扩展（`gnome-shell-extension-appindicator`）以及 `org.kde.StatusNotifierWatcher`；否则加 `--no-tray` 降级，日志里会打印本地历史页地址。
- **收不到别的内容**：
  1. `curl -s http://<对端IP>:47100/v1/health`，比较两边的 `roomHash` 是否一致（不一致 = `roomKey` 不同）；
  2. 确认在同一网段；若组播被禁，按第 4 节配置 `seeds`；
  3. 确认防火墙放行 `47100/tcp`（入站）。
- **Linux 报缺剪贴板命令**：X11 下报 `缺少剪贴板命令 xclip/xsel：请安装其一，例如 sudo apt install xclip（或 sudo apt install xsel）`；Wayland 下报 `Wayland 会话缺少剪贴板命令 wl-paste、wl-copy：请安装 wl-clipboard：sudo apt install wl-clipboard`——按提示装即可。若你其实在 X11 会话却带着 `WAYLAND_DISPLAY`，清掉该变量再启动。
- **无图形会话**：报 `未检测到图形会话（DISPLAY 与 WAYLAND_DISPLAY 均未设置），无法访问剪贴板` ——剪贴板必须有桌面会话，SSH/headless 下不可用。
- **配置被重置**：日志提示配置损坏时，同目录的 `config.json.bak` 是原文件；重建后 `deviceId` / `roomKey` 变了，需要重新对齐房间码。
- **历史落盘失败**（磁盘满 / 无权限）：内存历史继续可用，日志每分钟最多一条告警。

## 10. 开发

```bash
make vet            # go vet ./...
make test           # go test ./... -race -count=1
make fmt            # gofmt -l -w .（会改写文件）
make build-linux    # → bin/clipshare      CGO_ENABLED=0 GOOS=linux
make build-windows  # → bin/clipshare.exe  CGO_ENABLED=0 GOOS=windows
make all            # vet + test + 双平台构建（默认目标）
make clean          # 删除 bin/
```

两个 build 目标默认 `GOARCH=amd64`，可用 `make build-linux GOARCH=arm64` 覆盖（`-race` 只能在本机跑，交叉产物不参与测试）。版本号 `VERSION ?= 0.1.0`，经 `-ldflags -s -w -X main.version=$(VERSION)` 注入，可用 `make build-linux VERSION=0.2.0` 覆盖。

目录结构（一行一包）：

```
cmd/clipshare/     入口：解析 flag、加载配置、组装并启动
internal/model     条目类型、内容归一化、内容 hash / 房间 hash
internal/config    配置定位、默认值、加载、损坏备份重建
internal/history   环形历史、内容去重、原子落盘
internal/clip      剪贴板读写适配器 + 轮询变更检测
internal/sync      HTTP 收发、房间鉴权、并发广播
internal/discovery mDNS 公告/订阅 + 静态 seeds 探活
internal/engine    组装上述组件，独占同步语义（S1–S8）
internal/tray      托盘菜单与图标（含 --no-tray 降级）
internal/webui     本地历史页（只绑 127.0.0.1）
docs/superpowers/specs/  设计文档与任务拆解
```

## 附录：命令验证状态（初稿，定稿时需逐条复跑）

**已在本机实跑**（Ubuntu 20.04.6 LTS，`go1.24.0 linux/amd64`）：

```console
$ make vet
go vet ./...
# 无输出 = 通过

$ make test
go test ./... -race -count=1
ok  	clipshare/internal/clip	1.469s
ok  	clipshare/internal/config	1.009s
ok  	clipshare/internal/discovery	1.314s
ok  	clipshare/internal/engine	12.574s
ok  	clipshare/internal/history	1.138s
ok  	clipshare/internal/model	1.007s
ok  	clipshare/internal/sync	1.324s
ok  	clipshare/internal/tray	1.050s
ok  	clipshare/internal/webui	1.013s

$ make -n all
go vet ./...
go test ./... -race -count=1
mkdir -p bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=0.1.0" -o bin/clipshare ./cmd/clipshare
mkdir -p bin
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=0.1.0" -o bin/clipshare.exe ./cmd/clipshare

$ make fmt
gofmt -l -w .
# 执行前后 `gofmt -l .` 均为空，未改写任何文件

$ make build-linux
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=0.1.0" -o bin/clipshare ./cmd/clipshare
no Go files in /home/haitang/src/UserCode/tools/cmd/clipshare
make: *** [Makefile:23：build-linux] 错误 1
```

> **阻塞项**：`cmd/clipshare/main.go` 尚未落地（归 t5），所以两个 build 目标当前只能跑到「找不到 Go 文件」。main.go 需要声明 `var version = "dev"` 之类的变量供 `-X main.version` 注入，落地后 `make build-linux` 应产出 `bin/clipshare`。

依赖表依据：`apt-cache policy xclip xsel wl-clipboard` 显示三者在本机 focal/universe 仓库均有候选包（`xclip 0.13-1`、`xsel 1.2.0+git9bfc13d.20180109-3`、`wl-clipboard 2.0.0-1`），本机已装 `xsel`。

**尚未验证（无二进制，禁止编造输出；定稿时复跑）**：

- `./clipshare`、`./clipshare --help`、`./clipshare --version`、`--no-tray` 的实际输出
- `curl -s http://<host>:47100/v1/health` 的真实响应体
- 托盘菜单与状态行、本地历史页 `http://127.0.0.1:<port>/ui` 的实际地址
