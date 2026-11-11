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
| Windows | 放行同步端口入站（默认 TCP `47100`） | 剪贴板与托盘都走 Win32 API、无需额外依赖；但 Defender 防火墙**默认阻断入站**，不放行就只剩余单向同步 |
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

### 放行防火墙（Windows 必做；Linux 开了 ufw 也要）

clipshare 是对等 P2P：**两个方向都要能连**。Windows Defender 默认阻断入站，不放行的典型表现是「能收到别人的内容，别人收不到你的」——实测 Linux → Windows 的 `GET /v1/health` 与 `POST /v1/items` 都返回 `HTTP 000`（连不上），反向 Windows → Linux 却是 `HTTP 200`。

Windows（管理员 PowerShell，`LocalPort` 改成你配置里的 `port`）：

```powershell
New-NetFirewallRule -DisplayName clipshare -Direction Inbound -Protocol TCP -LocalPort 47100 -Action Allow
```

Linux（若启用了 ufw，同样改端口）：

```bash
sudo ufw allow 47100/tcp
```

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

首次启动会生成配置，并随机一个 **12 位**房间码（`roomKey`；31 字符表 ⇒ 31^12 ≈ 7.9×10^17 ≈ **59.5 bit** 熵）。

**第三步：对齐房间码** —— 把每台机器 `config.json` 里的 `roomKey` 改成**同一个值**（两台、三台或更多都一样）。

> 12 位房间码不是用来背的：多机之间请**直接复制粘贴整串**，不要口述或手抄。启动日志会打印**完整**房间码，可直接整串复制到另一台机器（贴日志求助前请先打码，见「安全边界」）。`roomKey` 只是进房间的凭据，不参与内容加密。

**第四步：重启全部进程** —— 同一局域网内通过 mDNS（服务类型 `_clipshare._tcp`）自动互相发现。

**第五步：确认已互连**

- 托盘状态行：`与 N 台同步中`（N ≥ 1）/ `无可用节点`（只有自己）/ `已暂停`
- 或者直接问对端的 health 接口（免鉴权，默认端口 47100）：

```bash
curl -s http://192.168.1.7:47100/v1/health
```

返回四个字段：`nodeId`、`name`、`version`、`roomHash`。两台的 `roomHash` 必须相同——它等于 `sha256(roomKey)` 的前 12 位；不同就是 `roomKey` 没对齐。`version` 与 `./clipshare --version` 打印的是同一个值。

**一次真实启动的输出**（本机实测：Ubuntu 20.04 / go1.24.0；用 `--config-dir /tmp/clipshare-demo` 避免污染用户目录，`--no-tray` 方便贴日志）：

```console
$ ./bin/clipshare --version
clipshare 0.1.0

$ ./bin/clipshare --no-tray --port 47999 --config-dir /tmp/clipshare-demo
2026/11/11 20:18:34 [engine] 启动完成：节点 haitang（e0a195），端口 47999，房间 a98508acc9a9
2026/11/11 20:18:34 clipshare 0.1.0 已启动：节点 "haitang"，端口 47999，房间 a98508acc9a9
2026/11/11 20:18:34 配置文件: /tmp/clipshare-demo/config.json
2026/11/11 20:18:34 本地历史页: http://127.0.0.1:48000/ui
2026/11/11 20:18:34 房间码: gjhbh8u6chsw —— 其他机器填同一个 roomKey 才能互通（贴日志求助前请先打码）
2026/11/11 20:18:34 首次启动：已生成配置 /tmp/clipshare-demo/config.json；把各台机器的 roomKey 改成同一个值，再分别启动
2026/11/11 20:18:34 已禁用托盘（--no-tray）：本地历史页 http://127.0.0.1:48000/ui，Ctrl+C 退出

$ curl -s http://127.0.0.1:47999/v1/health
{"nodeId":"e0a195c5380b67353bf616be66d62633","name":"haitang","version":"0.1.0","roomHash":"a98508acc9a9"}

$ curl -s -o /dev/null -w 'HTTP %{http_code}, %{content_type}, %{size_download} bytes\n' http://127.0.0.1:48000/ui
HTTP 200, text/html; charset=utf-8, 9897 bytes
```

