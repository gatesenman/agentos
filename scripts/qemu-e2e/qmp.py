import json, socket, sys, time
class QMP:
    def __init__(s, path):
        s.s = socket.socket(socket.AF_UNIX); s.s.connect(path); s.f = s.s.makefile('rw')
        s.f.readline(); s.cmd('qmp_capabilities')
    def cmd(s, ex, **a):
        s.f.write(json.dumps({'execute': ex, 'arguments': a} if a else {'execute': ex}) + '\n'); s.f.flush()
        while True:
            r = json.loads(s.f.readline())
            if 'return' in r or 'error' in r: return r
    def keys(s, *qc):
        return s.cmd('send-key', keys=[{'type': 'qcode', 'data': k} for k in qc])
SPECIAL = {' ': 'spc', '-': 'minus', '/': 'slash', '.': 'dot', '=': 'equal', ',': 'comma', '_': ('shift', 'minus'), ':': ('shift', 'semicolon'), ';': 'semicolon', '\\': 'backslash', '"': ('shift', 'apostrophe'), "'": 'apostrophe'}
def type_text(q, t):
    for ch in t:
        if ch.isalpha() and ch.isupper(): q.keys('shift', ch.lower())
        elif ch.isalnum(): q.keys(ch)
        else:
            k = SPECIAL[ch]; q.keys(*(k if isinstance(k, tuple) else (k,)))
        time.sleep(0.03)
if __name__ == '__main__':
    q = QMP(sys.argv[1]); op = sys.argv[2]
    if op == 'shot': print(q.cmd('screendump', filename=sys.argv[3], format='png'))
    elif op == 'key': print(q.keys(*sys.argv[3:]))
    elif op == 'type': type_text(q, sys.argv[3])
    elif op == 'grub':
        time.sleep(float(sys.argv[4]) if len(sys.argv) > 4 else 9)
        q.keys('c'); time.sleep(5); q.keys('esc'); time.sleep(0.8); q.keys('c'); time.sleep(0.8)
        for l in open(sys.argv[3]).read().splitlines():
            type_text(q, l); q.keys('ret'); time.sleep(0.6)
    elif op == 'spaces':
        for i in range(35): q.keys('spc'); time.sleep(0.7)
    elif op == 'fedora':
        time.sleep(9); q.keys('up'); time.sleep(0.4); q.keys('ret')
    else: print(q.cmd(op))
