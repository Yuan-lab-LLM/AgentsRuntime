"""Keep managed Lite custom routes at their pre-0.22.5 unset default.

Applied only by Lite builds. Both the Redis Team gateway and the web/TUI
runner use this provider. Explicit user/per-model effort remains unchanged.
"""
import hashlib
from pathlib import Path
import sys

SOURCE_SHA256 = "e0bebb5675726793c87be3ba12d2cce088742106c852f142bcdf74f1d0156d98"
BEFORE = '        return {"enabled": True, "effort": "medium"}'
AFTER = '        return None  # CM opaque routes own unset reasoning; explicit effort still passes through.'


def rewrite(source):
    if source.count(BEFORE) != 1:
        raise ValueError("managed reasoning source anchor changed")
    return source.replace(BEFORE, AFTER)


def apply(root):
    target = Path(root) / "plugins/model-providers/custom/__init__.py"
    source = target.read_bytes()
    if hashlib.sha256(source).hexdigest() != SOURCE_SHA256:
        raise ValueError("managed reasoning requires exact locked native source")
    output = rewrite(source.decode("utf-8"))
    compile(output, str(target), "exec")
    target.write_text(output, encoding="utf-8", newline="\n")


if __name__ == "__main__":
    apply(sys.argv[1])
