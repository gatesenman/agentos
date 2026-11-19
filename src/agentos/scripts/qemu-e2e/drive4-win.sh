#!/bin/bash
# Fresh Windows 11 E2E under WSL QEMU/KVM using AgentOS tip seedgen (ComputerName≤15 + VBS off).
# Usage: PROXY=http://10.0.2.2:7897 bash drive4-win.sh
set -u
trap '' HUP
OS=windows-11
ISO=${ISO:-/mnt/d/AgentOS-VMs/iso/26200.6584.250915-1905.25h2_ge_release_svc_refresh_CLIENTENTERPRISEEVAL_OEMRET_x64FRE_zh-cn.iso}
MEM=${MEM:-8192}
PORT=${PORT:-2231}
VNCD=${VNCD:-91}
NAME=${NAME:-windows-11c}
B=/root/e2e
D=$B/$NAME
R=/mnt/d/AgentOS-E2E/qemu-results/$NAME
mkdir -p $D $R/shots
cd $D
# Fresh run: wipe prior disk/seed for this name
if [ "${FRESH:-1}" = 1 ]; then
  rm -f disk.qcow2 seed.iso vars.fd creds.txt id_ed25519 config.json grub.txt seedgen.log
  rm -rf tpm
fi
log(){ echo "$(date '+%F %T') $*" | tee -a $R/result.txt; }
shot(){ python3 $B/qmp.py $D/qmp.sock shot $D/tmp.png >/dev/null 2>&1 && cp $D/tmp.png "$R/shots/$1.png" || true; }
T0=$(date +%s); el(){ echo $(( ($(date +%s)-T0) )); }

# Prefer HTTP(S)_PROXY for MSI downloads inside seedgen
export HTTP_PROXY=${PROXY:-${HTTP_PROXY:-}}
export HTTPS_PROXY=${PROXY:-${HTTPS_PROXY:-}}
export http_proxy=$HTTP_PROXY https_proxy=$HTTPS_PROXY

if [ "${FRESH:-1}" = 1 ] || [ ! -f seed.iso ] || [ ! -f creds.txt ]; then
  log "SEEDGEN tip binary=$(sha256sum $B/seedgen.test | awk '{print $1}')"
  AGENTOS_QEMU_PROXY=${PROXY:-} AGENTOS_QEMU_OUT=$D AGENTOS_QEMU_OS=$OS AGENTOS_QEMU_ISO=$ISO \
    $B/seedgen.test -test.run TestQEMUSeed -test.v > seedgen.log 2>&1 || {
    log "SEEDGEN FAILED"; cat seedgen.log >> $R/result.txt; exit 1; }
else
  log "SEEDGEN skipped (FRESH=0 keep seed.iso/creds)"
fi
chmod 600 id_ed25519 2>/dev/null || true
cp creds.txt seedgen.log config.json $R/ 2>/dev/null || true
# strip private key from archived config
python3 -c "import json,sys; p='$R/config.json';
import pathlib
d=json.loads(pathlib.Path(p).read_text());
for k in list(d):
  if 'private' in k.lower() or k in ('ssh_private_key','SSHPrivateKey'): d.pop(k,None)
