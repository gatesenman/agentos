#!/usr/bin/env python3
"""From UEFI Interactive Shell, boot Windows from the HD ESP.

After Windows Setup reboots mid-install, OVMF often drops to the UEFI shell when
the install ISO (bootindex=1) times out on "Press any key" and the HD had no
bootindex. The ESP typically already has:
  FS0:\\EFI\\Microsoft\\Boot\\bootmgfw.efi
  FS0:\\EFI\\Boot\\bootx64.efi
"""
import json, socket, sys, time

class QMP:
    def __init__(self, path):
        self.s = socket.socket(socket.AF_UNIX)
        self.s.settimeout(20)
        self.s.connect(path)
        self.f = self.s.makefile("rw")
        self.f.readline()
        self.cmd("qmp_capabilities")

    def cmd(self, ex, **a):
        payload = {"execute": ex, "arguments": a} if a else {"execute": ex}
        self.f.write(json.dumps(payload) + "\n")
        self.f.flush()
        while True:
            r = json.loads(self.f.readline())
            if "return" in r or "error" in r:
                return r

    def keys(self, *qc):
        return self.cmd("send-key", keys=[{"type": "qcode", "data": k} for k in qc])

SPECIAL = {
    " ": "spc", "-": "minus", "/": "slash", ".": "dot", "=": "equal",
    ",": "comma", ";": "semicolon", "\\": "backslash",
    "_": ("shift", "minus"), ":": ("shift", "semicolon"),
}

def type_text(q, t):
    for ch in t:
        if ch.isalpha() and ch.isupper():
            q.keys("shift", ch.lower())
        elif ch.isalnum():
            q.keys(ch)
        else:
            k = SPECIAL[ch]
            q.keys(*(k if isinstance(k, tuple) else (k,)))
        time.sleep(0.04)

def run_line(q, line, wait=1.0):
    type_text(q, line)
    q.keys("ret")
    time.sleep(wait)

def main():
    sock = sys.argv[1]
    shot = sys.argv[2] if len(sys.argv) > 2 else None
    q = QMP(sock)
    q.keys("ret")
    time.sleep(0.4)
    run_line(q, "map -r", wait=1.5)
    # Prefer Microsoft Boot Manager on ESP; try a few FS in case order shifts
    paths = []
    for fs in ("FS0", "FS1", "FS2", "FS3"):
        paths.append(f"{fs}:\\EFI\\Microsoft\\Boot\\bootmgfw.efi")
        paths.append(f"{fs}:\\EFI\\Boot\\BOOTX64.EFI")
        paths.append(f"{fs}:\\EFI\\Boot\\bootx64.efi")
    for p in paths:
        run_line(q, p, wait=2.0)
        print(f"tried {p}")
    print("sent HD EFI boot attempts")
    time.sleep(5)
    if shot:
        print(q.cmd("screendump", filename=shot, format="png"))

if __name__ == "__main__":
    main()
