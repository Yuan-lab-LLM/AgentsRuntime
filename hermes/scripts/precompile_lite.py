"""Build-only checked-hash bytecode for immutable application source.

Keep PYTHONDONTWRITEBYTECODE at runtime: instances neither compile into shared
source directories nor write their HOME just to cache installed application code.
Checked hashes reject stale bytecode after source overlays, including hotfixes.
"""
from pathlib import Path
import py_compile

APPLICATION_TREES = (
    'hermes_cli', 'agent', 'gateway', 'tools', 'tui_gateway', 'providers',
    'plugins', 'cron', 'pm', 'hermes_platform', 'acp_adapter',
)


def precompile(root):
    root = Path(root).resolve()
    sources = list(root.glob('*.py'))
    for name in APPLICATION_TREES:
        sources.extend((root / name).rglob('*.py'))
    for source in sorted(sources):
        if source.is_symlink() or not source.resolve().is_relative_to(root):
            raise ValueError('application source must stay inside immutable root')
        py_compile.compile(str(source), doraise=True,
                           invalidation_mode=py_compile.PycInvalidationMode.CHECKED_HASH)
    print(f'Precompiled {len(sources)} immutable application sources')


if __name__ == '__main__':
    precompile('/opt/hermes-agent')
