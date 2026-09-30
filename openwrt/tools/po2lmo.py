#!/usr/bin/env python3
"""
po2lmo - Convert .po files to LuCI .lmo binary format.

LMO format (LuCI Machine Objects):
  1. Translated string data (each padded to 4-byte alignment)
  2. Index entries: [key_id(u32be), val_id(u32be), offset(u32be), length(u32be)]
     sorted by key_id
  3. Total data section size as u32be

Hash: SuperFastHash (Paul Hsieh) - same as used in LuCI C implementation.

Usage: po2lmo.py input.po output.lmo
"""

import struct
import sys
import re


def sfh_hash(data: bytes, init: int) -> int:
    """SuperFastHash - compatible with LuCI's sfh_hash in lmo.c"""
    if not data:
        return 0

    h = init & 0xFFFFFFFF
    length = len(data)
    rem = length & 3
    n = length >> 2

    idx = 0
    for _ in range(n):
        h = (h + (data[idx] | (data[idx + 1] << 8))) & 0xFFFFFFFF
        tmp = (((data[idx + 2] | (data[idx + 3] << 8)) << 11) ^ h) & 0xFFFFFFFF
        h = ((h << 16) ^ tmp) & 0xFFFFFFFF
        idx += 4
        h = (h + (h >> 11)) & 0xFFFFFFFF

    if rem == 3:
        h = (h + (data[idx] | (data[idx + 1] << 8))) & 0xFFFFFFFF
        h = (h ^ (h << 16)) & 0xFFFFFFFF
        # signed char
        c = data[idx + 2]
        if c >= 128:
            c -= 256
        h = (h ^ ((c << 18) & 0xFFFFFFFF)) & 0xFFFFFFFF
        h = (h + (h >> 11)) & 0xFFFFFFFF
    elif rem == 2:
        h = (h + (data[idx] | (data[idx + 1] << 8))) & 0xFFFFFFFF
        h = (h ^ (h << 11)) & 0xFFFFFFFF
        h = (h + (h >> 17)) & 0xFFFFFFFF
    elif rem == 1:
        c = data[idx]
        if c >= 128:
            c -= 256
        h = (h + c) & 0xFFFFFFFF
        h = (h ^ (h << 10)) & 0xFFFFFFFF
        h = (h + (h >> 1)) & 0xFFFFFFFF

    h = (h ^ (h << 3)) & 0xFFFFFFFF
    h = (h + (h >> 5)) & 0xFFFFFFFF
    h = (h ^ (h << 4)) & 0xFFFFFFFF
    h = (h + (h >> 17)) & 0xFFFFFFFF
    h = (h ^ (h << 25)) & 0xFFFFFFFF
    h = (h + (h >> 6)) & 0xFFFFFFFF

    return h


def lmo_hash(s: str) -> int:
    """Hash a string the way LuCI does: sfh_hash(data, len, init=len)"""
    data = s.encode("utf-8")
    return sfh_hash(data, len(data))


def parse_po(path: str):
    """Parse a .po file, yield (msgid, msgstr) pairs."""
    msgid = None
    msgstr = None
    cur = None  # 'id' or 'str'

    def unescape(s):
        """Unescape PO string escapes."""
        s = s.replace("\\n", "\n")
        s = s.replace("\\t", "\t")
        s = s.replace('\\"', '"')
        s = s.replace("\\\\", "\\")
        return s

    with open(path, "r", encoding="utf-8") as f:
        for line in f:
            line = line.rstrip("\n")

            if line.startswith("msgid \""):
                # Emit previous pair
                if msgid is not None and msgstr:
                    yield (msgid, msgstr)
                m = re.match(r'^msgid "(.*)"$', line)
                msgid = unescape(m.group(1)) if m else ""
                msgstr = None
                cur = "id"
            elif line.startswith("msgstr \""):
                m = re.match(r'^msgstr "(.*)"$', line)
                msgstr = unescape(m.group(1)) if m else ""
                cur = "str"
            elif line.startswith('"') and line.endswith('"'):
                # Continuation line
                content = unescape(line[1:-1])
                if cur == "id":
                    msgid = (msgid or "") + content
                elif cur == "str":
                    msgstr = (msgstr or "") + content
            elif line.startswith("#") or line.strip() == "":
                cur = None

    # Emit last pair
    if msgid is not None and msgstr:
        yield (msgid, msgstr)


def po2lmo(po_path: str, lmo_path: str):
    """Convert a .po file to .lmo binary format."""
    entries = []
    data_parts = []
    offset = 0

    for msgid, msgstr in parse_po(po_path):
        if not msgid or not msgstr:
            continue

        key_id = lmo_hash(msgid)
        val_id = lmo_hash(msgstr)

        if key_id == val_id:
            continue

        val_bytes = msgstr.encode("utf-8")
        val_len = len(val_bytes)
        padded_len = val_len + ((4 - (val_len % 4)) % 4)

        entries.append((key_id, 1, offset, val_len))  # val_id=1 (singular)
        data_parts.append(val_bytes + b"\x00" * (padded_len - val_len))
        offset += padded_len

    if not entries:
        print(f"Warning: no translations found in {po_path}", file=sys.stderr)
        return

    # Sort index by key_id
    entries.sort(key=lambda e: e[0])

    with open(lmo_path, "wb") as out:
        # Write data section
        for part in data_parts:
            out.write(part)

        # Write index (sorted by key_id)
        # Re-sort since data was written in parse order; re-map offsets
        # Actually we need to write data in parse order but index sorted.
        # The data was already written above. The offsets in entries are correct.
        for key_id, val_id, off, length in entries:
            out.write(struct.pack(">IIII", key_id, val_id, off, length))

        # Write data section size
        out.write(struct.pack(">I", offset))

    print(f"Wrote {len(entries)} translations to {lmo_path}")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        print(f"Usage: {sys.argv[0]} input.po output.lmo", file=sys.stderr)
        sys.exit(1)
    po2lmo(sys.argv[1], sys.argv[2])
