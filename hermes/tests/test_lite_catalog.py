import importlib.util
import os
from pathlib import Path
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

SOURCE = Path(os.environ.get('LITE_ENVIRONMENT_FILE', Path(__file__).resolve().parents[1] / 'patches/hermes-agent/lite_environment.py'))
spec = importlib.util.spec_from_file_location('catalog_environment_test', SOURCE)
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)


class CatalogReadTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def skill(self, name, data=b'---\nname: sample\n---\nBody\n'):
        path = self.root / name / 'SKILL.md'
        path.parent.mkdir(parents=True)
        path.write_bytes(data)
        return path

    def test_snapshot_is_fresh_next_request_and_strict_decoding_is_preserved(self):
        path = self.skill('a', b'\xef\xbb\xbfName\r\nBody\rTail\xff')
        @guard.env_read_request
        def read():
            self.assertEqual([path], list(guard.prefetch_skill_files([path])))
            expected = path.read_text(encoding='utf-8-sig', errors='replace')
            path.write_text('updated', encoding='utf-8')
            self.assertEqual(expected, guard.read_skill_text(path, errors='replace'))
            with self.assertRaises(UnicodeDecodeError): guard.read_skill_text(path)
        read()
        @guard.env_read_request
        def fresh():
            list(guard.prefetch_skill_files([path]))
            return guard.read_skill_text(path)
        self.assertEqual('updated', fresh())
        path.unlink()
        with self.assertRaises(FileNotFoundError): guard.read_skill_text(path)

    def test_reads_are_parallel_bounded_and_not_repeated_by_catalog_consumers(self):
        paths = [self.skill(str(i)) for i in range(19)]
        original = guard._read_small_skill
        lock = threading.Lock()
        active = peak = reads = 0
        def slow(path):
            nonlocal active, peak, reads
            with lock:
                active += 1; reads += 1; peak = max(active, peak)
            try:
                time.sleep(.02)
                return original(path)
            finally:
                with lock: active -= 1
        @guard.env_read_request
        def catalog():
            for path in guard.prefetch_skill_files(iter(paths)):
                guard.read_skill_text(path, errors='replace')
                guard.read_skill_text(path)
            list(guard.prefetch_skill_files(paths))
        with patch.object(guard, '_read_small_skill', slow): catalog()
        self.assertEqual(len(paths), reads)
        self.assertGreater(peak, 1)
        self.assertLessEqual(peak, 8)

    def test_non_catalog_reads_and_large_files_keep_upstream_behavior(self):
        path = self.skill('a', b'x' * 32)
        with patch.object(guard, '_read_small_skill', side_effect=AssertionError('unexpected prefetch')):
            self.assertEqual([path], list(guard.prefetch_skill_files([path])))
        @guard.env_read_request
        def read():
            list(guard.prefetch_skill_files([path]))
            self.assertNotIn(guard._skill_text_key(path), guard._READ_REQUEST.get())
            self.assertEqual('x' * 32, guard.read_skill_text(path))
        with patch.object(guard, '_SKILL_TEXT_LIMIT', 8): read()

    def test_budget_paths_and_error_cleanup(self):
        a, b = self.skill('profile-a', b'a' * 8), self.skill('profile-b', b'b' * 8)
        missing = self.root / 'missing' / 'SKILL.md'
        @guard.env_read_request
        def read():
            list(guard.prefetch_skill_files([a, b, missing]))
            self.assertLessEqual(guard._READ_REQUEST.get()['skill-text-bytes'], 8)
            self.assertEqual('a' * 8, guard.read_skill_text(a))
            self.assertEqual('b' * 8, guard.read_skill_text(b))
            with self.assertRaises(FileNotFoundError): guard.read_skill_text(missing)
            raise RuntimeError('controlled')
        with patch.object(guard, '_SKILL_REQUEST_LIMIT', 8), self.assertRaisesRegex(RuntimeError, 'controlled'): read()
        self.assertIsNone(guard._READ_REQUEST.get())


if __name__ == '__main__':
    unittest.main()
