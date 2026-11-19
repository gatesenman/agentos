#!/usr/bin/env python3
"""Minimal RFB 3.8 server requiring VNC authentication (any 16-byte response is
accepted unless it is all zeros), serving a 320x200 solid orange framebuffer
and announcing a server clipboard text. For M4 UI tests only.
Usage: fake_rfb_auth.py <port>"""
import socket, struct, sys, threading

W, H = 320, 200

def recvn(c, n):
    b = b''
    while len(b) < n:
        d = c.recv(n - len(b))
        if not d:
            raise EOFError
        b += d
    return b

def handle(c):
    try:
        c.sendall(b'RFB 003.008\n'); recvn(c, 12)
        c.sendall(bytes([1, 2]))            # one security type: VNC auth
        recvn(c, 1)
        c.sendall(b'\x11' * 16)             # challenge
        resp = recvn(c, 16)
        if resp == b'\x00' * 16:
            msg = b'bad password'
            c.sendall(struct.pack('>II', 1, len(msg)) + msg); return
        c.sendall(struct.pack('>I', 0))
        recvn(c, 1)                         # ClientInit
        pf = struct.pack('>BBBBHHHBBBxxx', 32, 24, 0, 1, 255, 255, 255, 16, 8, 0)
        name = b'fake-auth-desktop'
        c.sendall(struct.pack('>HH', W, H) + pf + struct.pack('>I', len(name)) + name)
        sent_clip = False
        while True:
            t = recvn(c, 1)[0]
            if t == 0: recvn(c, 19)
            elif t == 2:
                n = struct.unpack('>xH', recvn(c, 3))[0]; recvn(c, 4 * n)
            elif t == 3:
                recvn(c, 9)
                px = bytes([0x20, 0x8c, 0xff, 0]) * (W * H)   # BGRX little-endian -> orange
                c.sendall(struct.pack('>BxH', 0, 1) + struct.pack('>HHHHi', 0, 0, W, H, 0) + px)
                if not sent_clip:
                    txt = b'hello from remote'
                    c.sendall(struct.pack('>BxxxI', 3, len(txt)) + txt); sent_clip = True
            elif t == 4: recvn(c, 7)
            elif t == 5: recvn(c, 5)
            elif t == 6:
                n = struct.unpack('>xxxi', recvn(c, 7))[0]; recvn(c, abs(n))
            else:
                return
    except (EOFError, OSError):
        pass
    finally:
        c.close()

s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', int(sys.argv[1]))); s.listen(8)
while True:
    conn, _ = s.accept()
    threading.Thread(target=handle, args=(conn,), daemon=True).start()
