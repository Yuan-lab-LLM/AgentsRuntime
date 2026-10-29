"""The DSH child sandbox must inherit only the outer instance's private /tmp."""
from pathlib import Path
import importlib.util
import json
import subprocess
import unittest

SCRIPT = Path(__file__).resolve().parents[2] / 'deepseek-harness/scripts/patch-persistent-tmp.py'
ORIGINAL = '''function bwrapProfileArgs(policy) {
 const args = ["--ro-bind", "/", "/", "--unshare-pid", "--proc", "/proc"];
 if (policy.mode === "workspace-write") {
  args.push("--tmpfs", "/tmp");
  args.push("--bind", policy.workspaceRoot, policy.workspaceRoot);
 }
 return args;
}
'''

class PersistentTmpPatch(unittest.TestCase):
    def test_write_profile_inherits_tmp_and_read_only_profile_stays_read_only(self):
        self.assertTrue(SCRIPT.is_file(), 'Lite image needs a checked patch for nested DSH tmpfs')
        spec = importlib.util.spec_from_file_location('dsh_tmp_patch', SCRIPT)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        patched = module.patch_text(ORIGINAL)
        source = patched + '\nconsole.log(JSON.stringify([bwrapProfileArgs({mode:"workspace-write",workspaceRoot:"/workspace"}), bwrapProfileArgs({mode:"read-only",workspaceRoot:"/workspace"})]));'
        result = subprocess.run(['node','--input-type=commonjs'], input=source, text=True, capture_output=True, check=True)
        write, read = json.loads(result.stdout)
        self.assertEqual(write, ['--ro-bind','/','/','--unshare-pid','--proc','/proc','--bind','/tmp','/tmp','--bind','/workspace','/workspace'])
        self.assertEqual(read, ['--ro-bind','/','/','--unshare-pid','--proc','/proc'])
        with self.assertRaises(ValueError): module.patch_text(ORIGINAL + ORIGINAL)
        with self.assertRaises(ValueError): module.patch_text(patched)

if __name__ == '__main__': unittest.main()
