"""Build-time installation of the core family required by the pinned DSH CLI."""
import base64
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import re
import shutil
import sys
import tarfile
import tempfile

root = Path('/usr/local/lib/node_modules/@deepseek-ai/dsh')
assert json.loads((root / 'package.json').read_text())['version'] == '0.1.2-alpha.3'
lock = json.loads(Path(__file__).with_name('cordis.lock.json').read_text())
archives = Path(sys.argv[1])
assert len(lock['packages']) == 8
assert len({item['name'] for item in lock['packages']}) == 8
for item in lock['packages']:
    assert re.fullmatch(r'@deepseek-ai/[a-z0-9-]+', item['name'])
    assert Path(item['file']).name == item['file']
    data = (archives / item['file']).read_bytes()
    assert hashlib.sha256(data).hexdigest() == item['sha256']
    algorithm, expected = item['integrity'].split('-', 1)
    assert algorithm == 'sha512'
    assert base64.b64encode(hashlib.sha512(data).digest()).decode() == expected
    target = root / 'node_modules' / item['name']
    assert target.is_dir() and not target.is_symlink()
    with tempfile.TemporaryDirectory(prefix='.cordis-pin-', dir=target.parent) as temporary:
        staging = Path(temporary)
        with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as archive:
            for entry in archive.getmembers():
                name = PurePosixPath(entry.name)
                assert name.parts and name.parts[0] == 'package'
                assert not name.is_absolute() and '..' not in name.parts
                assert entry.isdir() or entry.isfile()
                output = staging / name
                if entry.isdir():
                    output.mkdir(parents=True, exist_ok=True)
                else:
                    output.parent.mkdir(parents=True, exist_ok=True)
                    output.write_bytes(archive.extractfile(entry).read())
                    output.chmod(0o755 if entry.mode & 0o111 else 0o644)
        package = json.loads((staging / 'package/package.json').read_text())
        assert package['name'] == item['name'] and package['version'] == item['version']
        # Build layers can reject renaming lower-layer directories (EXDEV).
        # No SDK process is running here; a failure aborts the image build.
        shutil.rmtree(target)
        shutil.copytree(staging / 'package', target)
    print(item['name'] + '@' + item['version'])
