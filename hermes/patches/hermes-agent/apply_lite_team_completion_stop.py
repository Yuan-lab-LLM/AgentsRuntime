"""Adapt the Team completion boundary to the pinned v0.22.5 split tool round."""
import hashlib
from pathlib import Path
import sys
import textwrap
from apply_team_completion_stop import REPLACEMENT

EXPECTED = '1ccd3574528597f5c3a5589729849f8a10e422f0d970af3bb03b1fbd277f1f29'
ANCHOR = "    if agent._tool_guardrail_halt_decision is not None:\n"
# Keep the receipt predicate shared with Pro; the split phase returns a verdict.
BLOCK = textwrap.indent(textwrap.dedent(REPLACEMENT).replace(
    "    break\n\nif agent._tool_guardrail_halt_decision",
    '    return _verdict("break")\n\nif agent._tool_guardrail_halt_decision'), "    ")

def apply(path):
    raw = path.read_bytes()
    if hashlib.sha256(raw).hexdigest() != EXPECTED:
        raise ValueError("Team completion patch requires the locked v0.22.5 tool round")
    source = raw.decode("utf-8")
    if source.count(ANCHOR) != 1:
        raise ValueError("Team completion guardrail anchor changed")
    source = source.replace("import logging\n", "import logging\nimport json\n", 1)
    source = source.replace(ANCHOR, BLOCK)
    compile(source, str(path), "exec")
    path.write_text(source, encoding="utf-8", newline="\n")

if __name__ == "__main__":
    apply(Path(sys.argv[1]))
