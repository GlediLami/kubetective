#!/usr/bin/env python3
"""Render a real terminal demo of kubetective as an animated GIF.

It actually RUNS the command and renders its real output, instead of
hardcoding a fake screenshot. Pure PIL - no ffmpeg/asciinema needed.

    python3 hack/demo_gif.py                      # -> demo.gif (repo root)
    python3 hack/demo_gif.py --out /tmp/demo.gif 
    python3 hack/demo_gif.py --show-frames 30     # dump frame PNGs for QA
"""
import argparse
import subprocess
import sys
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

BG = (15, 23, 32)          # page dark
TERM_BG = (10, 16, 24)
TITLE_BAR = (23, 32, 44)
TEXT = (219, 228, 238)
MUTED = (143, 163, 184)
PROMPT = (74, 163, 255)
OK = (139, 233, 168)
WARN = (255, 184, 108)
ACCENT = (74, 163, 255)
BORDER = (41, 55, 75)

FONT_PATH = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"
FONT_SIZE = 24
LINE_H = 27
PAD_X = 24
PAD_Y = 18


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default="demo.gif")
    ap.add_argument("--hard-frames", type=int, default=0)
    ap.add_argument("--command", default="kubetective replay scenarios/oom-after-deploy/record.jsonl")
    ap.add_argument("--charm-s", type=int, default=1, help="typing speed multiplier (higher=faster)")
    ap.add_argument("--scale", type=float, default=0.92, help="render scale for gif size")
    ap.add_argument("--colors", type=int, default=256, help="quantize to shared GIF palette (0=off)")
    args = ap.parse_args()

    repo = Path(__file__).resolve().parent.parent

    # 1) run the real command, capture the real output
    cmd = args.command.split(" ")
    proc = subprocess.run(cmd, cwd=repo, capture_output=True, text=True, timeout=120)
    output_lines = (proc.stdout + proc.stderr).split("\n")
    if proc.returncode != 0:
        print(f"demo command failed ({proc.returncode}): {' '.join(cmd)}", file=sys.stderr)
        sys.exit(1)

    font = ImageFont.truetype(FONT_PATH, FONT_SIZE)
    CELL = font.getlength("M")  # actual monospace cell width (text advance) in px

    # terminal width: wrap over-long lines like a real terminal would
    TERM_COLS = 118

    def wrap_lines(raw):
        out = []
        for l in raw:
            if len(l) <= TERM_COLS:
                out.append(l)
                continue
            out.append(l[:TERM_COLS])
            rest = l[TERM_COLS:]
            while len(rest) > TERM_COLS - 4:
                out.append(" " * 4 + rest[: TERM_COLS - 4])
                rest = rest[TERM_COLS - 4:]
            if rest:
                out.append(" " * 4 + rest)
        return out

    output_lines = wrap_lines(output_lines)
    cols = max((len(l) for l in [f"$ {args.command}"] + output_lines if l), default=80)
    rows = max(3, len(output_lines) + 2)  # done line + 1 blank

    W = PAD_X * 2 + max(80, cols * (FONT_SIZE * 0.62)) + 40
    H = PAD_Y * 2 + rows * LINE_H + 30

    lines = [f"$ {args.command}"]
    for l in output_lines:
        lines.append(l)

    # 2) build frames: type the command, then stream the output
    CHAR_MS = max(12, int(45 / args.charm_s))
    frames = []

    def make_frame(drawn, cursor_x, show_cursor, flash):
        img = Image.new("RGB", (int(W), int(H)), BG)
        d = ImageDraw.Draw(img)
        d.rectangle([0, 0, W, 34], fill=TITLE_BAR)
        d.text((PAD_X, 8), "kubectl — kubetective demo (real recorded investigation)", font=font, fill=MUTED)
        d.rectangle([PAD_X // 2, 34, W - PAD_X // 2, H - PAD_Y // 2], outline=BORDER, width=2)
        d.rectangle([PAD_X // 2 + 6, 36, W - PAD_X // 2 - 6, H - PAD_Y // 2 - 2], fill=TERM_BG)
        y = 36 + 10
        for (text, color) in drawn:
            d.text((PAD_X + 12, y), text, font=font, fill=color)
            y += LINE_H
        if show_cursor:
            x = PAD_X + 12 + cursor_x
            # distinct white block so the cursor never blends into the prompt text
            d.rectangle([x, y - LINE_H + 3, x + CELL, y - 3],
                        fill=(235, 238, 242) if (flash % 2) else (105, 118, 132))
        return img

    # phases ---------------------------------------------------------------
    drawn = []          # list of (text, color) rows
    cmd_text = f"$ {args.command}"
    # intro: empty prompt + blinking cursor
    for t in range(8):
        frames.append(make_frame(drawn, 0, True, t))
    # type the command one character per frame (smooth, no skipped chars)
    acc = ""
    for i, ch in enumerate(cmd_text):
        acc += ch
        frames.append(make_frame([(acc, PROMPT)], len(acc) * CELL, True, 1))
    drawn = [(cmd_text, PROMPT)]

    # stream the real output line by line (blank separator lines included,
    # like a real terminal: skipping them was leaving dead rows at the bottom)
    for line in output_lines:
        if line.startswith("╭") or line.startswith("╰"):
            color = ACCENT
        elif line.startswith("ROOT CAUSE") or line.startswith("EVIDENCE") or line.startswith("TIMELINE"):
            color = ACCENT
        elif line.startswith("RECOMMENDATION"):
            color = OK
        elif line.startswith("│ INCIDENT"):
            color = ACCENT
        elif line.startswith("RELATIONSHIPS") or line.startswith("WHAT CHANGED"):
            color = MUTED
        else:
            color = TEXT
        drawn.append((line, color))
        for _ in range(max(1, min(4, len(line) // 40))):
            frames.append(make_frame(drawn, -1, True, 0))
    # final hold: 3 quick cursor blinks, then ONE long static frame. Long
    # static-hold by duplicating frames would balloon the file (each GIF frame
    # stores the whole image), so the final frame gets a patched, longer delay.
    for t in range(6):
        frames.append(make_frame(drawn, -1, True, t))
    done = drawn + [("✓ investigation complete — recorded & replayable", OK)]
    frames.append(make_frame(done, -1, False, 0))

    out = Path(args.out)
    if not out.is_absolute():
        out = repo / out

    if args.colors:
        frames = _shared_palette(frames, args.colors)
    if args.scale != 1.0:
        w = max(1, int(frames[0].width * args.scale))
        h = max(1, int(frames[0].height * args.scale))
        frames = [f.resize((w, h), Image.LANCZOS) for f in frames]

    frames[0].save(
        out, save_all=True, append_images=frames[1:],
        duration=[CHAR_MS] * (len(frames) - 1) + [3900], loop=0,
        optimize=True, disposal=2,
    )
    print(f"wrote {out} ({len(frames)} frames, {out.stat().st_size / 1024:.0f} KB)")

    if args.hard_frames:
        for i in range(0, len(frames), max(1, len(frames) // args.hard_frames)):
            frames[i].save(out.with_suffix(f".{i:03d}.png"))


def _shared_palette(frames, colors):
    """Quantize with one shared palette (from the final frame) to avoid per-frame palette flicker."""
    sample = frames[-1].quantize(colors=colors, method=Image.MEDIANCUT, dither=Image.NONE)
    pal = sample.getpalette()
    out = []
    for f in frames:
        q = f.quantize(palette=sample, dither=Image.NONE)
        q.info = {"transparency": None}
        out.append(q)
    return out


if __name__ == "__main__":
    main()