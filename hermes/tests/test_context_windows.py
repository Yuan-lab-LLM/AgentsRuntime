import ast
from pathlib import Path
import unittest

script = Path(__file__).resolve().parents[1] / "rootfs/usr/local/bin/hermes-apply-runtime-config"
source = script.read_text(encoding="utf-8").split("<<'PY'\n",1)[1].rsplit("\nPY",1)[0]
tree = ast.parse(source)
helper = next(n for n in tree.body if isinstance(n,ast.FunctionDef) and n.name == "apply_managed_context_windows")
scope = {}
exec(compile(ast.Module(body=[helper],type_ignores=[]),str(script),"exec"),scope)
apply = scope["apply_managed_context_windows"]

class ContextTest(unittest.TestCase):
    def test_write_refresh_and_preserve(self):
        owned={"base_url":"http://gateway/v1","key_env":"OPENAI_API_KEY","models":{"auto":{"keep":True},"other":{}}}
        foreign={"base_url":"http://user/v1","key_env":"USER_KEY","models":{"auto":{}}}
        providers={"vendor":owned,"user":foreign}
        apply(providers,{"vendor/auto":64000},32000,"http://gateway/v1")
        self.assertEqual(owned["models"]["auto"]["context_length"],64000)
        self.assertEqual(owned["models"]["other"]["context_length"],32000)
        apply(providers,{"auto":4096},0,"http://gateway/v1")
        self.assertEqual(owned["models"]["auto"]["context_length"],4096)
        self.assertTrue(owned["models"]["auto"]["keep"])
        self.assertEqual(foreign["models"]["auto"],{})
    def test_invalid(self):
        for value in [True,-1,1.5,2147483648]:
            with self.assertRaises(ValueError): apply({}, {"auto":value},0,"http://gateway")

if __name__ == "__main__": unittest.main()
