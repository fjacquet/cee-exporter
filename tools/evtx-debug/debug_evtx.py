import struct

with open('/tmp/audit.evtx', 'rb') as f:
    buf = f.read()

print(f"File size: {len(buf)}")

# Chunk at offset 4096
C = 4096
hs = struct.unpack_from('<I', buf, C+40)[0]
nro = struct.unpack_from('<I', buf, C+48)[0]
print(f"Chunk header_size={hs}, next_record_offset={nro}")

# Manual record scan
ofs = hs
n = 0
while ofs < nro:
    a = C + ofs
    magic = struct.unpack_from('<I', buf, a)[0]
    sz = struct.unpack_from('<I', buf, a+4)[0]
    rid = struct.unpack_from('<Q', buf, a+8)[0]
    print(f"  ofs={ofs} magic=0x{magic:08x} size={sz} id={rid}")
    if magic != 0x00002a2a or sz == 0:
        break
    ofs += sz
    n += 1
print(f"Manual scan: {n} record(s)")

# python-evtx
print("\n--- python-evtx ---")
import Evtx.Evtx as evtx
with evtx.Evtx('/tmp/audit.evtx') as log:
    chunks = list(log.chunks())
    print(f"chunks: {len(chunks)}")
    for ci, chunk in enumerate(chunks):
        print(f"  chunk[{ci}] header_size={chunk.header_size()} next_record_offset={chunk.next_record_offset()}")
        try:
            recs = list(chunk.records())
            print(f"  records: {len(recs)}")
        except Exception as e:
            print(f"  records error: {e}")