pathlib.Path(p).write_text(json.dumps(d,indent=2))" 2>/dev/null || true
USER_=$(sed -n 1p creds.txt)
HOSTN=$(sed -n 3p creds.txt)
log "START $NAME ($OS) proxy=${PROXY:-none} iso=$(basename $ISO) mem=$MEM user=$USER_ host=$HOSTN (len=${#HOSTN})"
# Sanity: ComputerName must be ≤15
if [ ${#HOSTN} -gt 15 ]; then log "ERROR hostname >15: $HOSTN"; exit 3; fi

if [ ! -f disk.qcow2 ]; then
  qemu-img create -f qcow2 disk.qcow2 100G >/dev/null
fi
if [ ! -f vars.fd ]; then
  cp /usr/share/OVMF/OVMF_VARS_4M.fd vars.fd
fi
mkdir -p tpm
if [ ! -S $D/tpm/sock ]; then
  swtpm socket --tpmstate dir=$D/tpm --ctrl type=unixio,path=$D/tpm/sock --tpm2 -d
  sleep 1
fi
EXTRA="-chardev socket,id=chrtpm,path=$D/tpm/sock -tpmdev emulator,id=tpm0,chardev=chrtpm -device tpm-tis,tpmdev=tpm0"
# Disable nested VMX so guest does not turn on VBS; Hyper-V enlightenments
CPU="host,-vmx,hv_relaxed,hv_vapic,hv_spinlocks=0x1fff,hv_time,hv_vendor_id=AuthenticAMD"

ensure_tpm(){
  # swtpm must be up before every qemu start (sock dies if swtpm was killed)
  if [ ! -S "$D/tpm/sock" ]; then
    mkdir -p $D/tpm
    pkill -f "swtpm.*$NAME" 2>/dev/null || true
    sleep 1
    swtpm socket --tpmstate dir=$D/tpm --ctrl type=unixio,path=$D/tpm/sock --tpm2 -d
    sleep 1
    log "swtpm (re)started for $NAME"
  fi
}
start_vm(){
  ensure_tpm
  rm -f $D/qmp.sock
  # setsid: survive parent/session SIGHUP (PC flap / WSL disconnect)
  setsid qemu-system-x86_64 -enable-kvm -machine q35 -cpu "$CPU" -smp 6 -m $MEM \
    -drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
    -drive if=pflash,format=raw,file=$D/vars.fd \
    -drive file=$ISO,media=cdrom,if=none,id=cd0,readonly=on -device ide-cd,drive=cd0,bus=ide.1,bootindex=1 \
    -drive file=$D/disk.qcow2,if=none,id=hd,format=qcow2,cache=unsafe -device ide-hd,drive=hd,bus=ide.0,bootindex=2 \
    -drive file=$D/seed.iso,media=cdrom,if=none,id=cd1,readonly=on -device ide-cd,drive=cd1,bus=ide.2,bootindex=3 \
    -netdev user,id=n0,hostfwd=tcp:127.0.0.1:${PORT}-:22 -device e1000e,netdev=n0 \
    -boot order=d,menu=on,strict=off -usb -device usb-tablet -display none -vnc :$VNCD -qmp unix:$D/qmp.sock,server,nowait $EXTRA \
    >>$D/qemu.log 2>&1 &
  echo $! > $D/qemu.pid
  for i in $(seq 1 60); do [ -S $D/qmp.sock ] && break; sleep 1; done
}
start_vm
sleep 6; shot boot
# Brief keys only: Windows ISO "Press any key to boot from CD" — do NOT spam spaces into UEFI shell
python3 $B/qmp.py $D/qmp.sock key spc >/dev/null 2>&1 || true
sleep 1
python3 $B/qmp.py $D/qmp.sock key spc >/dev/null 2>&1 || true
log "boot keys (2x spc) sent at +$(el)s"
# OVMF often drops to UEFI shell when empty HD has no ESP; try each FS for Windows BOOTX64.EFI
# (seed.iso may be FS0 — blind FS0 boot was a root cause of never-installing / 198KB disk)
if [ -f "$B/uefi-boot-cd.py" ]; then
  log "UEFI multi-FS CD boot attempt at +$(el)s"
  python3 "$B/uefi-boot-cd.py" "$D/qmp.sock" "$D/tmp.png" >>"$R/result.txt" 2>&1 || true
  shot after-fs0
  # second chance after map settles
  sleep 5
  python3 "$B/uefi-boot-cd.py" "$D/qmp.sock" "$D/tmp2.png" >>"$R/result.txt" 2>&1 || true
  shot after-fs0-retry
fi

SSH="ssh -i id_ed25519 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=8 -o BatchMode=yes -o LogLevel=ERROR -p $PORT $USER_@127.0.0.1"
READY=0
for i in $(seq 1 300); do  # up to ~150 min
  sleep 30
  if [ $((i % 2)) -eq 1 ]; then shot "install-$(printf %04d $(el))"; fi
  if timeout 12 $SSH "type C:\\ProgramData\\AgentOS\\ready.txt" >/dev/null 2>&1; then READY=1; break; fi
  # After Setup reboot, OVMF may land in UEFI Shell (CD "press any key" timed out).
  # HD ESP usually already has bootmgfw/bootx64 — re-boot from HD every ~2 min.
  if [ -f "$B/uefi-boot-hd.py" ] && [ $((i % 4)) -eq 0 ]; then
    log "UEFI HD boot recovery attempt at +$(el)s (loop $i)"
    timeout 90 python3 "$B/uefi-boot-hd.py" "$D/qmp.sock" "$D/tmp-hd.png" >>"$R/result.txt" 2>&1 || true
    shot "after-hd-boot-$(printf %04d $(el))"
  fi
  # detect QEMU internal-error (nested VBS); timeout so a stuck QMP cannot hang the loop
  st=$(timeout 15 python3 -c "
import socket,json
s=socket.socket(socket.AF_UNIX); s.settimeout(10); s.connect('$D/qmp.sock'); f=s.makefile('rw')
f.readline(); f.write(json.dumps({'execute':'qmp_capabilities'})+'\n'); f.flush(); f.readline()
f.write(json.dumps({'execute':'query-status'})+'\n'); f.flush(); print(f.readline())
" 2>/dev/null || true)
  if echo "$st" | grep -q internal-error; then
    log "QEMU internal-error at +$(el)s (likely nested VBS); relaunch keep-disk"
    shot vbs-crash
    if [ -f $D/qemu.pid ]; then kill $(cat $D/qemu.pid) 2>/dev/null || true; fi
    pkill -f "qemu-system-x86_64.*${NAME}" 2>/dev/null || true
    sleep 3
    start_vm; sleep 6
    python3 $B/qmp.py $D/qmp.sock key spc >/dev/null 2>&1 || true
    if [ -f "$B/uefi-boot-hd.py" ]; then
      timeout 90 python3 "$B/uefi-boot-hd.py" "$D/qmp.sock" "$D/tmp-hd.png" >>"$R/result.txt" 2>&1 || true
    fi
    shot after-vbs-relaunch
    continue
  fi
  # qemu still alive?
  if [ -f $D/qemu.pid ] && ! kill -0 $(cat $D/qemu.pid) 2>/dev/null; then
    log "qemu exited at +$(el)s; last log:"; tail -30 $D/qemu.log | tee -a $R/result.txt
    # guest may have rebooted into installed OS - restart; prefer HD then re-try CD/HD EFI
    start_vm; sleep 5
    python3 $B/qmp.py $D/qmp.sock key spc >/dev/null 2>&1 || true
    sleep 1
    if [ -f "$B/uefi-boot-hd.py" ]; then
      timeout 90 python3 "$B/uefi-boot-hd.py" "$D/qmp.sock" "$D/tmp-hd.png" >>"$R/result.txt" 2>&1 || true
    fi
    if [ -f "$B/uefi-boot-cd.py" ]; then
      timeout 120 python3 "$B/uefi-boot-cd.py" "$D/qmp.sock" "$D/tmp.png" >>"$R/result.txt" 2>&1 || true
    fi
  fi
done
[ $READY -eq 1 ] || { log "TIMEOUT waiting for ready at +$(el)s"; shot timeout; exit 1; }
log "READY (ssh + marker) at +$(el)s = $(( $(el)/60 )) min"
sleep 20; shot ready-desktop

{
echo "== exec"
timeout 60 $SSH "whoami & hostname & ver"
timeout 90 $SSH "powershell -NoProfile -Command \"Get-Service sshd,tvnserver -EA SilentlyContinue | ft Name,Status -Auto; (Get-ItemProperty 'HKLM:\\System\\CurrentControlSet\\Control\\Terminal Server').fDenyTSConnections; Get-Content C:\\ProgramData\\AgentOS\\ready.txt; Get-Content C:\\ProgramData\\AgentOS\\setup.log -Tail 40 -EA SilentlyContinue\""
echo "== files"
head -c 200000 /dev/urandom > $D/up.bin; H1=$(sha256sum $D/up.bin | cut -c1-64)
SCP="scp -q -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes -o LogLevel=ERROR -i $D/id_ed25519 -P $PORT"
$SCP $D/up.bin $USER_@127.0.0.1:up.bin && $SCP $USER_@127.0.0.1:up.bin $D/down.bin && H2=$(sha256sum $D/down.bin | cut -c1-64) && [ "$H1" = "$H2" ] && echo "FILES_OK roundtrip sha $H1" || echo "FILES_FAIL"
echo "== vnc via ssh tunnel"
$SSH -N -L 127.0.0.1:$((PORT+1000)):127.0.0.1:5900 & TP=$!; sleep 4
python3 -c "import socket;s=socket.create_connection(('127.0.0.1',$((PORT+1000))),5);s.settimeout(8);print('VNC banner', s.recv(12))" || echo VNC_FAIL
kill $TP 2>/dev/null || true
} >> $R/result.txt 2>&1

log "checks done at +$(el)s; reboot test"
timeout 20 $SSH "shutdown /r /t 3" || true
sleep 45
for i in $(seq 1 60); do timeout 10 $SSH "echo BACK" >/dev/null 2>&1 && break; sleep 5; done
log "back after reboot at +$(el)s"; shot after-reboot
timeout 20 $SSH "shutdown /s /t 0" || true
sleep 20
log "DONE total +$(el)s"
echo PASS > $R/PASS
exit 0
