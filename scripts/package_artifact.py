"""Create an explicit file inventory; the archive digest is distributed separately."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys

root = Path(sys.argv[1])
manifest = dict(schema=1, revision=sys.argv[2], engine_version=sys.argv[3],
                os=sys.argv[4], arch=sys.argv[5], mode=sys.argv[6],
                go_version=subprocess.check_output(['go', 'env', 'GOVERSION'], text=True).strip(),
                data_format='foundation-1', files={})
for path in sorted(root.rglob('*')):
    if path.is_symlink():
        raise ValueError('artifact must not contain symlinks')
    if path.is_file():
        manifest['files'][str(path.relative_to(root))] = hashlib.sha256(path.read_bytes()).hexdigest()
(root / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