> **本机历史页**：端口 = 同步端口 + 1（默认 `47100` → `http://127.0.0.1:47101/ui`），只绑回环地址、不鉴权；该端口被占用时会自动换一个，实际地址以启动日志里「本地历史页: …」那行为准。托盘菜单「查看全部历史…」直接打开它。
>
> ⚠️ **在同一台机器上跑多个实例做联调时注意端口错位**：实例 A 用 `--port 47901` 时，它的历史页会占用 `47902`，正好撞上实例 B 的同步端口——此时**先启动的实例会赢，后启动的那个报端口占用并退出 1**。联调请把端口隔开（例如 `--port 47901` 与 `--port 47911`），或给后启动的实例加 `--no-tray` 时留意日志里的实际历史页地址。

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
- **房间码是唯一凭证，长度必须够**：`/v1/health` 与 mDNS TXT 都会公开 `sha256(roomKey)` 的前 12 位（48 bit）。拿到这个 hash 就能**离线枚举**候选 key 并用它验证——**6 位房间码只有 31^6 ≈ 8.9×10^8 ≈ 29.7 bit，单核分钟级即可爆破**，等于把整个房间（读剪贴板流 + 往你剪贴板写内容）交出去。当前版本首启生成 **12 位（31^12 ≈ 59.5 bit）**；旧的 6 位配置仍可用，但请改成 12 位以上，且多机对齐时从 `config.json` **复制粘贴**，不要口述/手抄。
- **启动日志会打印完整房间码**（方便直接复制到另一台机器）：贴日志、截图或提 issue 前请先把房间码打码，否则等于把房间凭证公开。
- `/v1/health` **免鉴权**：同网段的人可以探测某个房间 hash 是否存在（但不会泄露 `roomKey` 本身）。
- **历史默认明文落盘**（`history.json`，权限 0600）：不想落盘就 `"persist": false` 或加 `--no-persist`（内存历史仍可用）。
- **Web UI 只绑 `127.0.0.1`、不做鉴权**：能访问本机这个端口的人就能读历史、写剪贴板。

不要在不可信网络（咖啡店 Wi-Fi、公共热点）里开。v2 的升级路径是在 listener/dialer 两处加自签 TLS，协议其余部分不变。

## 6. 已知行为（有意的取舍）

1. **同一内容重复复制不会再广播**：轮询用「上次读到的内容」判重，你再次复制与剪贴板当前完全相同的内容不会产生事件；远端同理，服务端按内容去重，重复条目回 `202` 后直接丢弃。代价是这条记录不会在其他机器上刷新时间戳。
2. **所有排序与显示都用本机接收时间**（`recvAt`）：跨机时钟漂移不影响顺序，但显示的是「我收到的时刻」，不是发送方的时间。
3. **图片 / 非文本**：剪贴板里没有文本时视为「无变化」，不入历史、不广播。
4. **不重试**：广播失败或超时（3s）只记日志——剪贴板内容有过期语义，延迟重发会造成「你以为复制了 A，剪贴板后来跳成 B」的时序错乱。
5. **暂停是「外向」开关**：不广播本机内容、不写本机剪贴板；但**远端条目仍会进入本地历史**（历史与剪贴板是两条独立通道，与关闭「自动应用」时的行为一致）。
6. **同一台机器跑两个实例联调时，历史来源可能显示成「另一个实例」**：两个进程共享同一个剪贴板——A 收到远端内容写进剪贴板后，B 的轮询会把同一份内容识别成「本机复制」，于是 B 的历史里来源显示为 B 而不是 A。这**既不是转发也不是回环**：服务端按内容去重（重复条目回 `202` 后丢弃），不会扩散；真实的两台机器各有独立剪贴板，也不会出现。单机自测时别把这条当 bug，用两份内容或两台机器区分即可。

## 7. 配置项（`config.json`）

字段与 `internal/config/config.go` 的 `Config` 结构体一一对应。所有字段都会被写回文件；手改时只改需要的字段，**其它字段请保留原值**（缺字段会回落默认值，`deviceId` / `roomKey` 缺失会被重新生成）。

