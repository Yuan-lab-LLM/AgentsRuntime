import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('skills_startup', ROOT / 'patches/hermes-agent/lite_skills_sync.py')
fast = importlib.util.module_from_spec(spec); spec.loader.exec_module(fast)


class HashWorkersTests(unittest.TestCase):
    def test_bounded_ordered_reads_recheck_changes_and_propagate_errors(self):
        lock=threading.Lock(); active=0; peak=0
        def hash_dir(path):
            nonlocal active, peak
            with lock:
                active+=1;peak=max(peak,active)
            try:
                time.sleep(.005)
                return Path(path).read_bytes()
            finally:
                with lock:active-=1
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary); candidates=[]
            for i in range(12):
                src=root/f'src-{i}';dst=root/f'dst-{i}'
                src.write_bytes(b'original');dst.write_bytes(b'original')
                candidates.append((str(i),src,dst,str(i)))
            self.assertEqual(list(fast.matching_candidates(candidates,hash_dir)),candidates)
            self.assertLessEqual(peak,4);self.assertGreater(peak,1)
            candidates[0][2].write_bytes(b'changed')
            self.assertEqual(list(fast.matching_candidates(candidates,hash_dir)),candidates[1:])
            with self.assertRaises(OSError):
                list(fast.matching_candidates(candidates,lambda _: (_ for _ in ()).throw(OSError('read failed'))))


@unittest.skipUnless(importlib.util.find_spec('hermes_state'), 'requires pinned installed Hermes')
class InstalledSkillTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name)

    def make(self,relative,content='same'):
        p=self.root/relative;p.parent.mkdir(parents=True,exist_ok=True);p.write_text(content,encoding='utf-8');return p

    def test_pruning_preserves_discovery_and_sees_edits_without_following_links(self):
        from agent.skill_utils import is_excluded_skill_path
        for name in ('normal/SKILL.md','normal/scripts/hidden/SKILL.md','normal/assets/hidden/SKILL.md',
                     'normal/.venv/hidden/SKILL.md','.archive/hidden/SKILL.md','scripts/category/SKILL.md',
                     'normal/child/SKILL.md'):
            self.make(name)
        outside=self.root/'outside';outside.mkdir();(outside/'SKILL.md').write_text('outside')
        (self.root/'normal/link').symlink_to(outside,target_is_directory=True)
        original=sorted(p for p in self.root.rglob('SKILL.md') if not is_excluded_skill_path(p))
        self.assertEqual(list(fast.iter_skill_mds(self.root,sort=True)),original)
        fresh=self.make('added/SKILL.md')
        self.assertIn(fresh,list(fast.iter_skill_mds(self.root)))
        fresh.unlink();self.assertNotIn(fresh,list(fast.iter_skill_mds(self.root)))

    def test_streamed_hash_retains_exact_upstream_format(self):
        from tools import skills_sync as ss
        self.make('SKILL.md');self.make('data.bin','x'*(400*1024));self.make('__pycache__/ignored.pyc','ignored')
        def old_hash():
            h=hashlib.md5()
            for p in sorted(self.root.rglob('*')):
                if not ss._is_runtime_cache(p,self.root) and p.is_file():
                    h.update(str(p.relative_to(self.root)).encode('utf-8'));h.update(p.read_bytes())
            return h.hexdigest()
        self.assertEqual(ss.dir_hash(self.root),old_hash())

    def test_optional_provenance_preserves_modified_deleted_and_existing_installs(self):
        from tools import skills_sync as ss, skills_sync_optional as optional
        source=self.root/'source';active=self.root/'active'
        active.mkdir();(active/'.hub').mkdir()
        for name in ('same','edited','deleted','relocated','ambiguous','existing'):
            self.make(f'source/category/{name}/SKILL.md',f'---\nname: {name}\n---\nOriginal')
        for name in ('same','edited','existing'):
            shutil.copytree(source/'category'/name,active/'category'/name)
        shutil.copytree(source/'category/relocated',active/'other/relocated')
        shutil.copytree(source/'category/ambiguous',active/'one/ambiguous')
        shutil.copytree(source/'category/ambiguous',active/'two/ambiguous')
        (active/'category/edited/SKILL.md').write_text('user customized')
        existing={'version':1,'installed':{'existing':{'install_path':'category/existing','source':'user'}}}
        (active/'.hub/lock.json').write_text(json.dumps(existing))
        before={str(p.relative_to(active)):p.read_bytes() for p in active.rglob('SKILL.md')}
        with patch.object(ss,'_skills_dir',return_value=active),patch.object(ss,'_get_optional_dir',return_value=source):
            result=optional._backfill_optional_provenance(quiet=True)
            self.assertEqual(set(result),{'same','relocated'})
            self.assertEqual(optional._backfill_optional_provenance(quiet=True),[])
        data=json.loads((active/'.hub/lock.json').read_text())
        self.assertEqual(data['installed']['existing'],existing['installed']['existing'])
        self.assertEqual(data['installed']['relocated']['install_path'],'other/relocated')
        self.assertFalse((active/'category/deleted').exists())
        self.assertEqual(before,{str(p.relative_to(active)):p.read_bytes() for p in active.rglob('SKILL.md')})


if __name__ == '__main__':unittest.main()
