"""Reject credentials, private network endpoints, and runtime state in releases."""
from pathlib import Path
import re
import subprocess
import sys
paths = subprocess.check_output(['git', 'ls-files', '-z']).decode().split('\0')
errors = []
for name in filter(None, paths):
    p = Path(name)
    if any(part in {'.env', '.tmp', 'node_modules', 'output'} for part in p.parts) or p.suffix in {'.db', '.pem', '.key'}:
        errors.append(name + ': forbidden release file')
    if not p.is_file():
        continue
    data = p.read_bytes()
    # Scan binary artifacts as well as text. This scanner is only a guardrail;
    # human review of source, fixtures, links, and dependencies remains required.
    for pattern in [rb'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----', rb'gh[pousr]_[A-Za-z0-9]{30,}', rb'AKIA[A-Z0-9]{16}', rb'https?://(?:localhost|127\.0\.0\.1):\d+/.+token=[^\s"<>]+']:
        if re.search(pattern, data):
            errors.append(name + ': possible credential')
if errors:
    print('\n'.join(errors), file=sys.stderr)
    sys.exit(1)
print(f'Public artifact guard checked {len(paths)-1} tracked files.')
