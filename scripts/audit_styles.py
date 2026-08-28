#!/usr/bin/env python3
"""Count LITERAL colours in plugin markup/CSS, excluding HTML entities.

A naive `#[0-9a-f]{3,8}` also matches the digits inside `&#9679;` (a black
circle) and `&#8593;` (an up arrow), which are not colours at all. That
mistake inflated the first measurement, so this is the one the standard
should cite.
"""
import collections
import os
import re
import sys

HEX = re.compile(r'(?<![&\w])#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{4}|[0-9a-fA-F]{3})\b')
RGB = re.compile(r'\brgba?\(\s*\d')
INLINE = re.compile(r'style="')


def main():
    root = sys.argv[1] if len(sys.argv) > 1 else '.'
    colours = collections.Counter()
    inline = collections.Counter()
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in ('.git', 'vendor', 'node_modules')]
        for fn in filenames:
            if not (fn.endswith('.go') or fn.endswith('.html')):
                continue
            path = os.path.join(dirpath, fn)
            try:
                src = open(path, encoding='utf-8', errors='replace').read()
            except OSError:
                continue
            rel = os.path.relpath(path, root).replace(os.sep, '/')
            plug = rel.split('/')[0] if '/' in rel else '(root)'
            colours[plug] += len(HEX.findall(src)) + len(RGB.findall(src))
            inline[plug] += len(INLINE.findall(src))

    print('LITERAL COLOURS: %d   INLINE style=: %d' % (sum(colours.values()), sum(inline.values())))
    print('%-18s %8s %8s' % ('plugin', 'colours', 'inline'))
    for plug in sorted(set(colours) | set(inline), key=lambda p: -(colours[p] + inline[p])):
        if colours[plug] or inline[plug]:
            print('%-18s %8d %8d' % (plug, colours[plug], inline[plug]))


if __name__ == '__main__':
    main()
