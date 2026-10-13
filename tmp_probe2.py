import json, os, subprocess, tempfile

exe = r"C:\config\plugins\tool.unlock\bin\unlock-plugin-windows-amd64.exe"
work = tempfile.mkdtemp(prefix="unlock-probe-")
def probe(op, **args):
    inp = os.path.join(work, "in.json")
    out = os.path.join(work, "out.json")
    if os.path.exists(out): os.remove(out)
    with open(inp, "w", encoding="utf-8") as f:
        json.dump({"op": op, **args}, f, ensure_ascii=False)
    try:
        r = subprocess.run([exe, "run", "--input", inp, "--output", out], capture_output=True, text=True, timeout=30)
        res = ""
        if os.path.exists(out):
            with open(out, encoding="utf-8") as f:
                res = f.read()[:300]
        print(f"op={op}: rc={r.returncode} out={res!r}")
    except Exception as e:
        print(f"op={op}: EXC {e}")

for op in ["unlock", "decode", "convert", "info", "detect", "batch", "list", "ping", "status"]:
    probe(op)

# 也搜一下 exe 里的可打印字符串，找 op 名称
data = open(exe, "rb").read()
import re
strs = re.findall(rb"[\x20-\x7e]{4,}", data)
hits = [s.decode() for s in strs if any(k in s.lower() for k in [b"op", b"unlock", b"decode", b"input", b"file"])][:40]
print("---- strings hits ----")
for h in hits:
    print(h[:120])
