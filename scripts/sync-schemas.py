#!/usr/bin/env python3
"""Keep the published schema URLs compatible with the canonical contracts module."""
import argparse
from pathlib import Path
import sys

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--check', action='store_true')
args = parser.parse_args()
root = Path(__file__).resolve().parent.parent
source = root / 'contracts/schema'
target = root / 'schema'
schemas = sorted(source.glob('*.schema.json'))
if not schemas:
    sys.exit('No canonical schemas found')
drift = []
for schema in schemas:
    public = target / schema.name
    if args.check:
        if not public.is_file() or public.read_bytes() != schema.read_bytes():
            drift.append(str(public.relative_to(root)))
    else:
        target.mkdir(exist_ok=True)
        public.write_bytes(schema.read_bytes())
if drift:
    sys.exit('Published schemas differ from contracts; run make generate:\n' + '\n'.join(drift))
