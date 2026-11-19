# WSL QEMU Win11 E2E helpers (stand-in when Hyper-V host E2E is N/A)

- `drive4-win.sh` — seedgen + QEMU/OVMF install loop.
  - CD (Windows ISO) `bootindex=1`, HD `bootindex=2`, seed ISO `bootindex=3`.
  - Brief 2× space for "Press any key to boot from CD".
  - READY loop (~150 min) polls SSH `ready.txt`; every ~2 min runs `uefi-boot-hd.py`
    so post-Setup reboots that land in UEFI Shell resume from the HD ESP.
- `uefi-boot-cd.py` — from UEFI shell, `map -r` and try FS0..FS5 for `EFI\BOOT\BOOTX64.EFI`
  (do not assume FS0 is the Windows ISO; seed.iso is often FS0).
- `uefi-boot-hd.py` — from UEFI shell, try `bootmgfw.efi` / `BOOTX64.EFI` on HD ESP
  (fixes mid-install reboot dropping to shell when CD keypress timed out).
- `restart-win-cdfirst.sh` — archive prior run and kick a fresh CD-first install.
- `qmp.py` — tiny QMP helper (shot/key/type).

## Known failure modes (0.1.7 Win11c)

1. **Blind FS0** (fixed in 631e7cc): seed.iso as FS0 → install never wrote disk (~198KB).
2. **Post-reboot UEFI Shell** (fixed here): install reached ~77%, guest reboot, OVMF shell;
   HD ESP already had bootmgfw/bootx64 but no boot path without HD bootindex + recovery.