| 字段 | 类型 | 默认值 | 含义 / 校验 |
|---|---|---|---|
| `nodeName` | string | `os.Hostname()` | 托盘与 Web UI 显示的本机名 |
| `deviceId` | string | 首启随机 32 位 hex | 节点唯一标识；排除「自己」、peer 去重都靠它。**不要把两台机器改成同一个值** |
| `roomKey` | string | 首启随机 **12 位**（31 字符表 ⇒ ≈59.5 bit；字母表去掉了 0/O/1/l/i） | 房间密钥，唯一定义「谁和谁同步」；不能为空，多机必须相同。旧的 6 位 key 仍可用（仅 ≈29.7 bit，而它的 48 bit hash 是公开的，可被离线爆破），建议换成 12 位以上 |
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
| `--port <port>` | 覆盖 `port`（默认 `47100`） |
| `--seed <host:port>` | 追加一个静态 seed，可重复指定（给禁多播网络用） |
| `--mdns` | 启用 mDNS 自动发现（默认开）；用 `--mdns=false` 关闭，改为只连 `seeds` |
| `--no-tray` | 不初始化托盘，改用本地历史页 |
| `--no-persist` | 不落盘历史（等价 `persist: false`） |
| `--config-dir <dir>` | 配置目录（默认取系统用户配置目录下的 `clipshare/`） |
| `--version` | 打印 `clipshare <version>` 后退出（版本由构建时 `-ldflags -X main.version` 注入） |

- flag 优先级高于配置文件；但**只影响本次运行，不写回配置文件**（实测：带 `--room flag-room --port 47999` 启动后重读 `config.json`，值仍是原样）。
- 完整列表与文案以 `./clipshare --help` 为准（附录有实跑原文）。
- `--config-dir` 会一路生效到托盘开关：切换「自动应用 / mDNS」时写回的是**这个目录**的 `config.json`，不会在你的默认配置目录留下第二份（有回归测试守着）。

## 9. 排障

- **端口被占用**：启动报 `端口 47100 无法监听（可能已被占用），用 --port 或配置里的 port 换一个: …` → 换端口，两边一起改。
- **托盘图标不显示**：GNOME 需要 AppIndicator 扩展（`gnome-shell-extension-appindicator`）以及 `org.kde.StatusNotifierWatcher`；否则加 `--no-tray` 降级，日志里会打印本地历史页地址。
- **收不到别的内容**：
  1. `curl -s http://<对端IP>:47100/v1/health`，比较两边的 `roomHash` 是否一致（不一致 = `roomKey` 不同）；
  2. 确认在同一网段；若组播被禁，按第 4 节配置 `seeds`；
  3. 确认**两端**都放行 `47100/tcp` 入站（Windows 见第 2 节的 PowerShell 命令；ufw 用 `sudo ufw allow 47100/tcp`）。
- **一端能收到、另一端收不到**：九成是「收得到的那端」防火墙没放入站（Windows Defender 默认阻断）。按第 2 节命令放行，然后从**两个方向各测一次**定位：

  ```bash
  curl -s -m 3 -o /dev/null -w '%{http_code}\n' http://<对方IP>:47100/v1/health
  ```

  `200` = 这个方向通；`000` = 连不上（被防火墙挡、端口不对或没在监听）。哪边测出 `000`，就是哪边的入站没放行。
- **日志显示「广播条目 … 当前无可用节点」**：说明 peer 发现根本没成功，而不是发送失败。依次确认：① 各机 `roomKey` 一致；② `mdns: false` 时 `seeds` 的 `host:port` 填对且能互相路由；③ 端口入站已放行——seeds 探活走的就是同一个端口，被防火墙挡住就永远发现不了对方。
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

## 附录：命令验证状态（定稿）

环境：Ubuntu 20.04.6 LTS，`go1.24.0 linux/amd64`，`DISPLAY=:1`（无 `WAYLAND_DISPLAY`），已装 `xsel`。
以下每条命令都在本机真跑过，输出为原文。

