"""Check the exact Go release stays aligned across build and documentation."""
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[1]
PATTERNS = {
    'go.mod': r'^go (\d+\.\d+\.\d+)$',
    'Dockerfile': r'^FROM --platform=\$BUILDPLATFORM golang:(\d+\.\d+\.\d+) AS build$',
    'README.md': r'^Requires Go (\d+\.\d+\.\d+) or newer\.$',
}


def check(root=ROOT):
    versions = {}
    for name, pattern in PATTERNS.items():
        matches = re.findall(pattern, (root / name).read_text(), re.MULTILINE)
        if len(matches) != 1:
            raise ValueError(f'{name}: expected exactly one exact Go version')
        versions[name] = matches[0]
    if len(set(versions.values())) != 1:
        raise ValueError(f'Go versions differ: {versions}')
    return versions['go.mod']


if __name__ == '__main__':
    try:
        print(f'Go versions aligned: {check()}')
    except ValueError as error:
        print(error, file=sys.stderr)
        sys.exit(1)
