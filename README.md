# clipshare

局域网内多台机器共享剪贴板：**在一台机器上复制，同房间的其他机器自动同步**，每台机器各自保留可浏览的历史。跨网络时，用一台自有服务器做中心点中继（自签 TLS + 证书指纹钉），不同 NAT 后的机器也能同步。

Ubuntu（X11 / Wayland）与 Windows 双平台，单个二进制、无 cgo、无云服务、零配置发现（mDNS）；多播被禁的网络用静态 seeds 兜底，跨网络走中心点中继。另有只发送的 Android 端。当前版本 **1.0.0**。

> 📖 **图文版使用手册**：装好之后看 `dist/docs/guide.html`（同目录也有 `guide.pdf`），
> Ubuntu 上装包的路径是 `/usr/share/doc/clipshare/guide.html`，Windows 在开始菜单「clipshare → 使用手册」。
> 本文件（README.md）保留给工具/AI 读取，内容更偏「契约与实现细节」。

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

**第一步：构建**（也可以直接从 [Releases](https://github.com/Ahaitang/clipshare/releases/latest) 下载 1.0.0 的 deb / zip / tgz / Windows 安装器与 Android APK）

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
clipshare 1.0.0

$ ./bin/clipshare --no-tray --port 47999 --config-dir /tmp/clipshare-demo
2026/11/14 23:56:24 ---- 日志开始：pid=4008515 文件=/tmp/clipshare-demo/clipshare.log ----
2026/11/14 23:56:24 [engine] 启动完成：节点 haitang（18d9ca），端口 47999，房间 3ae611bcb821
2026/11/14 23:56:24 clipshare 1.0.0 已启动：节点 "haitang"，端口 47999，房间 3ae611bcb821
2026/11/14 23:56:24 配置文件: /tmp/clipshare-demo/config.json
2026/11/14 23:56:24 本地历史页: http://127.0.0.1:48000/ui
2026/11/14 23:56:24 房间码: mn4nqa34yzyw —— 其他机器填同一个 roomKey 才能互通（贴日志求助前请先打码）
2026/11/14 23:56:24 首次启动：已生成配置 /tmp/clipshare-demo/config.json；把各台机器的 roomKey 改成同一个值，再分别启动
2026/11/14 23:56:24 已禁用托盘（--no-tray）：本地历史页 http://127.0.0.1:48000/ui，Ctrl+C 退出

$ curl -s http://127.0.0.1:47999/v1/health
{"nodeId":"18d9cab7feb7b90fe03a31dff66db651","name":"haitang","version":"1.0.0","roomHash":"3ae611bcb821"}

$ curl -s -o /dev/null -w 'HTTP %{http_code}, %{content_type}, %{size_download} bytes\n' http://127.0.0.1:48000/ui
HTTP 200, text/html; charset=utf-8, 14660 bytes
```

> **本机历史页**：端口 = 同步端口 + 1（默认 `47100` → `http://127.0.0.1:47101/ui`），只绑回环地址、不鉴权；该端口被占用时会自动换一个，实际地址以启动日志里「本地历史页: …」那行为准。托盘菜单「查看全部历史…」直接打开它。
>
> ⚠️ **在同一台机器上跑多个实例做联调时注意端口错位**：实例 A 用 `--port 47901` 时，它的历史页会占用 `47902`，正好撞上实例 B 的同步端口——此时**先启动的实例会赢，后启动的那个报端口占用并退出 1**。联调请把端口隔开（例如 `--port 47901` 与 `--port 47911`），或给后启动的实例加 `--no-tray` 时留意日志里的实际历史页地址。

### 便携包（tgz / zip，不经过包管理器）

不想用 deb、或者要装到自定义前缀时用 tgz：

```bash
# Linux：默认装到 /usr/local（需要 sudo）；也可以 PREFIX=$HOME/.local 免 root
tar xzf clipshare-1.0.0-linux-amd64.tar.gz
cd clipshare-1.0.0-linux-amd64
./install.sh                    # 或：PREFIX=$HOME/.local ./install.sh
./uninstall.sh                  # 卸载（只删程序，保留 ~/.config/clipshare）
```

也可以完全不安装，直接跑解压出来的 `./clipshare`。
Windows 的 tgz 与 zip 内容一致（`clipshare.exe` + `install.ps1` + 手册），
解开后双击 `安装.bat` 即可（会请求管理员权限以放行防火墙）。

### Android 发送端（只发送，不接收）

手机上的 APK：`clipshare-sender-<版本>.apk`，把手机里的文本发到局域网内的电脑。

**为什么它只发送**：桌面端是靠 mDNS / `seeds` 探活（`GET /v1/health`）发现彼此的，
而这个 App **不起 HTTP 服务**，所以电脑端根本发现不到它，也就不会往里推内容 ——
「只发送」不是靠开关，而是靠它没有可被发现的入口。

用法：

1. 在电脑上 `clipshare invite` 拿到邀请码，粘到 App 顶部（或通过下面的 Intent 预置）；
2. 在文本框输入/粘贴内容 → 点「发送」；也可以点「读系统剪贴板」直接取手机剪贴板；
3. 更省事：在任意 App 里选中文本 → 分享 → 选「clipshare 发送端」，**自动发送并返回**。

```bash
# 用 adb 预置邀请码（也可给二维码/深链用同一套 extra）
adb shell am start -n work.metoak.clipshare.sender/.MainActivity \
  --es invite 'clipshare1:房间码@电脑IP:47100'
```

发送成功后电脑端历史里来源显示为 **手机**（`origin=android`），并会像普通条目一样继续同步到其它电脑。

已知限制（都是有意的）：不接收、不保留历史、不在后台轮询剪贴板（Android 10+ 也不允许后台读剪贴板）；
协议为明文 HTTP，与桌面端一致。

### 第一次配置：三条路，最省事的是「邀请码」

一台从没配置过的机器，装完之后只要**拿到一条邀请码**就能接进房间。邀请码把
「房间码 + 对方地址」合成一条可复制的字符串（禁多播的网络也能连）：

```
clipshare1:2mnsn82k6b62@172.26.81.147:47100
```

**第一台机器**（确定房间）：

```bash
clipshare init        # 交互式：选 1「我是第一台机器」
# 或者不交互，直接拿邀请码：
clipshare invite
```

**新机器**（任意一种）：

```bash
clipshare join 'clipshare1:2mnsn82k6b62@172.26.81.147:47100'
```

或者打开本地历史页 <http://127.0.0.1:47101/ui> → 展开「房间与邀请」→ 粘贴邀请码 → 「加入房间」。
`join` 会写入 `roomKey` 与 `seeds`，并**自动让正在运行的实例重启**生效
（未运行时下次启动生效）。

几个设计细节，方便你判断能不能把邀请码发到群里：

- 邀请码**只包含房间码和地址**，不含设备标识、不含任何密钥以外的凭证；
- 它等价于把房间钥匙给对方——拿到的人可以读你们同步的剪贴板内容，
  所以按「发给可信同事」的尺度处理；
- `join` 用的是**替换**语义写 `seeds`：邀请码描述的就是该连的那台机器，
  追加只会把历史遗留的错误地址留着，反而更难排查；
- 邀请码里的地址优先取「到已有 peer 的出口网卡」——多网卡机器上
  「第一个私有 IP」经常不是对方能连到的那个（本机 172.24/172.26 两段，
  实测挑错会让邀请码直接不可用）。

### 用快捷键取用历史（Ctrl+Alt+V）

按下 `Ctrl+Alt+V` 会弹出一个选择窗口，`↑↓` 移动、**直接打字即过滤**、`Enter` 取回、`Esc` 取消。
取回走的是「只写本机剪贴板、不广播」的语义——不会把你历史里的旧内容再推给别人。

窗口是各平台原生自绘：

- **Linux**：X11 自绘窗口（`xgb` + 系统 CJK 字体栅格化，无 GTK/Qt 依赖）。找不到可用字体、
  连不上 X、或抢不到键盘时**自动降级到 `zenity`**（GNOME 自带），宁可换外观也不留一个
  点不动的空白窗口。
- **Windows**：Win32 自绘窗口（GDI 绘制，系统字体渲染），由常驻进程内的
  `RegisterHotKey` 触发。

**Linux 的快捷键是怎么来的**（重点：不需要你手动配）

GNOME 没有 Win32 `RegisterHotKey` 那样的进程内全局热键接口，全局组合键只能由桌面环境
绑到一个命令上。所以 clipshare **启动时会自己登记**这条绑定：

- 组合键：`<Control><Alt>v`，命令：`/usr/bin/clipshare pick`
- 槽位名：`clipshare`（追加到你的自定义快捷键列表里，**不会覆盖** flameshot 等已有项）
- 已经存在指向本程序的槽位时只修正命令路径（例如从手工拷贝路径改成 `/usr/bin/clipshare`）
- 组合键已被**别的程序**占用时不会抢占，只在日志里提示，由你决定怎么处理

启动日志里能看到结果（`journalctl --user -u clipshare -n 20`）：

```
已自动注册快捷键：<Control><Alt>v → /usr/bin/clipshare pick
已把快捷键命令更新为 "/usr/bin/clipshare pick"（绑定不变）
提示: 未自动注册快捷键: 组合键 <Control><Alt>v 已被「xxx」占用，未改动
```

非 GNOME 桌面（KDE/XFCE/...）不做自动登记，请自行在系统设置里把某个组合键绑到
`clipshare pick`。想自己换组合键，直接在「设置 → 键盘快捷键 → 自定义快捷键」里改
名为 `clipshare` 的那一项即可（程序不会再覆盖你改过的绑定）。

手动登记（自动注册失败时的等价命令）：

```bash
P=/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings/clipshare/
gsettings set org.gnome.settings-daemon.plugins.media-keys custom-keybindings \
  "$(gsettings get org.gnome.settings-daemon.plugins.media-keys custom-keybindings | sed "s/]$/, '$P']/; s/\['\]/['$P']/")"
gsettings set org.gnome.settings-daemon.plugins.media-keys.custom-keybinding:$P name 'clipshare 历史'
gsettings set org.gnome.settings-daemon.plugins.media-keys.custom-keybinding:$P command '/usr/bin/clipshare pick'
gsettings set org.gnome.settings-daemon.plugins.media-keys.custom-keybinding:$P binding '<Control><Alt>v'
```

> 注意：GNOME 的自定义快捷键命令**不做 `~` 展开**，必须写绝对路径，否则按了没反应。

`clipshare status` 可以在不开托盘的情况下看「与几台同步中」、房间码、历史条数与最近错误；
历史页顶部的状态条读的是同一份数据。命令行等价物：`clipshare pick`（需要 clipshare 正在运行；`--limit N` 控制显示条数）。

**拖动与位置记忆**：按住弹窗**顶部标题条**可以拖动（右侧有一排 2×3 的小点作为提示）；
拖动后的位置会被记住，下次弹出回到同一处（Windows 与 Linux 行为一致）。
位置存在配置目录的 `picker.pos` 里，换显示器或改分辨率后如果记的位置在屏幕外，
会自动夹回可见范围。想恢复居中，删掉该文件即可：

```bash
rm ~/.config/clipshare/picker.pos
```

### 跨网络同步（经中心点 A）

局域网 P2P 只在同一网段有效。要让两台**在不同网络**（各自在 NAT 后）的机器共享剪贴板，
需要一个双方都能访问的中心点 A 做中继 —— 它们各自只与 A 通信，内容由 A 转发。

**三种角色是三个独立开关，可任意组合**（不是互斥的模式）：

| 开关 | 作用 | 谁打开 |
|---|---|---|
| `lanEnabled`（默认 `true`） | 局域网对等：mDNS/seeds 发现 + 直连互推 | 同网段的机器 |
| `hubEnabled` + `hubListen` | 中继服务端：收下上行条目并下发 | 中心点 A |
| `relayURL` + `relayEnabled`（默认 `true`） | 中继客户端：上行到 A、从 A 拉取；`relayURL` 为空即不启用 | 不在同一网络里的机器 |

所以一台机器可以「在公司和同事直连（LAN）+ 在家通过 A 与公司机器同步（relay）」同时成立。

托盘菜单里有「启用局域网对等」「启用中心点中继」两个勾选项，点一下即切换；中继项的标题还会显示「已连通 / 未连通」。切换要重建传输层，因此实现为「写配置 + 自动重启生效」，不是热切换。

#### 1. 在 A 上跑中继

推荐用打包好的部署件（`make dist` 产出的 `dist/hub/`，含 systemd 单元、Docker 文件与 `install-hub.sh`）：

```bash
# 在 A 上（需要 root）；二进制与本目录的 install-hub.sh 放在一起
sudo ./install-hub.sh ./clipshare
# 可选：指定房间码与监听地址
sudo ROOM_KEY=2mnsn82k6b62 HUB_LISTEN=127.0.0.1:3011 ./install-hub.sh ./clipshare
```

脚本会建 `clipshare` 系统用户、装二进制到 `/usr/local/bin`、建 `/etc/clipshare`、装并启用 `clipshare-hub.service`，最后打印证书指纹。也可以直接跑二进制（Docker/Compose 跑法见 `dist/hub/README.md`）：

```bash
# A 上不需要图形会话：纯中继模式不会去碰剪贴板
./clipshare --config-dir ~/clipshare
```

`~/clipshare/config.json` 关键片段：

```json
{
  "roomKey": "2mnsn82k6b62",
  "lanEnabled": false,
  "hubEnabled": true,
  "hubListen": "127.0.0.1:3011",
  "hubKeep": 200,
  "persist": false
}
```

首次启动会自动生成自签证书（`hub-cert.pem` / `hub-key.pem`）并打印指纹：

```bash
clipshare hub-info --config-dir ~/clipshare
# 中继监听：127.0.0.1:3011
# 房间标识：f0f7751e5273
# 保留条数：200
# 明文模式：false（true 仅限内网调试）
# 证书指纹：sha256:4983af57ce763d83add94fe91c56fb5770be6ebdf8fee53ec7fabf357fa56ec7
#   ↑ 填到 B/C 的 relayPin
# 实时状态：在线节点 2，最新序号 41，房间 f0f7751e5273
```

**只监听回环 + 内网穿透**是推荐跑法：frp/nginx 把 `127.0.0.1:3011` 映射到一个公网端口，
中继本身不直接暴露在公网接口上。`hubListen` 默认是 `:8443`；经内网穿透时改成
`127.0.0.1:3011` 这类回环地址。

#### 2. 在 B/C 上指向中心点

```json
{
  "lanEnabled": true,
  "relayURL": "https://101.201.30.29:13004",
  "relayEnabled": true,
  "relayPin": "sha256:4983af57ce763d83add94fe91c56fb5770be6ebdf8fee53ec7fabf357fa56ec7",
  "relayIntervalMs": 800
}
```

`relayPin` 是 A 的证书指纹 —— 自签证书无法用 CA 校验，靠指纹钉确认「连的真是我的中心点」。
改完重启（托盘菜单或历史页的「重启」按钮）即可。

`clipshare status` 会同时报告两条链路：

```
状态：与 1 台同步中（局域网直连）
中心点：https://101.201.30.29:13004（已连通）
```

#### 3. 协议与语义（自建代理时需要知道）

| 方法 / 路径 | 作用 | 约束 |
|---|---|---|
| `POST /v1/items` | 上行一条剪贴板 | `Authorization: Bearer <roomKey>` + `X-Room: sha256(key)[:12]`；回 202 `{"seq":n}` |
| `GET /v1/feed?since=&wait=` | 下行拉增量（长轮询） | `wait` 上限 5s；省略 `since` 只回游标；回 `{"items":[],"lastSeq":n,"peers":m}` |
| `GET /v1/health` | 探活 | 免鉴权，回 `role=hub`、`peers`、`lastSeq` |

因此自建反向代理时：`/v1/feed` 必须允许**长连接 > 5s** 且不能缓冲响应体；
若要保留指纹钉，中间层必须做 **TCP 透传**（不做 TLS 终止）。

#### 4. 安全边界（与局域网模式的区别）

- **A 能读到所有经过它的剪贴板内容**。中继是明文转发（不做端到端加密），
  所以 A 必须是**你自己的机器**。
- 中继**强制 TLS**（自签 + 指纹钉），只有显式 `hubAllowPlaintext: true` 才允许明文，仅限内网调试。
- 公网暴露时建议房间码加长（默认 12 位 ≈59 bit）；中继每 IP 每分钟默认限流 120 条。
- 中心点不可用时，客户端会指数退避重连（1s→30s），本机历史与局域网同步不受影响。

#### 5. 已知取舍

- 下行用长轮询而非 WebSocket：穿 NAT/代理更稳、零额外依赖，代价是延迟在 1 秒量级。
- A 只保留最近 `hubKeep` 条（默认 200）供断线补收；`hubKeep: 0` 即纯转发不保留。
- 客户端游标只在内存：进程重启后从中心点当前值开始，不回放历史（避免一次性灌爆剪贴板）。

### 重启（不用杀进程）

改完 `config.json`、换了房间码、或想让程序重新注册快捷键之后，两处都能一键重启：

- **托盘菜单**：`重启（重新加载配置）`
- **本地历史页**：右上角「重启」按钮，对应 `POST /ui/api/restart`（返回 202，页面随后自动刷新）

重启是「正常退出 → 用同一套参数重新拉起」：先把剪贴板轮询停掉、历史落盘、历史页端口释放，
再启动新实例，所以不会丢历史，也不会出现新进程抢不到端口的竞态。

- Linux 用 `exec` 替换进程映像，**PID 不变**（systemd 的 MainPID 不会抖，日志里会看到
  `收到重启请求` → `[engine] 已停止` → 新的启动序列）。
- Windows 不能 exec，改为隐藏窗口延迟 2 秒启动新实例（必须等旧进程释放 `47100` 端口），
  所以会有约 2 秒空窗。


### Windows 上没有黑窗口

Windows 版编成 **GUI 子系统**（`-H windowsgui`），双击或用计划任务启动都**不会弹出控制台黑窗**。
代价是它没有标准输出，所以日志会同时写到配置目录下的文件：

```
%APPDATA%\clipshare\clipshare.log
```

排障时直接看这个文件（超过 2MB 会在下次启动时轮转成 `.log.1`）。Linux 侧同样会写
`~/.config/clipshare/clipshare.log`，另外 systemd 还收了 journal：`journalctl --user -u clipshare -f`。

需要 `--help` / `--version` 这类命令行输出时，用控制台变体：

```bash
make build-windows-console   # → bin/clipshare-console.exe（会开控制台，便于排障）
```

开机自启（Windows）用的是登录触发的计划任务 `clipshare-autostart`，可直接重置：

```powershell
schtasks /create /tn clipshare-autostart /tr '"C:\Users\<你>\clipshare.exe" --port 47100' /sc onlogon /it /f
```


## 4. 禁多播网络怎么办（企业 Wi-Fi / 隔离 VLAN）

mDNS 依赖多播。多播被禁时，**同一网段内的唯一可用路径**是关掉 mDNS、改用手填的 seeds：在各台机器的 `config.json` 里改这两个字段（其余字段保持原样）。如果两台机器根本不在同一网络，见第 3 节的「跨网络同步（经中心点 A）」。

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

局域网模式是**内网信任模型**，没有 TLS、没有账号体系。以下每条都请当成事实（跨网络中继额外有自签 TLS，见第 3 节）：

- **局域网明文传输**：HTTP/1.1 + JSON，同网段可被嗅探，也可被注入。
- **中继链路有 TLS，但不做端到端加密**：B/C 与 A 之间是自签 TLS + 指纹钉；A 能看到明文内容，所以 A 必须是自己的机器。`relayPin` 留空等于不校验证书，不推荐。
- **同房间内任何人，既能读取这条同步流，也能直接往你的剪贴板写内容**——`/v1/items` 收到即写入历史，`autoApply` 打开时还会写进本机剪贴板。
- **房间密钥只解决「谁能接入」**，不提供保密性，也不防重放。
- **房间码是唯一凭证，长度必须够**：`/v1/health` 与 mDNS TXT 都会公开 `sha256(roomKey)` 的前 12 位（48 bit）。拿到这个 hash 就能**离线枚举**候选 key 并用它验证——**6 位房间码只有 31^6 ≈ 8.9×10^8 ≈ 29.7 bit，单核分钟级即可爆破**，等于把整个房间（读剪贴板流 + 往你剪贴板写内容）交出去。当前版本首启生成 **12 位（31^12 ≈ 59.5 bit）**；旧的 6 位配置仍可用，但请改成 12 位以上，且多机对齐时从 `config.json` **复制粘贴**，不要口述/手抄。
- **启动日志会打印完整房间码**（方便直接复制到另一台机器）：贴日志、截图或提 issue 前请先把房间码打码，否则等于把房间凭证公开。
- `/v1/health` **免鉴权**：同网段的人可以探测某个房间 hash 是否存在（但不会泄露 `roomKey` 本身）。
- **历史默认明文落盘**（`history.json`，权限 0600）：不想落盘就 `"persist": false` 或加 `--no-persist`（内存历史仍可用）。
- **Web UI 只绑 `127.0.0.1`、不做鉴权**：能访问本机这个端口的人就能读历史、写剪贴板。

不要在不可信网络（咖啡店 Wi-Fi、公共热点）里开。局域网 P2P 仍是明文 HTTP；跨网络请只连自己的中心点 A，并按第 3 节配置 `relayPin`。

## 6. 已知行为（有意的取舍）

1. **同一内容重复复制不会再广播**：轮询用「上次读到的内容」判重，你再次复制与剪贴板当前完全相同的内容不会产生事件；远端同理，服务端按内容去重，重复条目回 `202` 后直接丢弃。代价是这条记录不会在其他机器上刷新时间戳。
2. **所有排序与显示都用本机接收时间**（`recvAt`）：跨机时钟漂移不影响顺序，但显示的是「我收到的时刻」，不是发送方的时间。
3. **图片 / 非文本**：剪贴板里没有文本时视为「无变化」，不入历史、不广播。
4. **不重试**：广播失败或超时（3s）只记日志——剪贴板内容有过期语义，延迟重发会造成「你以为复制了 A，剪贴板后来跳成 B」的时序错乱。
5. **暂停是「外向」开关**：不广播本机内容、不写本机剪贴板；但**远端条目仍会进入本地历史**（历史与剪贴板是两条独立通道，与关闭「自动应用」时的行为一致）。
6. **同一台机器跑两个实例联调时，历史来源可能显示成「另一个实例」**：两个进程共享同一个剪贴板——A 收到远端内容写进剪贴板后，B 的轮询会把同一份内容识别成「本机复制」，于是 B 的历史里来源显示为 B 而不是 A。这**既不是转发也不是回环**：服务端按内容去重（重复条目回 `202` 后丢弃），不会扩散；真实的两台机器各有独立剪贴板，也不会出现。单机自测时别把这条当 bug，用两份内容或两台机器区分即可。

## 7. 配置项（`config.json`）

字段与 `internal/config/config.go` 的 `Config` JSON 字段一一对应（`Dir` 是运行期字段，不落盘）。所有字段都会被写回文件；手改时只改需要的字段，**其它字段请保留原值**（缺字段会回落默认值，`deviceId` / `roomKey` 缺失会被重新生成）。

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
| `lanEnabled` | bool | `true` | 局域网对等角色：是否做 mDNS/seeds 发现与同网段直连互推 |
| `hubEnabled` | bool | `false` | 中继服务端角色（中心点 A）：收下上行条目并下发 |
| `hubListen` | string | `:8443` | 中继监听地址；`hubEnabled` 为 true 时必填，经内网穿透时建议 `127.0.0.1:3011` |
| `hubKeep` | int | `200` | 每房间保留的最近条目数（0 = 纯转发不保留）。取值 0–10000 |
| `hubCertFile` / `hubKeyFile` | string | `""` | 中继证书/私钥路径；留空则用配置目录下的 `hub-cert.pem` / `hub-key.pem`。两者必须同时提供或同时留空 |
| `hubAllowPlaintext` | bool | `false` | 允许中继明文监听；仅限内网调试，公网暴露等于裸奔 |
| `relayURL` | string | `""` | 中心点地址（`http(s)://host:port`）；非空且 `relayEnabled` 为 true 时才做中继客户端 |
| `relayEnabled` | bool | `true` | 中继客户端开关；关掉后保留 `relayURL`，便于在托盘里一键切换 |
| `relayPin` | string | `""` | 中心点证书 SHA-256 指纹（`sha256:<64 位 hex>`）；留空 = 不校验，不推荐 |
| `relayIntervalMs` | int | `1000` | 长轮询挂起时长与重试节奏（毫秒）。取值 100–30000；仅在 `relayURL` 非空时校验 |

三条传输（`lanEnabled` / `hubEnabled` / `relayURL` + `relayEnabled`）**至少要启用一条**；全关会报错，因为那样程序跑着却不与任何机器通信。`relayEnabled` 默认 true、`relayURL` 默认为空——这是为了兼容纯局域网老配置：**没有地址时不会因为开关为 true 就要求填地址**。

配置文件损坏时：原文件备份为 `config.json.bak`，然后用默认值重建（`deviceId` / `roomKey` 会变，需要重新对齐房间码）。

## 8. 命令行

**子命令**（`clipshare <子命令> [选项]`，都支持 `--config-dir`）：

| 子命令 | 作用 |
|---|---|
| `clipshare init` | 交互式首次配置：选「我是第一台机器」或「加入已有房间」 |
| `clipshare invite` | 打印本机邀请码（房间码 + 对方地址） |
| `clipshare join <邀请码>` | 写入房间码与 seeds，并让正在运行的实例重启生效 |
| `clipshare status` | 打印节点、房间、状态、历史条数与中心点连通性（不开托盘也能排障） |
| `clipshare pick [--limit N]` | 弹出历史选择窗口（`Ctrl+Alt+V` 调用的就是它） |
| `clipshare hub-info` | 打印中继监听、证书指纹、保留条数与在线节点（部署中心点后自检用） |

**启动参数**：

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
| `--startup-delay <duration>` | 内部用：启动后延迟再监听端口（重启时避免与旧进程抢端口） |

- flag 优先级高于配置文件；但**只影响本次运行，不写回配置文件**（实测：带 `--room flag-room --port 47999` 启动后重读 `config.json`，值仍是原样）。
- 完整列表与文案以 `./clipshare --help` 为准（附录有实跑原文）。
- `--config-dir` 会一路生效到托盘开关：切换「自动应用 / mDNS / 局域网对等 / 中心点中继」时写回的是**这个目录**的 `config.json`，不会在你的默认配置目录留下第二份（有回归测试守着）。

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
- **跨网络同步不通**：先跑 `clipshare status` 看「中心点：…（已连通 / 未连通）」。`relayURL` 没配、`relayEnabled` 被关、`relayPin` 与 A 的证书指纹不一致、代理终止了 TLS（必须 **TCP 透传**）、或 A 上的中继没在跑，都会显示未连通。A 上跑 `clipshare hub-info --config-dir /etc/clipshare` 看「在线节点」是否 ≥1。
- **Linux 报缺剪贴板命令**：X11 下报 `缺少剪贴板命令 xclip/xsel：请安装其一，例如 sudo apt install xclip（或 sudo apt install xsel）`；Wayland 下报 `Wayland 会话缺少剪贴板命令 wl-paste、wl-copy：请安装 wl-clipboard：sudo apt install wl-clipboard`——按提示装即可。若你其实在 X11 会话却带着 `WAYLAND_DISPLAY`，清掉该变量再启动。
- **按 `Ctrl+Alt+V` 没反应**：先跑 `clipshare status` 看 `历史：N 条`。
  - **历史为空**时旧版本会静默退出（现在改成照样弹窗并提示「还没有剪贴板历史」）；
  - 历史不为空却仍无反应，就看 `journalctl --user -u clipshare -n 20` 里「快捷键」那一行，
    确认它是 `已就绪` 还是提示被别的程序占用。
- **连按快捷键想关掉窗口**：现在同一时刻只允许一个选择窗口（`picker.lock` 文件锁），
  连按不会叠窗口；关掉用 `Esc`（或点某一行取回）。
- **无图形会话**：报 `未检测到图形会话（DISPLAY 与 WAYLAND_DISPLAY 均未设置），无法访问剪贴板` ——剪贴板必须有桌面会话，SSH/headless 下不可用。
- **配置被重置**：日志提示配置损坏时，同目录的 `config.json.bak` 是原文件；重建后 `deviceId` / `roomKey` 变了，需要重新对齐房间码。
- **历史落盘失败**（磁盘满 / 无权限）：内存历史继续可用，日志每分钟最多一条告警。

## 10. 开发

```bash
make vet                    # go vet ./...
make test                   # go test ./... -race -count=1
make fmt                    # gofmt -l -w .（会改写文件）
make build-linux            # → bin/clipshare      CGO_ENABLED=0 GOOS=linux
make build-windows          # → bin/clipshare.exe  CGO_ENABLED=0 GOOS=windows（GUI 子系统）
make build-windows-console  # → bin/clipshare-console.exe（保留控制台，排障用）
make all                    # vet + test + 双平台构建（默认目标）
make dist                   # 打安装包与文档到 dist/（deb、zip、tgz、hub 部署件、使用手册）
make clean                  # 删除 bin/
```

两个 build 目标默认 `GOARCH=amd64`，可用 `make build-linux GOARCH=arm64` 覆盖（`-race` 只能在本机跑，交叉产物不参与测试）。版本号 `VERSION ?= 1.0.0`，经 `-ldflags -s -w -X main.version=$(VERSION)` 注入，可用 `make build-linux VERSION=1.0.1` 覆盖。

目录结构（一行一包）：

```
cmd/clipshare/     入口：解析 flag / 子命令、加载配置、组装并启动
internal/model     条目类型、内容归一化、内容 hash / 房间 hash
internal/config    配置定位、默认值、加载、损坏备份重建
internal/history   环形历史、内容去重、原子落盘
internal/clip      剪贴板读写适配器 + 轮询变更检测
internal/sync      HTTP 收发、房间鉴权、并发广播
internal/discovery mDNS 公告/订阅 + 静态 seeds 探活
internal/engine    组装上述组件，独占同步语义（S1–S8）
internal/relay     中继客户端（B/C）：上行 + 长轮询下拉
internal/hub       中继服务端（A）：鉴权、限流、保留与下发
internal/tlscert   自签证书生成与 SHA-256 指纹
internal/onboard   邀请码的生成与解析
internal/hotkey    全局热键（Linux/GNOME 与 Windows）
internal/picker    历史选择窗口与 HTTP 客户端
internal/tray      托盘菜单与图标（含 --no-tray 降级）
internal/webui     本地历史页（只绑 127.0.0.1）
packaging/         安装包、使用手册与中继部署件
docs/superpowers/specs/  设计文档与任务拆解
```

## 附录：验收记录

环境：Ubuntu 20.04.6 LTS，`go1.24.0 linux/amd64`，`DISPLAY=:1`（无 `WAYLAND_DISPLAY`），已装 `xsel`。
以下 `make all`、`--version` 与 `--help` 为 1.0.0 在 2026-11-14 的实跑原文；运行期与真机验收记录保留 0.1.0 时期的原文并另行标注。

```console
$ make all
go vet ./...
go test ./... -race -count=1
ok  	clipshare/cmd/clipshare	1.324s
ok  	clipshare/internal/clip	1.773s
ok  	clipshare/internal/config	1.025s
ok  	clipshare/internal/discovery	1.319s
ok  	clipshare/internal/engine	14.626s
ok  	clipshare/internal/history	1.148s
ok  	clipshare/internal/hotkey	1.010s
ok  	clipshare/internal/hub	1.336s
ok  	clipshare/internal/model	1.012s
ok  	clipshare/internal/onboard	1.010s
ok  	clipshare/internal/picker	1.326s
ok  	clipshare/internal/relay	2.892s
ok  	clipshare/internal/sync	1.332s
ok  	clipshare/internal/tlscert	1.018s
ok  	clipshare/internal/tray	1.056s
ok  	clipshare/internal/webui	1.023s
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=1.0.0" -o bin/clipshare ./cmd/clipshare
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=1.0.0 -H windowsgui" -o bin/clipshare.exe ./cmd/clipshare

$ ls -l bin/
总用量 18740
-rwx------ 1 haitang haitang 10784952 11月 14 23:55 clipshare
-rwx------ 1 haitang haitang  8400384 11月 14 23:55 clipshare.exe

$ file bin/clipshare bin/clipshare.exe
bin/clipshare:     ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, BuildID[sha1]=6c81aa2079d40a9939accc549f55b266347a997b, stripped
bin/clipshare.exe: PE32+ executable (GUI) x86-64, for MS Windows
```

### 运行期行为（`--no-tray`，0.1.0 实测，真实剪贴板）

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

### 真机验收记录（0.1.0 时期，Ubuntu 22.04 X11 + Windows 10 双机实测）

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

### `--help` 原文（1.0.0）

```console
$ ./bin/clipshare --help
clipshare 1.0.0 —— 局域网剪贴板共享

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
  -startup-delay duration
    	内部用：启动后延迟指定时间再监听端口（重启时避免与旧进程抢端口）
  -version
    	打印版本号后退出
```

### 仍需人工验收（代码跑不出来）

已由真机验收覆盖、不再是待办：**两台真机互相同步**（Windows → Linux 实测通过，见上文「真机验收记录」）、**A→B→A 不回环**（`TestIntegrationNoLoopback` 断言 B 广播 0 次 + 双进程实测双向不转发）、**禁多播网络的 `mdns: false` + `seeds` 连通**（上面双机验收走的就是这条路径）、**Windows 侧实际运行**（真实 Windows 10 上跑通）、**Windows 的 `Ctrl+Alt+V` 自绘窗口**（用户真机确认：窗口弹出、`↓` 高亮移动、`Enter` 关闭并取回、粘贴内容正确 —— 即焦点抢占与键盘处理都正常）。

剩下的只有一类，都需要人坐在电脑前看屏幕或点鼠标：

- **托盘菜单逐项点击**：取回历史、发送当前剪贴板、勾选自动应用 / mDNS、切换「启用局域网对等 / 启用中心点中继」、查看全部历史、退出（Linux 侧已验证图标注册与注销，菜单项的点击效果未逐个验）。
- **Windows 托盘图标的视觉效果**（SSH 会话跑不出可见托盘，需要交互式桌面会话）。
- **真实多播环境下的 mDNS 自动发现**（双机验收刻意用了 `--mdns=false` 的确定性路径）。

依赖表依据：`apt-cache policy xclip xsel wl-clipboard` 显示三者在本机 focal/universe 仓库均有候选包（`xclip 0.13-1`、`xsel 1.2.0+git9bfc13d.20180109-3`、`wl-clipboard 2.0.0-1`），本机已装 `xsel`。
