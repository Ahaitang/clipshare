#!/usr/bin/env python3
"""把用户手册模板渲染成单文件 HTML（图片以 base64 内嵌，可离线分发）。

用法：build-guide.py <仓库根> <输出目录>
"""
import base64, pathlib, sys

root = pathlib.Path(sys.argv[1])
out = pathlib.Path(sys.argv[2])
# 源图放在 packaging/docs/images（被 git 跟踪），渲染时复制到输出目录
src_images = root / "packaging" / "docs" / "images"
images = out / "images"

def b64(name):
    p = src_images / name
    return base64.b64encode(p.read_bytes()).decode() if p.exists() else ""

ARCH_SVG = """<svg viewBox="0 0 900 420" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="clipshare 架构图">
  <defs>
    <marker id="a" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="#5b6470"/>
    </marker>
    <style>
      .box{fill:#f6f8fa;stroke:#c9d1d9;stroke-width:1.5;rx:10}
      .inner{fill:#fff;stroke:#c9d1d9;stroke-width:1.2;rx:7}
      .t{font:14px -apple-system,"Segoe UI","Noto Sans CJK SC",sans-serif;fill:#1f2328}
      .tb{font:600 15px -apple-system,"Segoe UI","Noto Sans CJK SC",sans-serif;fill:#1f2328}
      .ts{font:12px -apple-system,"Segoe UI","Noto Sans CJK SC",sans-serif;fill:#57606a}
      .flow{stroke:#5b6470;stroke-width:1.6;fill:none;marker-end:url(#a)}
      .flow2{stroke:#8b949e;stroke-width:1.4;fill:none;stroke-dasharray:5 4;marker-end:url(#a)}
    </style>
  </defs>

  <rect class="box" x="12" y="24" width="400" height="360" rx="12"/>
  <text class="tb" x="30" y="52">机器 A · Ubuntu</text>
  <rect class="inner" x="34" y="70" width="356" height="54" rx="8"/>
  <text class="t" x="50" y="93">剪贴板轮询（400ms，内容变化才算新条目）</text>
  <text class="ts" x="50" y="112">xclip / xsel / wl-paste；自身写入用 MarkSeen 抑制回播</text>

  <rect class="inner" x="34" y="136" width="356" height="48" rx="8"/>
  <text class="t" x="50" y="158">历史（环形 500 条，明文落盘）</text>
  <text class="ts" x="50" y="175">同内容置顶去重，带来源标记与本地接收时间</text>

  <rect class="inner" x="34" y="196" width="356" height="48" rx="8"/>
  <text class="t" x="50" y="218">HTTP 服务 0.0.0.0:47100</text>
  <text class="ts" x="50" y="235">POST /v1/items（Bearer + X-Room 双重校验）</text>

  <rect class="inner" x="34" y="256" width="356" height="48" rx="8"/>
  <text class="t" x="50" y="278">发现：mDNS 组播 + 静态 seeds 探活</text>
  <text class="ts" x="50" y="295">只认同房间（room hash 一致），按 deviceId 去重</text>

  <rect class="inner" x="34" y="316" width="356" height="46" rx="8"/>
  <text class="t" x="50" y="338">界面：托盘菜单 · Ctrl+Alt+V 选择窗口</text>
  <text class="ts" x="50" y="354">本地历史页 127.0.0.1:47101/ui（只绑回环，无鉴权）</text>

  <rect class="box" x="488" y="24" width="400" height="360" rx="12"/>
  <text class="tb" x="506" y="52">机器 B · Windows 10/11</text>
  <rect class="inner" x="510" y="70" width="356" height="54" rx="8"/>
  <text class="t" x="526" y="93">剪贴板读写（Win32 API，不依赖外部命令）</text>
  <text class="ts" x="526" y="112">OpenClipboard 被占用时退避重试 3 次</text>

  <rect class="inner" x="510" y="136" width="356" height="48" rx="8"/>
  <text class="t" x="526" y="158">历史（同上）</text>
  <text class="ts" x="526" y="175">配置与历史在 %APPDATA%\\clipshare</text>

  <rect class="inner" x="510" y="196" width="356" height="48" rx="8"/>
  <text class="t" x="526" y="218">HTTP 服务 0.0.0.0:47100</text>
  <text class="ts" x="526" y="235">安装包自动放行防火墙入站</text>

  <rect class="inner" x="510" y="256" width="356" height="48" rx="8"/>
  <text class="t" x="526" y="278">发现：同上</text>
  <text class="ts" x="526" y="295">禁多播时靠 seeds 互指（本项目实测网络形态）</text>

  <rect class="inner" x="510" y="316" width="356" height="46" rx="8"/>
  <text class="t" x="526" y="338">界面：托盘菜单 · Ctrl+Alt+V（进程内注册）</text>
  <text class="ts" x="526" y="354">GUI 子系统，无控制台黑窗</text>

  <path class="flow" d="M412 220 L486 220"/>
  <path class="flow" d="M486 240 L412 240"/>
  <text class="ts" x="404" y="212">广播条目</text>
  <text class="ts" x="404" y="258">接收并应用</text>

  <path class="flow2" d="M412 286 C440 300, 460 300, 486 286"/>
  <text class="ts" x="404" y="320">发现 / 探活</text>
  <text class="ts" x="300" y="404" text-anchor="middle">对等 P2P：每台机器既是服务端也是客户端，无中心节点；房间密钥只以 Bearer 头出网</text>
</svg>"""

