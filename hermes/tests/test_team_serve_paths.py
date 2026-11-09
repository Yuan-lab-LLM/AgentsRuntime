"""HTTP and RPC share the same member-scoped filesystem policy."""
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


def load(name):
    path = Path(__file__).resolve().parents[1] / 'patches/hermes-agent' / (name + '.py')
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class TeamPaths(unittest.TestCase):
    def test_only_assigned_team_share_is_allowed(self):
        policies = [load('lite_gateway_boundary').workspace_path_allowed,
                    load('lite_non_native')._workspace_path_allowed]
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            workspace, shared, foreign = (root / name for name in ('workspace', 'assigned-team', 'other-team'))
            for path in (workspace, shared, foreign):
                path.mkdir()
            with patch.dict(os.environ, {'CLAWMANAGER_WORKSPACE_PATH': workspace.as_posix(),
                                         'CLAWMANAGER_TEAM_SHARED_DIR': shared.as_posix(),
                                         'CLAWMANAGER_TEAM_ENABLED': 'true'}):
                for allow in policies:
                    self.assertTrue(allow((workspace / 'file.md').as_posix()))
                    self.assertTrue(allow((shared / 'report.md').as_posix()))
                    self.assertFalse(allow((foreign / 'report.md').as_posix()))
                    self.assertFalse(allow('../other-team/report.md'))
                    self.assertFalse(allow({}))
                    self.assertFalse(allow([]))
                os.environ['CLAWMANAGER_TEAM_ENABLED'] = 'false'
                for allow in policies:
                    self.assertFalse(allow((shared / 'report.md').as_posix()))

    @unittest.skipIf(os.name == 'nt', 'requires Linux symlink semantics')
    def test_symlink_to_another_member_is_denied(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            work, foreign = root / 'work', root / 'foreign'
            work.mkdir(); foreign.mkdir()
            (work / 'escape').symlink_to(foreign, target_is_directory=True)
            with patch.dict(os.environ, {'CLAWMANAGER_WORKSPACE_PATH': str(work), 'CLAWMANAGER_TEAM_ENABLED': 'false'}):
                self.assertFalse(load('lite_gateway_boundary').workspace_path_allowed(str(work / 'escape/private')))
                self.assertFalse(load('lite_non_native')._workspace_path_allowed(str(work / 'escape/private')))