```console
$ make all
go vet ./...
go test ./... -race -count=1
ok  	clipshare/cmd/clipshare	1.014s
ok  	clipshare/internal/clip	1.462s
ok  	clipshare/internal/config	1.012s
ok  	clipshare/internal/discovery	1.316s
ok  	clipshare/internal/engine	12.980s
ok  	clipshare/internal/history	1.139s
ok  	clipshare/internal/model	1.008s
ok  	clipshare/internal/sync	1.327s
ok  	clipshare/internal/tray	1.052s
ok  	clipshare/internal/webui	1.015s
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=0.1.0" -o bin/clipshare ./cmd/clipshare
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=0.1.0" -o bin/clipshare.exe ./cmd/clipshare

$ ls -l bin/
-rwxrwxr-x 1 haitang haitang 9801912 11月 11 19:12 clipshare
-rwxrwxr-x 1 haitang haitang 7737856 11月 11 19:12 clipshare.exe

$ file bin/clipshare bin/clipshare.exe
bin/clipshare:     ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, BuildID[sha1]=65a1d09cab5bb1dfaf18ce1334971f6662d6ea3b, stripped
bin/clipshare.exe: PE32+ executable (console) x86-64, for MS Windows
```

### 运行期行为（`--no-tray`，真实剪贴板）

```console
$ printf 't5-accept-%s' "$(date +%s)" | xsel --clipboard --input   # 模拟用户复制
$ curl -s http://127.0.0.1:48000/ui/api/entries
[{"id":"7df77ffe3f748c06","origin":"e0a195c5380b67353bf616be66d62633","originName":"haitang","content":"t5-accept-1794399516","recvAt":"2026-11-11T20:18:36.398623633+08:00"}]

$ kill -TERM <pid>          # exit=0；日志末两行：
收到信号 terminated，开始退出…
[engine] 已停止

$ cat /tmp/clipshare-demo/history.json
[{"id":"7df77ffe3f748c06","origin":"e0a195c5380b67353bf616be66d62633","originName":"haitang","content":"t5-accept-1794399516","recvAt":"2026-11-11T20:18:36.398623633+08:00"}]
```

- `/v1/health` 的 `version` 与 `--version` 都是 `0.1.0`（同一个注入值，已核对）。
- SIGTERM 后历史已落盘（`history.json` 里是真实剪贴板条目）。
- 端口冲突时报 `clipshare: 端口 47999 无法监听（可能已被占用），用 --port 或配置里的 port 换一个: listen tcp :47999: bind: address already in use`，退出码 1。
- 首启生成的 `roomKey` 实测 12 位（本次为 `gjhbh8u6chsw`），启动日志打印的是**完整值**（已用脚本把日志行与 `config.json` 逐字符比对，一致）；`/v1/health` 的 `roomHash` = `sha256(roomKey)` 前 12 位（`a98508acc9a9`，已独立复算核对）。
- 用日志里的完整 key 直接加入另一实例（`--room <日志里的 key>`）时两边 `roomHash` 相同；对 `/v1/items` 用**完整 key** 请求得 `202`，用旧 bug 的**前 6 位**请求得 `401`（已验证，说明「日志只打前缀」确实会让人配不上）。
- flag 只对本次运行生效：以 `--room flagroom --port 47996 --mdns=false` 启动时 `/v1/health` 的 `roomHash` = `c16ecc5336ee`（= `sha256("flagroom")` 前 12 位），而 `config.json` 里仍是随机 12 位 `roomKey`（`g6gxne5cqjhd`）、`port: 47100`、`mdns: true`。
- 剪贴板命令缺失时的退出路径（`PATH=/nonexistent` 实测）：`clipshare: 缺少剪贴板命令 xclip/xsel：请安装其一，例如 sudo apt install xclip（或 sudo apt install xsel）`，退出码 1，不会静默空转。
- 托盘模式（不带 `--no-tray`）在本机能启动、SIGTERM 退出码 0。

### 真机验收记录（Ubuntu 22.04 X11 + Windows 10 双机实测）

在真实的两台机器上跑过（不是模拟）：Linux `172.26.81.147`、Windows 10 19045 `172.26.81.73`，同一 `/22` 网段，两端 `roomKey` 一致、`--mdns=false` + 互填 `seeds`。

**跨机同步（Windows → Linux，已验证通过）**

在 Windows 侧复制 `e2e-real-202951`：

```text
Windows 历史: [{"originName":"DESKTOP-UIRQT1R","content":"e2e-real-202951\n", ...}]
Linux 剪贴板: e2e-real-202951        ← 被写入
Linux 历史:   条数 1, 来源 DESKTOP-UIRQT1R
```

