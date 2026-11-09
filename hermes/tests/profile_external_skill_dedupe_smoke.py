#!/usr/bin/env python3
"""Container smoke tests for Hermes profile/external skill de-duplication."""

from __future__ import annotations

import json
import os
import shutil
import sys
import tempfile
from pathlib import Path


def main() -> int:
    if len(sys.argv) != 2 or sys.argv[1] not in {"identical", "old-managed", "modified"}:
        raise SystemExit(f"usage: {Path(sys.argv[0]).name} identical|old-managed|modified")
    mode = sys.argv[1]

    with tempfile.TemporaryDirectory(prefix="hermes-profile-skill-dedupe-") as raw_root:
        root = Path(raw_root)
        profile = root / "profile"
        shared = root / "shared"
        local_skill = profile / "skills" / "autonomous-ai-agents" / "hermes-agent"
        shared_skill = shared / "autonomous-ai-agents" / "hermes-agent"
        source_root = Path("/opt/hermes-agent")
        if not source_root.is_dir():
            source_root = Path("/usr/local/lib/hermes-agent")
        bundled_skill = source_root / "skills/autonomous-ai-agents/hermes-agent"

        profile.mkdir(parents=True)
        shared_skill.parent.mkdir(parents=True)
        local_skill.parent.mkdir(parents=True)
        shutil.copytree(bundled_skill, shared_skill)
        shutil.copytree(bundled_skill, local_skill)
        (profile / "config.yaml").write_text(
            f"skills:\n  external_dirs:\n    - {shared.as_posix()}\n",
            encoding="utf-8",
        )

        os.environ["HERMES_HOME"] = str(profile)
        os.environ["HOME"] = str(root / "home")
        sys.path.insert(0, str(source_root))

        from tools import skills_sync

        manifest_path = profile / "skills" / ".bundled_manifest"
        bundled_hash = skills_sync._dir_hash(bundled_skill)
        if mode == "old-managed":
            with (local_skill / "SKILL.md").open("a", encoding="utf-8") as handle:
                handle.write("\n<!-- simulated older managed revision -->\n")
            old_origin_hash = skills_sync._dir_hash(local_skill)
            manifest_path.write_text(
                f"hermes-agent:{old_origin_hash}\n", encoding="utf-8"
            )
        elif mode == "modified":
            manifest_path.write_text(
                f"hermes-agent:{bundled_hash}\n", encoding="utf-8"
            )
            with (local_skill / "SKILL.md").open("a", encoding="utf-8") as handle:
                handle.write("\n<!-- intentional user customization -->\n")

        result = skills_sync.sync_skills(quiet=True)
        assert "hermes-agent" in result["shadowed_by_external"], result
        assert shared_skill.is_dir(), "external canonical skill was removed"

        if mode == "modified":
            assert local_skill.is_dir(), "user-modified skill was deleted"
            assert "hermes-agent" in result["external_conflicts"], result
        else:
            assert not local_skill.exists(), "managed local shadow was not removed"
            assert "hermes-agent" not in result["external_conflicts"], result

            from tools.skills_tool import skill_view

            viewed = json.loads(skill_view("hermes-agent", preprocess=False))
            assert viewed.get("success") is True, viewed
            assert "Ambiguous skill name" not in viewed.get("error", ""), viewed

        print(json.dumps({"mode": mode, "result": result}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
