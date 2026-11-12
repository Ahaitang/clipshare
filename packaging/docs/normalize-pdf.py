#!/usr/bin/env python3
"""把 Chrome 生成的 PDF 里的创建/修改时间固定下来，让打包可复现。

为什么不用改长度：PDF 的 xref 表存的是字节偏移，替换串必须**等长**，
否则偏移全错、文件损坏。所以固定成一个同样 22 字符的日期串。
"""
import re, sys

FIXED = b"D:19700101000000+00'00'"
path = sys.argv[1]
data = open(path, "rb").read()

def repl(m):
    return m.group(1) + FIXED + b")"

new = re.sub(rb"(/(?:CreationDate|ModDate)\s*\()([^)]*)\)", repl, data)
# Chrome 还会写入一个随机的文档 ID，一并去掉（它不是必需字段）
new = re.sub(rb"/ID\s*\[<[0-9a-fA-F]+>\s*<[0-9a-fA-F]+>\]", b"/ID [<00000000000000000000000000000000> <00000000000000000000000000000000>]", new)

if new != data:
    open(path, "wb").write(new)
    print(f"已规范化 {path}（{len(data)} → {len(new)} 字节）", file=sys.stderr)
