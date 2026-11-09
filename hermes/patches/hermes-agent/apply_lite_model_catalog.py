"""Keep cold Lite model-picker requests independent of remote manifest latency."""
import hashlib
from pathlib import Path
import sys

RELATIVE = "hermes_cli/model_catalog.py"
EXPECTED = "80dd5e199de1dc5cac7eab22bf3030d1ad659645fbf01c1ef9c106384451e832"


def apply(root):
    path = root / RELATIVE
    raw = path.read_bytes()
    if hashlib.sha256(raw).hexdigest() != EXPECTED:
        raise ValueError("Lite model catalog patch requires exact reviewed source")
    before = '    fetched = _fetch_manifest_with_fallback(cfg["url"], DEFAULT_FETCH_TIMEOUT)\n'
    after = (
        '    # Lite: the first picker open must work without outbound Internet.\n'
        '    # Existing accessors already fall back to bundled model lists on {}.\n'
        '    # Reuse the upstream profile-scoped, deduplicated background refresh.\n'
        '    # An explicit refresh still retains the upstream blocking contract.\n'
        '    if not force_refresh:\n'
        '        _spawn_catalog_swr_refresh(cfg["url"])\n'
        '        return _in_process_catalog() or {}\n\n' + before
    )
    source = raw.decode("utf-8")
    if source.count(before) != 1:
        raise ValueError("Lite model catalog patch anchor changed")
    source = source.replace(before, after)
    compile(source, str(path), "exec")
    path.write_text(source, encoding="utf-8", newline="\n")


if __name__ == "__main__":
    apply(Path(sys.argv[1]))
