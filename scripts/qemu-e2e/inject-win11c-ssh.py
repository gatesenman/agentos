#!/usr/bin/env python3
"""Inject offline OpenSSH fix into live Win11c via QMP Win+R + schtasks SYSTEM."""
import json, socket, sys, time

CHUNKS = ['JEVycm9yQWN0aW9uUHJlZmVyZW5jZSA9ICdDb250aW51ZScKJGxvZyA9ICdDOlxQcm9ncmFtRGF0YVxBZ2VudE9TXGluamVjdC1maXgubG9nJwpOZXctSXRlbSAtSXRlbVR5cGUgRGlyZWN0b3J5IC1Gb3JjZSBD', 'OlxQcm9ncmFtRGF0YVxBZ2VudE9TLEM6XFByb2dyYW1EYXRhXHNzaCB8IE91dC1OdWxsClN0YXJ0LVRyYW5zY3JpcHQgLVBhdGggJGxvZyAtQXBwZW5kIHwgT3V0LU51bGwKZnVuY3Rpb24gU3RlcCgkbSl7IFdy', 'aXRlLU91dHB1dCAoIlt7MH1dIHsxfSIgLWYgKEdldC1EYXRlIC1Gb3JtYXQgcyksICRtKSB9CiRtc2kgPSAkbnVsbApmb3JlYWNoICgkbiBpbiA2OC4uNzUpIHsKICAkcCA9ICgnezB9OlxPcGVuU1NILVdpbjY0', 'Lm1zaScgLWYgW2NoYXJdJG4pCiAgaWYgKFRlc3QtUGF0aCAkcCkgeyAkbXNpID0gJHA7IGJyZWFrIH0KfQppZiAoJG1zaSkgewogIFN0ZXAgIm1zaWV4ZWMgJG1zaSIKICAkcCA9IFN0YXJ0LVByb2Nlc3MgbXNp', 'ZXhlYy5leGUgLVdhaXQgLVBhc3NUaHJ1IC1Bcmd1bWVudExpc3QgJy9pJywkbXNpLCcvcW4nLCcvbm9yZXN0YXJ0JwogIFN0ZXAgKCJtc2lleGVjIGV4aXQ9JCgkcC5FeGl0Q29kZSkiKQp9IGVsc2UgeyBTdGVw', 'ICdNU0kgbm90IGZvdW5kIG9uIEQtSycgfQokcHViID0gJ3NzaC1lZDI1NTE5IEFBQUFDM056YUMxbFpESTFOVEU1QUFBQUlPUzJUYjQvRi9sd3orMml5bVBYNzAzd05Zb1EwYUFuMGVWalE2aEJVWFA0IGFnZW50', 'b3MnClNldC1Db250ZW50IEM6XFByb2dyYW1EYXRhXHNzaFxhZG1pbmlzdHJhdG9yc19hdXRob3JpemVkX2tleXMgJHB1YiAtRW5jb2RpbmcgYXNjaWkKaWNhY2xzIEM6XFByb2dyYW1EYXRhXHNzaFxhZG1pbmlz', 'dHJhdG9yc19hdXRob3JpemVkX2tleXMgL2luaGVyaXRhbmNlOnIgL2dyYW50ICcqUy0xLTUtMzItNTQ0OkYnIC9ncmFudCAnKlMtMS01LTE4OkYnIHwgT3V0LU51bGwKU2V0LVNlcnZpY2Ugc3NoZCAtU3RhcnR1', 'cFR5cGUgQXV0b21hdGljIC1FcnJvckFjdGlvbiBTaWxlbnRseUNvbnRpbnVlClN0YXJ0LVNlcnZpY2Ugc3NoZCAtRXJyb3JBY3Rpb24gU2lsZW50bHlDb250aW51ZQppZiAoLW5vdCAoR2V0LU5ldEZpcmV3YWxs', 'UnVsZSAtTmFtZSAnT3BlblNTSC1TZXJ2ZXItSW4tVENQJyAtRXJyb3JBY3Rpb24gU2lsZW50bHlDb250aW51ZSkpIHsKICBOZXctTmV0RmlyZXdhbGxSdWxlIC1OYW1lICdPcGVuU1NILVNlcnZlci1Jbi1UQ1An', 'IC1EaXNwbGF5TmFtZSAnT3BlblNTSCBTZXJ2ZXIgKHNzaGQpJyAtRW5hYmxlZCBUcnVlIC1EaXJlY3Rpb24gSW5ib3VuZCAtUHJvdG9jb2wgVENQIC1BY3Rpb24gQWxsb3cgLUxvY2FsUG9ydCAyMiB8IE91dC1O', 'dWxsCn0KJG9rID0gW2Jvb2xdKEdldC1TZXJ2aWNlIHNzaGQgLUVycm9yQWN0aW9uIFNpbGVudGx5Q29udGludWUgfCBXaGVyZS1PYmplY3QgeyAkXy5TdGF0dXMgLWVxICdSdW5uaW5nJyB9KQokc3RhdGUgPSBp', 'ZiAoJG9rKSB7ICdzc2g9cnVubmluZycgfSBlbHNlIHsgJ3NzaD1GQUlMJyB9ClN0ZXAgImRvbmUgJHN0YXRlIgpAIgokc3RhdGUKJChHZXQtRGF0ZSAtRm9ybWF0IG8pCiJAIHwgU2V0LUNvbnRlbnQgQzpcUHJv', 'Z3JhbURhdGFcQWdlbnRPU1xyZWFkeS50eHQgLUVuY29kaW5nIGFzY2lpClN0b3AtVHJhbnNjcmlwdCB8IE91dC1OdWxsCg==']

