#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path


SHA40_RE = re.compile(r"[0-9a-f]{40}")
SHA256_RE = re.compile(r"[0-9a-f]{64}")
IMAGE_ID_RE = re.compile(r"sha256:[0-9a-f]{64}")
ROLE_RE = re.compile(r"[A-Za-z_][A-Za-z0-9_]{0,62}")
EXPECTED_RP_ID = "sorryiossearch.areasong.top"
EXPECTED_ORIGIN = f"https://{EXPECTED_RP_ID}"
EXPECTED_REPOSITORY = "AreaSong/sorryiosSearch"
EXPECTED_SBOM_PREDICATE = "https://cyclonedx.org/bom"
EXPECTED_TRIVY_PREDICATE = "https://areasong.top/attestations/trivy/v1"


def dotenv_values(path: Path) -> dict[str, str]:
    values: dict[str, str] = {}
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        values[key.strip()] = value
    return values


def release_roles(path: Path) -> tuple[str, str]:
    values = dotenv_values(path)
    required = (
        "POSTGRES_PASSWORD",
        "DATABASE_APP_USER",
        "DATABASE_APP_PASSWORD",
        "RP_ID",
        "ORIGIN",
        "SESSION_SECRET",
        "ENCRYPTION_KEY",
        "REGISTRATION_TOKEN",
    )
    missing = [name for name in required if not values.get(name)]
    if missing:
        raise ValueError("release environment is missing required values: " + ", ".join(missing))
    admin_user = values.get("POSTGRES_USER", "account_user")
    app_user = values["DATABASE_APP_USER"]
    if ROLE_RE.fullmatch(admin_user) is None or ROLE_RE.fullmatch(app_user) is None:
        raise ValueError("database role names must be safe PostgreSQL identifiers")
    if app_user == admin_user:
        raise ValueError("runtime database role must differ from the migration management role")
    if values["DATABASE_APP_PASSWORD"] == values["POSTGRES_PASSWORD"]:
        raise ValueError("runtime and migration database passwords must differ")
    return admin_user, app_user


def validate_environment(path: Path) -> None:
    values = dotenv_values(path)
    release_roles(path)
    if values["RP_ID"] != EXPECTED_RP_ID or values["ORIGIN"] != EXPECTED_ORIGIN:
        raise ValueError("RP_ID and ORIGIN must match the production Account Vault hostname")
    if len(values["POSTGRES_PASSWORD"]) < 20 or len(values["DATABASE_APP_PASSWORD"]) < 20:
        raise ValueError("database passwords must contain at least 20 characters")
    if len(values["SESSION_SECRET"]) < 32:
        raise ValueError("SESSION_SECRET must contain at least 32 characters")
    if re.fullmatch(r"[0-9a-fA-F]{64}", values["ENCRYPTION_KEY"]) is None:
        raise ValueError("ENCRYPTION_KEY must contain exactly 64 hexadecimal characters")
    if len(values["REGISTRATION_TOKEN"]) < 32:
        raise ValueError("REGISTRATION_TOKEN must contain at least 32 characters")


def validate_evidence(path: Path, image: str) -> tuple[str, str]:
    evidence = json.loads(path.read_text(encoding="utf-8"))
    sha = str(evidence.get("gitSha", ""))
    expected_tag = f"ghcr.io/areasong/sorryiossearch:sha-{sha}"
    subject_digest = image.rsplit("@", 1)[-1]
    image_id = str(evidence.get("candidateImageId", ""))
    workflow_ref = str(evidence.get("workflowRef", ""))
    checks = (
        evidence.get("stage") == "published",
        evidence.get("repositoryDigest") == image,
        SHA40_RE.fullmatch(sha) is not None,
        evidence.get("tag") == expected_tag,
        evidence.get("migrationPolicy") == "expand-only",
        SHA256_RE.fullmatch(str(evidence.get("migrationTreeSha256", ""))) is not None,
        SHA256_RE.fullmatch(str(evidence.get("sbomSha256", ""))) is not None,
        SHA256_RE.fullmatch(str(evidence.get("trivyReportSha256", ""))) is not None,
        IMAGE_ID_RE.fullmatch(image_id) is not None,
        evidence.get("provenance") == "github-build-provenance",
        evidence.get("sbomAttestationPredicate") == EXPECTED_SBOM_PREDICATE,
        evidence.get("trivyAttestationPredicate") == EXPECTED_TRIVY_PREDICATE,
        evidence.get("attestedSubjectDigest") == subject_digest,
        evidence.get("repository") == EXPECTED_REPOSITORY,
        workflow_ref.startswith(f"{EXPECTED_REPOSITORY}/.github/workflows/ci.yml@"),
        str(evidence.get("runId", "")).isdigit(),
        str(evidence.get("runAttempt", "")).isdigit(),
    )
    if not all(checks):
        raise ValueError("published release evidence does not match the approved digest and policy")
    return sha, image_id


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Validate Account Vault release inputs.")
    subparsers = parser.add_subparsers(dest="command", required=True)
    environment = subparsers.add_parser("environment")
    environment.add_argument("path", type=Path)
    evidence = subparsers.add_parser("evidence")
    evidence.add_argument("path", type=Path)
    evidence.add_argument("image")
    roles = subparsers.add_parser("roles")
    roles.add_argument("path", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        if args.command == "environment":
            validate_environment(args.path)
        elif args.command == "roles":
            print("\t".join(release_roles(args.path)))
        else:
            print("\t".join(validate_evidence(args.path, args.image)))
    except (OSError, ValueError, json.JSONDecodeError) as error:
        print(str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
