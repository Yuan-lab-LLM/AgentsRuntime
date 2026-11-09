"""Serve missing optional model metadata without a foreground network fetch."""
import hashlib
from pathlib import Path
import sys


def apply(root):
    path = root / "agent/models_dev.py"
    raw = path.read_bytes()
    if hashlib.sha256(raw).hexdigest() != "92bab26726beae0259989c75316167134b0f67a243a5faa4a29c5f6e4ac9f30d":
        raise ValueError("Lite models metadata patch requires exact reviewed source")
    source = raw.decode("utf-8")
    before = "    # Stage 4: singleflight foreground fetch. Recheck state under the lock"
    if source.count(before) != 1:
        raise ValueError("Lite model metadata anchor changed")
    source = source.replace(before,
        "    # Lite: optional metadata must not block a cold picker/startup.\n"
        "    # Keep upstream backoff, singleflight and explicit refresh semantics.\n"
        "    if not force_refresh:\n"
        "        _start_background_refresh_models_dev()\n"
        "        return _models_dev_cache\n"
        + before)
    compile(source, str(path), "exec")
    path.write_text(source, encoding="utf-8", newline="\n")


if __name__ == "__main__":
    apply(Path(sys.argv[1]))