FLOW_SVG = """<svg viewBox="0 0 900 300" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="首次配置流程">
  <defs>
    <marker id="b" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="#5b6470"/>
    </marker>
    <style>
      .card{fill:#f6f8fa;stroke:#c9d1d9;stroke-width:1.5;rx:10}
      .step{fill:#fff;stroke:#c9d1d9;stroke-width:1.2;rx:8}
      .t{font:13.5px -apple-system,"Segoe UI","Noto Sans CJK SC",sans-serif;fill:#1f2328}
      .ts{font:12px -apple-system,"Segoe UI","Noto Sans CJK SC",sans-serif;fill:#57606a}
      .tb{font:600 14px -apple-system,"Segoe UI","Noto Sans CJK SC",sans-serif;fill:#1f2328}
      .arr{stroke:#5b6470;stroke-width:1.6;fill:none;marker-end:url(#b)}
      .code{font:12px ui-monospace,Consolas,monospace;fill:#0a3069}
    </style>
  </defs>

  <rect class="card" x="10" y="16" width="420" height="252" rx="12"/>
  <text class="tb" x="28" y="44">第一台机器</text>
  <rect class="step" x="28" y="58" width="384" height="46"/>
  <text class="t" x="42" y="78">1. 装好 clipshare</text>
  <text class="ts" x="42" y="95">deb / setup.exe，装完自动启动并注册 Ctrl+Alt+V</text>
  <rect class="step" x="28" y="116" width="384" height="52"/>
  <text class="t" x="42" y="136">2. 生成邀请码</text>
  <text class="code" x="42" y="156">clipshare invite</text>
  <rect class="step" x="28" y="180" width="384" height="70"/>
  <text class="t" x="42" y="200">3. 把邀请码发给对方</text>
  <text class="code" x="42" y="222">clipshare1:2mnsn82k6b62@172.26.81.147:47100</text>
  <text class="ts" x="42" y="240">＝房间码 + 本机地址（禁多播也能连）</text>

  <path class="arr" d="M434 142 L470 142"/>
  <text class="ts" x="430" y="130">发给</text>

  <rect class="card" x="478" y="16" width="412" height="252" rx="12"/>
  <text class="tb" x="496" y="44">新机器</text>
  <rect class="step" x="496" y="58" width="376" height="46"/>
  <text class="t" x="510" y="78">1. 装好同一版本</text>
  <text class="ts" x="510" y="95">首次启动会生成一份随机房间（还没对上）</text>
  <rect class="step" x="496" y="116" width="376" height="52"/>
  <text class="t" x="510" y="136">2. 加入房间（两种方式）</text>
  <text class="code" x="510" y="156">clipshare join 'clipshare1:…'  /  历史页「房间与邀请」</text>
  <rect class="step" x="496" y="180" width="376" height="70"/>
  <text class="t" x="510" y="200">3. 自动写入 roomKey + seeds 并重启</text>
  <text class="ts" x="510" y="220">无需手工改配置；重启后立即开始同步</text>
  <text class="ts" x="510" y="238">验证：托盘状态行显示「与 1 台同步中」</text>

  <text class="ts" x="450" y="288" text-anchor="middle">邀请码只含房间码与地址、不含设备标识；但它等价于房间钥匙，按「发给可信同事」的尺度处理</text>
</svg>"""

html = pathlib.Path(root / "packaging/docs/guide.tmpl.html").read_text(encoding="utf-8")
html = (html
        .replace("{{ARCH_SVG}}", ARCH_SVG)
        .replace("{{FLOW_SVG}}", FLOW_SVG)
        .replace("{{PICKER_PNG}}", b64("picker.png"))
        .replace("{{WEBUI_PNG}}", b64("webui.png"))
        .replace("{{ANDROID_PNG}}", b64("android-sender.png")))
images.mkdir(parents=True, exist_ok=True)
for f in src_images.glob("*.png"):
    images.joinpath(f.name).write_bytes(f.read_bytes())
out.joinpath("guide.html").write_text(html, encoding="utf-8")
print("已生成", out / "guide.html", len(html), "字节")
