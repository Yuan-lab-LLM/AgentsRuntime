"""Usage: python migration.py NODE OLD_DSH_JS NEW_DSH_JS; no production data used."""
import hashlib
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
from http.server import ThreadingHTTPServer
from smoke import ModelFixture


def main():
    node, old_entry, new_entry = sys.argv[1:]
    runtime = Path(__file__).resolve().parents[1]
    patch = str(runtime / 'config' / 'managed.cordis.patch.yml')
    fixture = ThreadingHTTPServer(('127.0.0.1', 0), ModelFixture)
    threading.Thread(target=fixture.serve_forever, daemon=True).start()
    with tempfile.TemporaryDirectory(prefix='dsh-migration-') as directory:
        # New Harness canonicalizes cwd; avoid a Windows 8.3 alias in old metadata.
        root = Path(directory).resolve()
        home = root / '.dsh'
        env = dict(os.environ, HOME=str(root), USERPROFILE=str(root), DSH_HOME=str(home), DSH_TELEMETRY_DISABLED='1',
                   CLAWMANAGER_LLM_API_KEY='fixture-model', CLAWMANAGER_LLM_CONTEXT_WINDOW='64000',
                   CLAWMANAGER_LLM_RESOLVED_CONFIG_JSON=json.dumps({
                       'default_provider': 'clawmanager', 'default_model': 'auto',
                       'providers': [{'provider_id': 'clawmanager', 'model_ids': ['auto']}],
                       'api_key_env': 'CLAWMANAGER_LLM_API_KEY',
                       'base_url': 'http://127.0.0.1:%d/v1' % fixture.server_port,
                   }))

        def run(entry, *args):
            result = subprocess.run([node, entry, *args], env=env, cwd=root,
                                    text=True, encoding='utf-8', capture_output=True, timeout=180)
            if result.returncode:
                raise AssertionError(result.stderr[-6000:])
            return result.stdout

        run(old_entry, 'web', '--dump-config')
        user_patch = home / 'profiles' / 'web' / 'cordis.patch.yml'
        user_patch.write_text('[]\n# user configuration sentinel\n', encoding='utf-8')
        skill = home / 'skills' / 'fixture' / 'SKILL.md'
        skill.parent.mkdir(parents=True)
        skill.write_text('---\nname: fixture\ndescription: Migration fixture\n---\nFixture body\n', encoding='utf-8')
        workspace_file = root / 'user-file.txt'
        workspace_file.write_text('preserve workspace\n', encoding='utf-8')
        hashes = {path: hashlib.sha256(path.read_bytes()).hexdigest() for path in [user_patch, skill, workspace_file]}
        output = run(old_entry, '--profile', 'headless', '--patch', patch, 'Reply with pong.')
        session_ids = set()
        for path in (home / 'sessions').rglob('*'):
            session_ids.update(re.findall(r'session-[0-9a-f-]{36}', str(path)))
            if path.is_file():
                session_ids.update(re.findall(r'session-[0-9a-f-]{36}', path.read_bytes().decode('utf-8', errors='ignore')))
        assert len(session_ids) == 1, ('session IDs', session_ids)
        session_id = session_ids.pop()
        assert 'pong' in output, output
        run(str(runtime / 'scripts' / 'prepare-managed-config.mjs'), patch, new_entry)
        output = run(new_entry, '--profile', 'headless', '--patch', patch, '--json',
                     '--session-id', session_id, 'Continue and reply with pong.')
        assert 'pong' in output, output
        for path, expected in hashes.items():
            assert hashlib.sha256(path.read_bytes()).hexdigest() == expected, str(path)
        print('old session resumed by new Harness; user profile/skill/workspace preserved', flush=True)
    fixture.shutdown()


if __name__ == '__main__':
    main()
