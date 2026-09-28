#!/usr/bin/env python3
"""Local UDP/TCP RFC 8489 Binding fixture for isolated namespace tests only."""
import selectors
import socket
import struct


def response(raw, source):
    if len(raw) < 20 or len(raw) > 2048:
        return None
    kind, size, cookie = struct.unpack("!HHI", raw[:8])
    if kind != 1 or cookie != 0x2112A442 or size != len(raw) - 20:
        return None
    address = int.from_bytes(socket.inet_aton(source[0]), "big") ^ cookie
    attribute = struct.pack("!HHBBHI", 0x20, 8, 0, 1, source[1] ^ 0x2112, address)
    return struct.pack("!HHI", 0x101, len(attribute), cookie) + raw[8:20] + attribute


udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
udp.bind(("192.0.2.1", 3478))
tcp = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
tcp.bind(("192.0.2.1", 3478))
tcp.listen(16)
selector = selectors.DefaultSelector()
selector.register(udp, selectors.EVENT_READ)
selector.register(tcp, selectors.EVENT_READ)
print("ready", flush=True)
while True:
    for key, _ in selector.select():
        conn = key.fileobj
        if conn is udp:
            raw, source = udp.recvfrom(2049)
            reply = response(raw, source)
            if reply:
                udp.sendto(reply, source)
        elif conn is tcp:
            conn, source = tcp.accept()
            if len(selector.get_map()) >= 34:
                conn.close()
            else:
                conn.settimeout(1)
                selector.register(conn, selectors.EVENT_READ, (source, bytearray()))
        else:
            try:
                source, buffer = key.data
                raw = conn.recv(4096)
                if not raw:
                    raise OSError("closed")
                buffer.extend(raw)
                if len(buffer) > 4096:
                    raise OSError("oversized")
                while len(buffer) >= 20:
                    size = 20 + int.from_bytes(buffer[2:4], "big")
                    if size > 2048:
                        raise OSError("oversized")
                    if len(buffer) < size:
                        break
                    reply = response(buffer[:size], source)
                    del buffer[:size]
                    if not reply:
                        raise OSError("invalid")
                    conn.sendall(reply)
            except OSError:
                selector.unregister(conn)
                conn.close()
