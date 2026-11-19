#!/usr/bin/env python3
"""From UEFI Interactive Shell, find Windows ISO and boot EFI\\BOOT\\BOOTX64.EFI.

Root cause of Win11c stuck install (disk.qcow2 stayed ~198KB): OVMF often drops to
the UEFI shell when the empty HD has no EFI system partition. Blindly typing FS0:
can hit the *seed* ISO (ide.2) instead of the Windows install ISO (ide.1), so setup
never runs and the disk never grows.
"""
import json, socket, sys, time

class QMP:
    def __init__(self, path):
        self.s = socket.socket(socket.AF_UNIX)
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
        time.sleep(0.05)

def run_line(q, line, wait=1.0):
    type_text(q, line)
    q.keys("ret")
    time.sleep(wait)

def main():
    sock = sys.argv[1]
    shot = sys.argv[2] if len(sys.argv) > 2 else None
    q = QMP(sock)
    # clear any junk / exit editors
    for _ in range(4):
        q.keys("esc")
        time.sleep(0.12)
    time.sleep(0.3)
    # refresh filesystem map (seed + Win ISO + possibly others)
    run_line(q, "map -r", wait=2.0)
    # Try each FS: cd to volume, then boot Windows media path if present.
    # Prefer higher indices last; seed ISO is often FS0 when enumerated first.
    candidates = ["FS0", "FS1", "FS2", "FS3", "FS4", "FS5"]
    for fs in candidates:
        run_line(q, f"{fs}:", wait=0.8)
        # Attempt Windows UEFI bootloader; if missing, shell prints error and we try next.
        run_line(q, r"EFI\BOOT\BOOTX64.EFI", wait=2.5)
        print(f"tried {fs}:\\EFI\\BOOT\\BOOTX64.EFI")
    # Also try explicit cd forms some shells accept
    for fs in candidates:
        run_line(q, f"{fs}:\\EFI\\BOOT\\BOOTX64.EFI", wait=2.0)
        print(f"tried path {fs}:\\EFI\\BOOT\\BOOTX64.EFI")
    print("sent multi-FS EFI boot attempts")
    time.sleep(8)
    if shot:
        print(q.cmd("screendump", filename=shot, format="png"))

if __name__ == "__main__":
    main()
