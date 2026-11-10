"""Bound normal Web picker discovery; explicit refresh keeps the full budget."""
import hashlib
from pathlib import Path
import sys


def apply(root):
    path = root / "hermes_cli/inventory.py"
    raw = path.read_bytes()
    if hashlib.sha256(raw).hexdigest() != "db3710c659155baabc7f599acef8f737974f8c8fbad365f35f01890daaac1e01":
        raise ValueError("Lite picker budget patch requires exact reviewed source")
    source = raw.decode("utf-8")
    before = "        capabilities=True, featured=True, for_picker=True, fast_custom_probe=False,"
    if source.count(before) != 1:
        raise ValueError("Lite picker budget anchor changed")
    source = source.replace(before,
        "        # Lite Web: use the existing bounded discovery for ordinary opens.\n"
        "        # Explicit refresh keeps full live discovery; cached/declared rows remain fallback.\n"
        "        capabilities=True, featured=True, for_picker=True, fast_custom_probe=not refresh,")
    source = source.replace(
        "Visibility only: ``fast_custom_probe=False``\n    keeps the live probe of the current custom endpoint on its full 5s discovery budget.",
        "Lite Web uses the existing 1.5s discovery budget on normal opens; explicit refresh keeps 5s.")
    compile(source, str(path), "exec")
    path.write_text(source, encoding="utf-8", newline="\n")


if __name__ == "__main__":
    apply(Path(sys.argv[1]))
