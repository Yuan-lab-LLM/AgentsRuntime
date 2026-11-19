import contextlib
import importlib.util
import io
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


timing = load('startup_timing_test', ROOT / 'patches/hermes-agent/lite_startup_timing.py')
bytecode = load('precompile_test', ROOT / 'scripts/precompile_lite.py')


class PrehttpTests(unittest.TestCase):
    def test_timing_preserves_return_and_exception_without_arguments(self):
        output = io.StringIO()
        @timing.timed('plugins_load')
        def function(secret, fail=False):
            if fail:
                raise ValueError(secret)
            return secret
        with patch.object(timing, '_enabled', True), contextlib.redirect_stderr(output):
            self.assertEqual(function('sensitive-value'), 'sensitive-value')
            with self.assertRaisesRegex(ValueError, 'sensitive-value'):
                function('sensitive-value', True)
            timing.emit('sensitive-value', timing.clock())
        lines = output.getvalue().splitlines()
        self.assertEqual(len(lines), 2)
        self.assertNotIn('sensitive-value', output.getvalue())
        self.assertRegex(lines[0], r'^hermes_lite_startup phase=plugins_load action=done elapsed_ms=\d+$')
        self.assertRegex(lines[1], r'action=failed elapsed_ms=\d+$')

    def test_timing_disabled_and_broken_diagnostic_stream(self):
        output = io.StringIO()
        with patch.object(timing, '_enabled', False), contextlib.redirect_stderr(output):
            with timing.stage('web_import'):
                pass
        self.assertEqual(output.getvalue(), '')
        with patch.object(timing, '_enabled', True), patch('builtins.print', side_effect=OSError):
            with timing.stage('web_import'):
                pass

    def test_checked_hash_cache_cannot_hide_source_change(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / 'fixture.py'
            source.write_text('VALUE = 1\n', encoding='utf-8')
            with contextlib.redirect_stdout(io.StringIO()):
                bytecode.precompile(root)
            cache = Path(importlib.util.cache_from_source(str(source)))
            self.assertEqual(int.from_bytes(cache.read_bytes()[4:8], 'little'), 3)
            source.write_text('VALUE = 2\n', encoding='utf-8')  # same size, rejects stale .pyc
            result = subprocess.check_output([sys.executable, '-B', '-c', 'import fixture; print(fixture.VALUE)'], cwd=root, text=True)
            self.assertEqual(result.strip(), '2')

    def test_only_immutable_application_trees_are_compiled(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'hermes_cli').mkdir()
            (root / 'skills').mkdir()
            source = root / 'hermes_cli' / 'fixture.py'
            source.write_text('VALUE = 1\n', encoding='utf-8')
            (root / 'skills' / 'user.py').write_text('not valid python!', encoding='utf-8')
            with contextlib.redirect_stdout(io.StringIO()):
                bytecode.precompile(root)
            self.assertTrue(Path(importlib.util.cache_from_source(str(source))).exists())
            self.assertFalse((root / 'skills' / '__pycache__').exists())


if __name__ == '__main__':
    unittest.main()
