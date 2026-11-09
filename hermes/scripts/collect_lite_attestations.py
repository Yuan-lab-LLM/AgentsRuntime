#!/usr/bin/env python3
"""Extract and verify the candidate's content-addressed SBOM/provenance records."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import tarfile


def digest_file(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def collect(oci, metadata_path, output):
    metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
    candidate = metadata["containerimage.digest"]
    if output.exists():
        raise ValueError("Refusing to overwrite an existing attestation directory")
    with tarfile.open(oci, "r") as archive:
        def blob(digest):
            if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
                raise ValueError("Invalid OCI digest")
            raw = archive.extractfile("blobs/sha256/" + digest[7:]).read()
            if hashlib.sha256(raw).hexdigest() != digest[7:]:
                raise ValueError("OCI attestation/manifest checksum mismatch")
            return json.loads(raw)
        index = blob(candidate)
        platforms = [item for item in index["manifests"] if item.get("platform", {}).get("os") == "linux"
                     and item["platform"].get("architecture") == "amd64"]
        if len(platforms) != 1:
            raise ValueError("Expected exactly one tested linux/amd64 platform")
        platform_digest = platforms[0]["digest"]
        manifest = blob(platform_digest)
        records = {}
        for descriptor in index["manifests"]:
            attestation = blob(descriptor["digest"])
            for layer in attestation.get("layers", []):
                if layer["mediaType"] != "application/vnd.in-toto+json":
                    continue
                statement = blob(layer["digest"])
                subjects = statement.get("subject", [])
                if not subjects or any(item.get("digest", {}).get("sha256") != platform_digest[7:] for item in subjects):
                    raise ValueError("Attestation does not reference the tested platform digest")
                predicate_type = statement["predicateType"]
                if "spdx" in predicate_type:
                    name = statement["predicate"]["name"]
                    if name not in {"sbom", "sbom-session-worker-builder"}:
                        raise ValueError("Unexpected SBOM subject")
                    filename = name + ".spdx.json"
                elif "slsa" in predicate_type:
                    filename = "provenance.json"
                else:
                    raise ValueError("Unexpected attestation predicate")
                if filename in records:
                    raise ValueError("Duplicate attestation")
                records[filename] = statement
        if set(records) != {"sbom.spdx.json", "sbom-session-worker-builder.spdx.json", "provenance.json"}:
            raise ValueError("Missing runtime/bundled-dependency SBOM or provenance")
    output.mkdir(parents=True)
    summary = {"schema_version": 1, "candidate_image_digest": candidate,
               "linux_amd64_manifest_digest": platform_digest, "config_digest": manifest["config"]["digest"],
               "oci_archive_sha256": digest_file(oci), "oci_archive_bytes": oci.stat().st_size,
               "build_metadata_sha256": digest_file(metadata_path), "attestations": {}}
    for filename, statement in records.items():
        value = statement["predicate"] if filename.endswith(".spdx.json") else statement
        path = output / filename
        path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8", newline="\n")
        summary["attestations"][filename] = {"sha256": digest_file(path), "subject": platform_digest}
        if filename.endswith(".spdx.json"):
            summary["attestations"][filename]["package_records"] = len(value["packages"])
    (output / "artifact-summary.json").write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8", newline="\n")
    return summary


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--oci", type=Path, required=True)
    parser.add_argument("--metadata", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    summary = collect(args.oci, args.metadata, args.output)
    print(json.dumps(summary, sort_keys=True))


if __name__ == "__main__":
    main()