class QMP:
    def __init__(s, path):
        s.s = socket.socket(socket.AF_UNIX); s.s.connect(path); s.f = s.s.makefile("rw")
        s.f.readline(); s.cmd("qmp_capabilities")
    def cmd(s, ex, **a):
        s.f.write(json.dumps({"execute": ex, "arguments": a} if a else {"execute": ex}) + "\n"); s.f.flush()
        while True:
            r = json.loads(s.f.readline())
            if "return" in r or "error" in r: return r
    def keys(s, *qc):
        return s.cmd("send-key", keys=[{"type": "qcode", "data": k} for k in qc])

SPECIAL = {
    " ": "spc", "-": "minus", "/": "slash", ".": "dot", "=": "equal", ",": "comma",
    ";": "semicolon", "\\": "backslash", "'": "apostrophe", "`": "grave_accent",
    "[": "bracket_left", "]": "bracket_right",
    "_": ("shift", "minus"), ":": ("shift", "semicolon"), '"': ("shift", "apostrophe"),
    "+": ("shift", "equal"), "!": ("shift", "1"), "@": ("shift", "2"), "#": ("shift", "3"),
    "$": ("shift", "4"), "%": ("shift", "5"), "^": ("shift", "6"), "&": ("shift", "7"),
    "*": ("shift", "8"), "(": ("shift", "9"), ")": ("shift", "0"),
    "{": ("shift", "bracket_left"), "}": ("shift", "bracket_right"),
    "|": ("shift", "backslash"), "<": ("shift", "comma"), ">": ("shift", "dot"),
    "?": ("shift", "slash"), "~": ("shift", "grave_accent"),
}

def type_text(q, t, delay=0.02):
    for ch in t:
        if ch.isalpha() and ch.isupper():
            q.keys("shift", ch.lower())
        elif ch.isalnum():
            q.keys(ch)
        else:
            k = SPECIAL[ch]
            q.keys(*(k if isinstance(k, tuple) else (k,)))
        time.sleep(delay)

def win_r(q, cmd, wait=1.0):
    for _ in range(2):
        q.keys("esc"); time.sleep(0.15)
    # Win+R: try meta_l then meta_r (QEMU qcode names)
    try:
        q.keys("meta_l", "r")
    except Exception:
        q.keys("meta_r", "r")
    time.sleep(wait)
    # clear any prior Run contents
    q.keys("ctrl", "a"); time.sleep(0.05)
    type_text(q, cmd)
    time.sleep(0.2)
    q.keys("ret"); time.sleep(0.7)

def main():
    sock = sys.argv[1] if len(sys.argv) > 1 else "/root/e2e/windows-11c/qmp.sock"
    q = QMP(sock)
    for _ in range(5):
        q.keys("esc"); time.sleep(0.15)
    print("clear UI", flush=True)
    win_r(q, "cmd /c del /f /q C:\\aos.b64 C:\\aos-fix.ps1 2>nul")
    for i, c in enumerate(CHUNKS):
        # powershell Set-Content / Add-Content avoids cmd metachar hell on +/=
        if i == 0:
            cmd = "powershell -nop -c Set-Content -Path C:\\aos.b64 -Value '" + c + "' -Encoding ascii"
        else:
            cmd = "powershell -nop -c Add-Content -Path C:\\aos.b64 -Value '" + c + "' -Encoding ascii"
        if len(cmd) > 250:
            raise SystemExit("chunk cmd too long: %d" % len(cmd))
        print("chunk %d/%d len=%d" % (i + 1, len(CHUNKS), len(cmd)), flush=True)
        win_r(q, cmd)
        time.sleep(0.35)
    decode = (
        "powershell -nop -c "
        "\"$b=(Get-Content C:\\aos.b64)-join'';"
        "[IO.File]::WriteAllText('C:\\aos-fix.ps1',"
        "[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($b)))\""
    )
    print("decode len=%d" % len(decode), flush=True)
    if len(decode) > 250:
        # split decode into shorter form
        decode = "powershell -nop -c \"$b=(gc C:\\aos.b64)-join'';[IO.File]::WriteAllText('C:\\aos-fix.ps1',[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($b)))\""
        print("decode short len=%d" % len(decode), flush=True)
    win_r(q, decode, wait=1.2)
    time.sleep(2)
    run = (
        "schtasks /create /tn AOSFixSSH /tr \"powershell -nop -ep bypass -file C:\\aos-fix.ps1\" "
        "/sc once /st 00:00 /ru SYSTEM /f & schtasks /run /tn AOSFixSSH"
    )
    print("schtasks len=%d" % len(run), flush=True)
    win_r(q, run, wait=1.5)
    print("waiting 90s for MSI+sshd", flush=True)
    time.sleep(90)
    print("inject finished", flush=True)

if __name__ == "__main__":
    main()
