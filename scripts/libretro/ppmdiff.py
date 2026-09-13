#!/usr/bin/env python3
"""Compare two binary PPM (P6) frames dumped by the core's IKEMEN_DUMP hook.

    ppmdiff.py a.ppm b.ppm [--tolerance N]

Prints one line: identical, or how many pixels differ and by how much. Exit
status 0 when every channel is within the tolerance (default 0: bit-exact),
1 otherwise, 2 when the files cannot be compared.

A batching change should be bit-exact: it reorders how the same quads reach
the GPU, not what they draw. A tolerance is only there for experiments that
knowingly change precision.
"""
import sys


def read_ppm(path):
    with open(path, "rb") as f:
        data = f.read()
    fields, pos = [], 0
    while len(fields) < 4:
        while data[pos:pos + 1].isspace():
            pos += 1
        if data[pos:pos + 1] == b"#":
            pos = data.index(b"\n", pos) + 1
            continue
        end = pos
        while not data[end:end + 1].isspace():
            end += 1
        fields.append(data[pos:end])
        pos = end
    pos += 1  # the single whitespace byte after maxval
    if fields[0] != b"P6" or fields[3] != b"255":
        raise ValueError(f"{path}: not an 8-bit P6 PPM")
    w, h = int(fields[1]), int(fields[2])
    pixels = data[pos:pos + w * h * 3]
    if len(pixels) != w * h * 3:
        raise ValueError(f"{path}: truncated")
    return w, h, pixels


def main():
    args = sys.argv[1:]
    tol = 0
    if "--tolerance" in args:
        i = args.index("--tolerance")
        tol = int(args[i + 1])
        del args[i:i + 2]
    if len(args) != 2:
        print(__doc__.strip().splitlines()[2].strip(), file=sys.stderr)
        return 2
    try:
        wa, ha, a = read_ppm(args[0])
        wb, hb, b = read_ppm(args[1])
    except (OSError, ValueError) as e:
        print(f"cannot compare: {e}")
        return 2
    if (wa, ha) != (wb, hb):
        print(f"size differs: {wa}x{ha} vs {wb}x{hb}")
        return 1
    if a == b:
        print(f"identical ({wa}x{ha})")
        return 0

    differing, worst = 0, 0
    for i in range(0, len(a), 3):
        d = max(abs(a[i] - b[i]), abs(a[i + 1] - b[i + 1]), abs(a[i + 2] - b[i + 2]))
        if d:
            differing += 1
            worst = max(worst, d)
    total = wa * ha
    verdict = "within tolerance" if worst <= tol else "DIFFERENT"
    print(f"{verdict}: {differing}/{total} pixels differ ({100 * differing / total:.3f}%), "
          f"max channel delta {worst}")
    return 0 if worst <= tol else 1


if __name__ == "__main__":
    sys.exit(main())
