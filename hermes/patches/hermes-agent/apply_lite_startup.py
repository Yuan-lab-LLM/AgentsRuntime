"""Apply timings to exact sources after the existing Lite boundary patches."""
import hashlib
from pathlib import Path
import shutil
import sys

EXPECTED = {'hermes_cli/main.py': '95635aea97728e2759f9158210ae60fcfb7141610f9e7cbc2182021d9a50d416', 'hermes_cli/web_server.py': '7fa4ee8ff32d393b4401b890bfbbaba89ad9cb40e4b733bf2d2ff4be26edf990', 'hermes_cli/web_server_lifecycle.py': '2a486c40f5a5936c64d6f722720447d656ae02225aea443c323772bd8adfd2c5', 'hermes_cli/web_server_app.py': 'bb3f151bea79188e18b3df082db7adaabda79c47cd8974de576c7f997fbb5aac', 'tools/skills_sync.py': '00889977cc54f579888b147c9884adfb9b2963a162194010b33c7f36036d63a0', 'tools/skills_sync_optional.py': 'c77a6515a75a03f838c0f044adddc99cc9b4aa6784763fa17f462f2e4f4c420e'}


def once(source, before, after):
    if source.count(before) != 1:
        raise ValueError('Lite startup patch anchor changed')
    return source.replace(before, after)


IMPORT = 'from hermes_cli.lite_startup_timing import clock as _lite_clock, emit as _lite_emit, stage as _lite_stage, timed as _lite_timed\n'