即：源端入历史 → 广播 → 对端剪贴板被写入 → 对端历史带正确来源标记。

**Windows 二进制**

`clipshare.exe`（PE32+，静态无依赖）在本机 Windows 10 上 `--version` → `clipshare 0.1.0`、`--help` 正常、`/v1/health` 正常返回且 `roomHash` 与 Linux 端一致。

**Linux 托盘图标（已验证注册到系统托盘）**

不靠肉眼，用 DBus 差集证明：启动前 `org.kde.StatusNotifierWatcher` 的 `RegisteredStatusNotifierItems` 有 6 条，启动后有 7 条，新增项为 `:1.1591/StatusNotifierItem`；`SIGTERM` 后回到 6 条、无残留。同期历史页 `HTTP 200, text/html, 9897 bytes`，临时目录里只写 `config.json`（`--no-persist` 时确实不写 `history.json`），默认配置目录未被创建。

**防火墙实测（这也是第 2 节那条命令的由来）**

| 方向 | 结果 |
|---|---|
| Linux → Windows `/v1/health`、`/v1/items` | `HTTP 000`（Defender 默认阻断入站） |
| Windows → Linux `/v1/health` | `HTTP 200` |

不放行入站就只剩单向同步。放行命令见 §2。

**仍未覆盖**（诚实列出）

- Windows 托盘图标的视觉效果与菜单点击（需要交互式桌面会话；SSH 会话里跑不出可见托盘）。
- Linux 托盘菜单的逐项点击（只验证了图标注册与注销）。
- 真实多播环境下的 mDNS 自动发现（双机验收用的是 `--mdns=false` + `seeds` 这条确定性更强的路径）。
- macOS（不是 v1 目标平台）。

### `--help` 原文

```console
$ ./bin/clipshare --help
clipshare 0.1.0 —— 局域网剪贴板共享

用法：clipshare [选项]

不写选项时完全使用配置文件（首次启动自动生成）。flag 优先级高于配置文件。

选项：
  -config-dir string
    	配置目录（默认取系统用户配置目录下的 clipshare/）
  -mdns
    	是否启用 mDNS 自动发现；用 --mdns=false 关闭，改为只连 seeds (default true)
  -name string
    	本机显示名，覆盖配置里的 nodeName（默认主机名）
  -no-persist
    	不把历史落盘（等价于配置里的 persist: false）
  -no-tray
    	不初始化托盘，改用本地历史页（无 DBus / 无 AppIndicator 扩展时用）
  -port int
    	HTTP 监听端口，覆盖配置里的 port（默认 47100）
  -room string
    	房间码，覆盖配置里的 roomKey；多台机器必须填同一个
  -seed value
    	追加一个静态节点地址 host:port（可重复指定；给禁多播网络用）
  -version
    	打印版本号后退出
```

### 仍需人工验收（代码跑不出来）

已由真机验收覆盖、不再是待办：**两台真机互相同步**（Windows → Linux 实测通过，见上文「真机验收记录」）、**A→B→A 不回环**（`TestIntegrationNoLoopback` 断言 B 广播 0 次 + 双进程实测双向不转发）、**禁多播网络的 `mdns: false` + `seeds` 连通**（上面双机验收走的就是这条路径）、**Windows 侧实际运行**（真实 Windows 10 上跑通）。

剩下的只有一类，都需要人坐在电脑前看屏幕或点鼠标：

- **托盘菜单逐项点击**：取回历史、发送当前剪贴板、勾选自动应用 / mDNS、查看全部历史、退出（Linux 侧已验证图标注册与注销，菜单项的点击效果未逐个验）。
- **Windows 托盘图标的视觉效果**（SSH 会话跑不出可见托盘，需要交互式桌面会话）。
- **真实多播环境下的 mDNS 自动发现**（双机验收刻意用了 `--mdns=false` 的确定性路径）。

依赖表依据：`apt-cache policy xclip xsel wl-clipboard` 显示三者在本机 focal/universe 仓库均有候选包（`xclip 0.13-1`、`xsel 1.2.0+git9bfc13d.20180109-3`、`wl-clipboard 2.0.0-1`），本机已装 `xsel`。
