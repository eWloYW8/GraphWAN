#!/usr/bin/env python3
"""Local RFC 8489 Binding fixture for isolated network-namespace tests only."""
import socket
import struct

sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
sock.bind(("192.0.2.1", 3478))
print("ready", flush=True)
while True:
    raw, source = sock.recvfrom(2049)
    if len(raw) < 20 or len(raw) > 2048:
        continue
    kind, size, cookie = struct.unpack("!HHI", raw[:8])
    if kind != 1 or cookie != 0x2112A442 or size != len(raw) - 20:
        continue
    address = int.from_bytes(socket.inet_aton(source[0]), "big") ^ cookie
    attribute = struct.pack("!HHBBHI", 0x20, 8, 0, 1, source[1] ^ 0x2112, address)
    sock.sendto(struct.pack("!HHI", 0x101, len(attribute), cookie) + raw[8:20] + attribute, source)