def patched_sources(root):
    sources = {}
    for relative, digest in EXPECTED.items():
        raw = (root / relative).read_bytes()
        if hashlib.sha256(raw).hexdigest() != digest:
            raise ValueError('Lite startup patch requires exact post-boundary sources')
        sources[relative] = raw.decode('utf-8')
    key = 'hermes_cli/main.py'
    s = sources[key]
    s = once(s, 'from hermes_cli import _early_recovery as _early_recovery_mod', IMPORT + '_lite_cli_started = _lite_clock()\n\nfrom hermes_cli import _early_recovery as _early_recovery_mod')
    s = once(s, 'if __name__ == "__main__":\n    main()', '_lite_emit("cli_import", _lite_cli_started)\n\nif __name__ == "__main__":\n    main()')
    s = once(s, 'def _dashboard_prepare_runtime(args, headless_backend) -> bool:', '@_lite_timed("prepare_runtime")\ndef _dashboard_prepare_runtime(args, headless_backend) -> bool:')
    for before, phase in [
        ('    _require_dashboard_web_deps()\n', 'web_deps'),
        ('    _sync_bundled_skills_quietly()\n', 'skills_sync'),
        ('        apply_terminal_config_to_env()\n', 'terminal_config'),
        ('    from hermes_cli.web_server import start_server\n', 'web_import'),
    ]:
        indent = before[:len(before)-len(before.lstrip())]
        s = once(s, before, indent + f'with _lite_stage("{phase}"):\n' + '    ' + before)
    s = once(s, '        from hermes_cli.plugins import discover_plugins\n        discover_plugins()\n', '        with _lite_stage("plugins_load"):\n            from hermes_cli.plugins import discover_plugins\n            discover_plugins()\n')
    # Only dispatch work before entering cmd_dashboard, excluding its long-lived server.
    s = once(s, '    cmd_dashboard(args)\n    return True', '    _lite_emit("cli_dispatch", globals().get("_lite_main_started", _lite_cli_started))\n    cmd_dashboard(args)\n    return True')
    s = once(s, 'def main():\n', 'def main():\n    global _lite_main_started\n    _lite_main_started = _lite_clock()\n')
    sources[key] = s
    key = 'tools/skills_sync.py'
    s = once(sources[key], 'logger = logging.getLogger(__name__)', IMPORT + '\n_backfill_optional_provenance = _lite_timed("skills_optional")(_backfill_optional_provenance)\n\nlogger = logging.getLogger(__name__)')
    before = '''    found = root.rglob("SKILL.md") if root.exists() else iter(())
    for skill_md in sorted(found) if sort else found:
        if not is_excluded_skill_path(skill_md):
            yield skill_md'''
    s = once(s, before, '    from hermes_cli.lite_skills_sync import iter_skill_mds\n    yield from iter_skill_mds(root, sort=sort)')
    s = once(s, 'def _build_external_skill_index() -> set[str]:', '@_lite_timed("skills_external")\ndef _build_external_skill_index() -> set[str]:')
    s = once(s, '                hasher.update(fpath.read_bytes())', '                file_hasher = hasher.copy()\n                with fpath.open("rb") as stream:\n                    while chunk := stream.read(128 * 1024):\n                        file_hasher.update(chunk)\n                hasher = file_hasher')
    sources[key] = s
    key = 'tools/skills_sync_optional.py'
    s = once(sources[key], '    installed_dir_index: Optional[dict[str, list[Path]]] = None\n', '    installed_dir_index: Optional[dict[str, list[Path]]] = None\n    candidates = []\n')
    s = once(s, '        if install_path in existing_paths or ss.dir_hash(dest) != ss.dir_hash(src):\n            continue\n        timestamp = datetime.now(UTC).isoformat()', '        if install_path not in existing_paths:\n            candidates.append((lock_name, src, dest, install_path))\n    from hermes_cli.lite_skills_sync import matching_candidates\n    for lock_name, src, dest, install_path in matching_candidates(candidates, ss.dir_hash):\n        if lock_name in installed or install_path in existing_paths:\n            continue\n        timestamp = datetime.now(UTC).isoformat()')
    sources[key] = s
    key = 'hermes_cli/web_server.py'
    s = once(sources[key], 'def start_server(\n', IMPORT + '\n\ndef start_server(\n')
    s = once(s, '    _apply_ssh_session_token(ssh_session_token or "")', '    _lite_server_started = _lite_clock()\n    _apply_ssh_session_token(ssh_session_token or "")')
    s = once(s, '            await server.startup()\n', '            with _lite_stage("http_bind"):\n                await server.startup()\n')
    s = once(s, '    _run_serve(_serve, config, host, port)', '    _lite_emit("server_setup", _lite_server_started)\n    _run_serve(_serve, config, host, port)')
    sources[key] = s
    key = 'hermes_cli/web_server_lifecycle.py'
    s = once(sources[key], 'def _warm_gateway_module() -> None:', IMPORT + '\n\ndef _warm_gateway_module() -> None:')
    phases = ('gateway_warm', 'auth_warm', 'copilot_warm', 'provider_warm', 'skin_warm', 'inventory_warm', 'model_switch_warm')
    s = once(s, '    for mod in (\n', '    for phase, mod in zip(' + repr(phases) + ', (\n')
    s = once(s, '        "hermes_cli.model_switch",\n    ):', '        "hermes_cli.model_switch",\n    )):')
    s = once(s, '            __import__(mod)\n', '            with _lite_stage(phase):\n                __import__(mod)\n')
    sources[key] = s
    key = 'hermes_cli/web_server_app.py'
    s = once(sources[key], 'logger = logging.getLogger', IMPORT + '\nlogger = logging.getLogger')
    s = once(s, 'async def standalone_lifespan(app: "FastAPI"):\n', 'async def standalone_lifespan(app: "FastAPI"):\n    _lite_lifespan_started = _lite_clock()\n')
    s = once(s, '    record_boot_fingerprint()\n', '    with _lite_stage("boot_fingerprint"):\n        record_boot_fingerprint()\n')
    s = once(s, '    from tui_gateway import methods_groups as _hosted_groups\n    import tui_gateway.server', '    with _lite_stage("hosted_import"):\n        from tui_gateway import methods_groups as _hosted_groups\n        import tui_gateway.server')
    s = once(s, '    start_background_bootstrap()\n\n    try:', '    start_background_bootstrap()\n    _lite_emit("lifespan_prepare", _lite_lifespan_started)\n\n    try:')
    sources[key] = s
    for relative, source in sources.items():
        compile(source, relative, 'exec')
    return sources


def apply(root):
    sources = patched_sources(root)
    for relative, source in sources.items():
        (root / relative).write_text(source, encoding='utf-8', newline='\n')
    shutil.copyfile(Path(__file__).with_name('lite_startup_timing.py'), root / 'hermes_cli/lite_startup_timing.py')
    shutil.copyfile(Path(__file__).with_name('lite_skills_sync.py'), root / 'hermes_cli/lite_skills_sync.py')


if __name__ == '__main__':
    apply(Path(sys.argv[1]))
