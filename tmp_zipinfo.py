import zipfile, json
p = r"E:\项目\unlock-plugin.zip"
with zipfile.ZipFile(p) as z:
    for n in z.namelist():
        info = z.getinfo(n)
        print(f"{n}  ({info.file_size} bytes)")
    print("---- plugin.json ----")
    print(z.read("plugin.json").decode("utf-8", "replace"))
