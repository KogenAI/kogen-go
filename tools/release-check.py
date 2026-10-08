#!/usr/bin/env python3
"""Audit release evidence without running providers or changing source inputs.

The historical v1.2 run is summarized as a compatibility record only. Release
admission requires the coordinator-frozen v1.3 oracle, full G replay, accepted
closure receipts, macOS and Linux check/race evidence, offline measurements,
and a separately completed eligible live cache replay.
"""

from __future__ import annotations

import argparse
import hashlib
import importlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from typing import Any

sys.dont_write_bytecode = True

ROOT = Path(__file__).resolve().parents[1]
HISTORICAL_PROFILES = (
    "cli", "state", "approval", "shape", "build", "ladder", "provider",
    "custody", "format", "v1.2",
)
EXPECTED_HISTORICAL_CASES = 236
EXPECTED_HISTORICAL_INSTANCES = 570
EXPECTED_HISTORICAL_SUITE_REVISION = "0f93bad988fb8d7a8eff4e94954d1db0a046c89d"
EXPECTED_HISTORICAL_INPUT_SHA256 = "527c68f52015b2f1628169c6c22dde992aa4c7642db4c974218b337b3db22b89"
DRAFT_COMMIT = "e19dd1c21c19c5be1201c3b6a42c59c28b5c2887"
G_SLICES = ("intent", "approve", "queue", "status", "recovery", "rebase", "stream", "session")
D_SLICES = ("gate", "orchestration", "accounts", "setup-cache")
SEEDS = ("17", "23", "41")
HEX64 = re.compile(r"^[0-9a-f]{64}$")
STATUSES = {"pass", "fail", "error", "skip", "unimplemented"}


class ReleaseError(ValueError):
    pass


def _no_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ReleaseError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def read_json(path: Path) -> Any:
    try:
        return json.loads(
            path.read_text(encoding="utf-8"),
            object_pairs_hook=_no_duplicate_keys,
            parse_constant=lambda value: (_ for _ in ()).throw(ReleaseError(f"invalid JSON number: {value}")),
        )
    except OSError as error:
        raise ReleaseError(f"cannot read {path}: {error}") from error
    except json.JSONDecodeError as error:
        raise ReleaseError(f"invalid JSON at {path}:{error.lineno}:{error.colno}: {error.msg}") from error


def read_jsonl(path: Path) -> tuple[list[dict[str, Any]], dict[str, Any] | None]:
    rows: list[dict[str, Any]] = []
    meta: dict[str, Any] | None = None
    try:
        for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if not line.strip():
                continue
            try:
                row = json.loads(
                    line,
                    object_pairs_hook=_no_duplicate_keys,
                    parse_constant=lambda value: (_ for _ in ()).throw(
                        ReleaseError(f"invalid JSON number: {value}")
                    ),
                )
            except (json.JSONDecodeError, ReleaseError) as error:
                raise ReleaseError(f"invalid JSONL at {path}:{number}: {error}") from error
            if not isinstance(row, dict):
                raise ReleaseError(f"JSONL row {number} must be an object")
            if row.get("kind") == "meta":
                if meta is not None:
                    raise ReleaseError(f"duplicate runner metadata row in {path}")
                meta = row
            elif isinstance(row.get("id"), str):
                rows.append(row)
    except OSError as error:
        raise ReleaseError(f"cannot read {path}: {error}") from error
    return rows, meta


def read_records_jsonl(path: Path) -> list[dict[str, Any]]:
    """Read every object row in an evidence JSONL, including metadata rows."""
    records: list[dict[str, Any]] = []
    try:
        for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if not line.strip():
                continue
            try:
                row = json.loads(
                    line,
                    object_pairs_hook=_no_duplicate_keys,
                    parse_constant=lambda value: (_ for _ in ()).throw(
                        ReleaseError(f"invalid JSON number: {value}")
                    ),
                )
            except (json.JSONDecodeError, ReleaseError) as error:
                raise ReleaseError(f"invalid JSONL at {path}:{number}: {error}") from error
            if not isinstance(row, dict):
                raise ReleaseError(f"JSONL row {number} must be an object")
            records.append(row)
    except OSError as error:
        raise ReleaseError(f"cannot read {path}: {error}") from error
    return records


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def tree_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    for item in sorted(path.rglob("*")):
        if not item.is_file() or ".git" in item.parts or "__pycache__" in item.parts or item.suffix == ".pyc":
            continue
        relative = item.relative_to(path).as_posix().encode("utf-8")
        digest.update(len(relative).to_bytes(4, "big"))
        digest.update(relative)
        digest.update(bytes.fromhex(file_sha256(item)))
    return digest.hexdigest()


def is_sha(value: Any) -> bool:
    return isinstance(value, str) and HEX64.fullmatch(value) is not None


def canonical_sha256(value: Any) -> str:
    encoded = (json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"),
                          allow_nan=False) + "\n").encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def positive_or_zero(value: Any) -> bool:
    return isinstance(value, int) and not isinstance(value, bool) and value >= 0


def read_literal_case_list(path: Path) -> list[str]:
    try:
        raw = path.read_text(encoding="utf-8").strip()
    except OSError as error:
        raise ReleaseError(f"cannot read literal case list {path}: {error}") from error
    values = [value.strip() for value in raw.replace("\n", ",").split(",") if value.strip()]
    if len(values) != len(set(values)):
        raise ReleaseError(f"literal case list contains duplicates: {path}")
    return values


def load_suite_inventory(suite_root: Path, *, historical: bool) -> tuple[dict[str, int], dict[str, Any]]:
    suite_root = suite_root.resolve()
    if not suite_root.is_dir():
        raise ReleaseError(f"suite directory is missing: {suite_root}")
    version_path = suite_root / "VERSION"
    if not version_path.is_file():
        raise ReleaseError(f"suite has no VERSION file: {suite_root}")
    version = version_path.read_text(encoding="utf-8").strip()
    expected_marker = "1.2" if historical else "1.3"
    if expected_marker not in version.lower().removeprefix("v"):
        raise ReleaseError(f"expected a {expected_marker} frozen suite, found VERSION={version!r}")

    # Use the frozen runner's own expansion rules so case and instance counts
    # include overlays and generated rows exactly as the acceptance command did.
    for name in list(sys.modules):
        if name == "kogen_conformance" or name.startswith("kogen_conformance."):
            del sys.modules[name]
    sys.path.insert(0, str(suite_root))
    try:
        runner = importlib.import_module("kogen_conformance.runner")
        profiles = list(HISTORICAL_PROFILES) if historical else None
        cases = runner.load_cases(profiles=profiles)
        inventory: dict[str, int] = {}
        for case in cases:
            case_id = case.get("id")
            if not isinstance(case_id, str) or not case_id:
                raise ReleaseError("suite contains a case without a literal id")
            if case_id in inventory:
                raise ReleaseError(f"suite contains duplicate case id {case_id}")
            inventory[case_id] = len(runner.instances(case))
        details = {
            "version": version,
            "suite_revision": EXPECTED_HISTORICAL_SUITE_REVISION if historical else None,
            "suite_root": str(suite_root),
            "input_tree_sha256": tree_sha256(suite_root),
            "profile_selection": list(HISTORICAL_PROFILES) if historical else "runner default profiles",
            "case_count": len(inventory),
            "instance_count": sum(inventory.values()),
        }
        return inventory, details
    finally:
        sys.path.remove(str(suite_root))
        for name in list(sys.modules):
            if name == "kogen_conformance" or name.startswith("kogen_conformance."):
                del sys.modules[name]


def summarize_run(path: Path, expected: dict[str, int], *, label: str,
                  exclude_profiles: set[str] | None = None) -> dict[str, Any]:
    rows, meta = read_jsonl(path)
    excluded_profiles = exclude_profiles or set()
    excluded_rows = [row for row in rows if row.get("profile") in excluded_profiles]
    rows = [row for row in rows if row.get("profile") not in excluded_profiles]
    by_id: dict[str, dict[str, Any]] = {}
    duplicates: list[str] = []
    unknown_ids: list[str] = []
    missing_ids = sorted(set(expected))
    instance_counts = {"total": 0, "passed": 0, "failed": 0, "skipped": 0, "errors": 0}
    statuses: dict[str, int] = {}
    exact_nonpass: list[dict[str, Any]] = []
    unmatched_request_cases: list[str] = []

    for row in rows:
        case_id = row["id"]
        if case_id in by_id:
            duplicates.append(case_id)
            continue
        by_id[case_id] = row
        if case_id not in expected:
            unknown_ids.append(case_id)
        else:
            missing_ids.remove(case_id)
        status = row.get("status")
        if status not in STATUSES:
            raise ReleaseError(f"{label} case {case_id} has unknown status {status!r}")
        statuses[status] = statuses.get(status, 0) + 1
        counts = row.get("instances")
        if not isinstance(counts, dict):
            raise ReleaseError(f"{label} case {case_id} has no instance accounting")
        total, passed = counts.get("total"), counts.get("passed")
        skipped, errors = counts.get("skipped"), counts.get("errors")
        if any(not positive_or_zero(value) for value in (total, passed, skipped, errors)):
            raise ReleaseError(f"{label} case {case_id} has invalid instance counters")
        failed = total - passed - skipped - errors
        if failed < 0:
            raise ReleaseError(f"{label} case {case_id} instance counters exceed total")
        if case_id in expected and total != expected[case_id]:
            raise ReleaseError(
                f"{label} case {case_id} has {total} instances; frozen suite expands to {expected[case_id]}"
            )
        for key, value in (("total", total), ("passed", passed), ("failed", failed),
                           ("skipped", skipped), ("errors", errors)):
            instance_counts[key] += value
        if status != "pass" or failed or skipped or errors:
            exact_nonpass.append({
                "id": case_id,
                "profile": row.get("profile"),
                "status": status,
                "instances": {"total": total, "passed": passed, "failed": failed,
                              "skipped": skipped, "errors": errors},
                "failures": row.get("failures", []),
                "hints": row.get("hints", []),
            })
        diagnostic_text = json.dumps({"failures": row.get("failures", []), "hints": row.get("hints", [])},
                                     ensure_ascii=False).lower()
        if "unmatched request" in diagnostic_text or "unmatched provider request" in diagnostic_text:
            unmatched_request_cases.append(case_id)

    extra_expected = sorted(set(unknown_ids))
    expected_total = sum(expected.values())
    inventory_complete = (
        not duplicates and not missing_ids and not extra_expected
        and len(by_id) == len(expected) and instance_counts["total"] == expected_total
    )
    return {
        "label": label,
        "results_path": str(path.resolve()),
        "results_sha256": file_sha256(path),
        "runner_metadata": meta,
        "excluded_profiles": sorted(excluded_profiles),
        "excluded_case_ids": sorted(row["id"] for row in excluded_rows),
        "expected": {"cases": len(expected), "instances": expected_total},
        "resolved": {"cases": len(by_id), "instances": instance_counts["total"]},
        "inventory_complete": inventory_complete,
        "duplicates": sorted(set(duplicates)),
        "missing_ids": sorted(set(missing_ids)),
        "unmatched_ids": extra_expected,
        "statuses": statuses,
        "instances": instance_counts,
        "historical_assertion_pass_ids": sorted(
            case_id for case_id, row in by_id.items() if row.get("status") == "pass"
        ),
        "exact_nonpass_rows": exact_nonpass,
        "unmatched_request_cases": sorted(set(unmatched_request_cases)),
        "all_cases_pass": inventory_complete and not exact_nonpass,
        "skipped_or_unimplemented": statuses.get("skip", 0) + statuses.get("unimplemented", 0),
    }


def documented_v12_conflicts(run: dict[str, Any] | None = None) -> list[dict[str, Any]]:
    """Known v1.2 assertions superseded by the committed v1.3-draft rule."""
    descriptions = {
        "v1.2-73-ladder-05": (
            "over_strict audit advice demotes A2 before repair",
            "spec/03-build.md §3.8.2 keeps audit advice observational; approved A2 remains required",
        ),
        "v1.2-74-ladder-06": (
            "green policy parks a candidate after audit demotion without repair",
            "audit advice cannot alter eligibility or landing; actual passing verification is required",
        ),
        "v1.2-75-ladder-07": (
            "over_strict audit advice demotes A2 without a citation",
            "audit advice cannot change approved-item eligibility",
        ),
        "v1.2-76-ladder-08": (
            "unsupported infeasible advice is upheld as valid",
            "unknown or unsupported verdicts warn and do not change gate state",
        ),
        "v1.2-78-ladder-10": (
            "citation-bearing demotion uses the historical demotion schema",
            "v1.3 has no Build citation field or demotion effect",
        ),
        "v1.2-79-ladder-11": (
            "pre-repair demotion permits R1 to land without A2 passing",
            "approved A2 must pass actual verification before landing",
        ),
    }
    rows_by_id = {row["id"]: row for row in (run or {}).get("exact_nonpass_rows", [])}
    return [
        {
            "case_id": case_id,
            "historical_assertion": old,
            "v1_3_draft_rule": current,
            "classification": "documented_historical_assertion_conflict",
            "current_run_row": rows_by_id.get(case_id),
            "behavior_reached": None,
        }
        for case_id, (old, current) in descriptions.items()
    ]


def audit_historical_compatibility(history: dict[str, Any], gate_policy_path: Path,
                                  receipt_path: Path | None) -> dict[str, Any]:
    known = {row["case_id"] for row in documented_v12_conflicts()}
    failures = {row["id"]: row for row in history["exact_nonpass_rows"]}
    allowed: dict[str, Any] = {}
    policy_present = gate_policy_path.is_file()
    if policy_present:
        policy = read_json(gate_policy_path)
        package_policy = policy.get("I8-release-comparison-admission", {}) if isinstance(policy, dict) else {}
        entries = package_policy.get("conflicts", {}) if isinstance(package_policy, dict) else {}
        if not isinstance(entries, dict):
            raise ReleaseError("coordinator gate-policy I8 conflicts must be an object")
        unknown = sorted(set(entries) - known)
        if unknown:
            raise ReleaseError("coordinator gate-policy contains undocumented historical conflicts: "
                               + ", ".join(unknown))
        malformed = sorted(case_id for case_id, reason in entries.items()
                           if not isinstance(reason, str) or not reason.strip())
        if malformed:
            raise ReleaseError("coordinator gate-policy conflict reasons must be nonempty strings: "
                               + ", ".join(malformed))
        allowed = entries

    receipt_conflicts: set[str] = set()
    if receipt_path is not None and receipt_path.is_file():
        receipt = read_json(receipt_path)
        values = receipt.get("conflicts", []) if isinstance(receipt, dict) else []
        if not isinstance(values, list):
            raise ReleaseError("I8 gate receipt conflicts must be an array")
        receipt_conflicts = {
            value for value in values if isinstance(value, str) and value in known
        } | {
            value.get("case_id") for value in values
            if isinstance(value, dict) and isinstance(value.get("case_id"), str)
            and value.get("case_id") in known
        }

    accepted_conflict_failures = sorted(
        case_id for case_id in failures
        if case_id in allowed and failures[case_id]["status"] == "fail"
        and case_id in receipt_conflicts
    )
    unresolved = sorted(set(failures) - set(accepted_conflict_failures))
    conflict_ids = sorted(known)
    compatible_passes = sorted(set(history["historical_assertion_pass_ids"]) - known)
    return {
        "status": "pass" if not unresolved else "blocked",
        "compatible_v1_2_pass_ids": compatible_passes,
        "documented_conflict_ids": conflict_ids,
        "coordinator_policy_path": str(gate_policy_path.resolve()),
        "coordinator_policy_present": policy_present,
        "coordinator_accepted_conflict_failures": accepted_conflict_failures,
        "unresolved_nonpass_ids": unresolved,
        "note": "historical v1.2 assertion results; never a v1.3 behavior claim",
    }


def load_module(path: Path, name: str) -> Any:
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise ReleaseError(f"cannot load validator {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def validate_cohort(path: Path) -> dict[str, Any]:
    manifest = read_json(path)
    if not isinstance(manifest, dict):
        raise ReleaseError("shared cohort manifest must be an object")
    if manifest.get("format") != "kogen-shared-v1.3-cohort/v1":
        raise ReleaseError("shared cohort format must be kogen-shared-v1.3-cohort/v1")
    if manifest.get("status") != "frozen" or manifest.get("draft_migration") != "v1.3-draft":
        raise ReleaseError("shared cohort must be frozen and identify the v1.3-draft migration")
    if manifest.get("draft_commit") != DRAFT_COMMIT:
        raise ReleaseError("shared cohort must bind the authoritative e19dd1c draft commit")
    if not isinstance(manifest.get("spec_revision"), str) or not manifest["spec_revision"]:
        raise ReleaseError("shared cohort must bind spec_revision")
    g_slices = manifest.get("g_slices")
    if not isinstance(g_slices, dict) or set(g_slices) != set(G_SLICES):
        raise ReleaseError("shared cohort must contain exactly the eight required G slices")
    for name in G_SLICES:
        entry = g_slices[name]
        if not isinstance(entry, dict) or entry.get("classification") != "G" or entry.get("migrated") is not True:
            raise ReleaseError(f"shared cohort does not identify {name} as migrated G")
        if not positive_or_zero(entry.get("hand_count")) or entry["hand_count"] == 0:
            raise ReleaseError(f"shared cohort has no hand scenarios for {name}")
        if not is_sha(entry.get("tree_sha256")):
            raise ReleaseError(f"shared cohort has no tree digest for {name}")
    d_slices = manifest.get("d_slices")
    if not isinstance(d_slices, dict) or set(d_slices) != set(D_SLICES):
        raise ReleaseError("shared cohort must list all four D slices separately")
    for name, entry in d_slices.items():
        if (not isinstance(entry, dict) or entry.get("classification") != "D"
                or entry.get("migration") != "observational" or entry.get("counts_toward_g") is not False):
            raise ReleaseError(f"D slice {name} must be observational and excluded from G counts")
    if not is_sha(manifest.get("harness_sha256")):
        raise ReleaseError("shared cohort must bind the harness digest")
    return manifest


def validate_replay(path: Path, cohort: dict[str, Any]) -> dict[str, Any]:
    report = read_json(path)
    if not isinstance(report, dict) or report.get("format") != "kogen-production-replay/v1":
        raise ReleaseError("production replay must use kogen-production-replay/v1")
    revisions = [report.get(key) for key in ("go_revision", "cli_revision", "xspec_revision")]
    if not all(isinstance(revision, str) and revision for revision in revisions) or len(set(revisions)) != 1:
        raise ReleaseError("production replay CLI and xspec must bind one same Go revision")
    if report.get("spec_revision") != cohort.get("spec_revision"):
        raise ReleaseError("production replay and frozen cohort spec revisions differ")
    if report.get("cohort_format") != cohort.get("format") or report.get("result") != "pass":
        raise ReleaseError("production replay did not pass against the frozen cohort")
    slices = report.get("g_slices")
    if not isinstance(slices, dict) or set(slices) != set(G_SLICES):
        raise ReleaseError("production replay must report all eight G slices")
    for name in G_SLICES:
        entry = slices[name]
        if not isinstance(entry, dict) or entry.get("spec") != "pass":
            raise ReleaseError(f"production replay spec generation did not pass for {name}")
        hand_count = cohort["g_slices"][name]["hand_count"]
        if entry.get("hand_count") != hand_count:
            raise ReleaseError(f"production replay hand count differs from frozen cohort for {name}")
        seeds = entry.get("seeds")
        if not isinstance(seeds, dict) or set(seeds) != set(SEEDS):
            raise ReleaseError(f"production replay must include seeds 17, 23, and 41 for {name}")
        for seed, result in seeds.items():
            traces = result.get("traces") if isinstance(result, dict) else None
            expected_total = 500 + hand_count
            if (not isinstance(result, dict) or result.get("gen") != "pass"
                    or result.get("conform") != "pass" or not isinstance(traces, dict)
                    or traces.get("matched") != expected_total or traces.get("total") != expected_total):
                raise ReleaseError(f"{name} seed {seed} did not conform on all {expected_total} traces")
    d_reports = report.get("d_slices")
    if not isinstance(d_reports, dict) or set(d_reports) != set(D_SLICES):
        raise ReleaseError("production replay must account for D slices separately")
    for name, entry in d_reports.items():
        if not isinstance(entry, dict) or entry.get("counts_toward_g") is not False:
            raise ReleaseError(f"production replay incorrectly counts D slice {name} toward G")
    return report


def parse_named_path(value: str, *, allowed: set[str]) -> tuple[str, Path]:
    if "=" not in value:
        raise ReleaseError(f"expected NAME=PATH, got {value!r}")
    name, raw_path = value.split("=", 1)
    if name not in allowed or not raw_path:
        raise ReleaseError(f"invalid evidence mapping {value!r}; names must be {sorted(allowed)}")
    return name, Path(raw_path)


def validate_platform_receipt(path: Path, expected_os: str) -> dict[str, Any]:
    receipt = read_json(path)
    if not isinstance(receipt, dict) or receipt.get("schema") != "kogen-platform-check/v1":
        raise ReleaseError(f"{expected_os} check receipt must use kogen-platform-check/v1")
    if receipt.get("os") != expected_os:
        raise ReleaseError(f"platform receipt OS must be {expected_os}")
    if not isinstance(receipt.get("source_revision"), str) or not receipt["source_revision"]:
        raise ReleaseError(f"{expected_os} receipt must bind a source revision")
    commands = receipt.get("commands")
    if not isinstance(commands, dict):
        raise ReleaseError(f"{expected_os} receipt must record command outcomes")
    expected_commands = {
        "make_check": "GIT_CONFIG_GLOBAL=/dev/null make check",
        "make_race": "GIT_CONFIG_GLOBAL=/dev/null make race",
    }
    for name, command in expected_commands.items():
        item = commands.get(name)
        if (not isinstance(item, dict) or item.get("exit_code") != 0
                or item.get("command") != command or not is_sha(item.get("log_sha256"))):
            raise ReleaseError(f"{expected_os} {name} must pass and bind its log")
        log_path = item.get("log_path")
        if not isinstance(log_path, str) or not Path(log_path).is_absolute():
            raise ReleaseError(f"{expected_os} {name} must record an absolute log path")
        if file_sha256(Path(log_path)) != item["log_sha256"]:
            raise ReleaseError(f"{expected_os} {name} log digest does not match its retained file")
    return receipt


def validate_live_cache(replay_path: Path, requests_path: Path) -> dict[str, Any]:
    checker = ROOT / "tools" / "cache-report.py"
    with tempfile.TemporaryDirectory(prefix="kogen-release-cache-") as temporary:
        output = Path(temporary) / "cache-report.json"
        result = subprocess.run(
            [sys.executable, str(checker), "report", "--replay", str(replay_path),
             "--requests", str(requests_path), "--output", str(output)],
            cwd=ROOT,
            env={**os.environ, "PYTHONDONTWRITEBYTECODE": "1"},
            capture_output=True,
            text=True,
            check=False,
        )
        if not output.is_file():
            detail = (result.stderr or result.stdout).strip()
            raise ReleaseError(f"cache telemetry report was not produced: {detail or result.returncode}")
        report = read_json(output)
        if result.returncode != 0 or not isinstance(report, dict):
            raise ReleaseError("cache telemetry did not qualify; inspect its report for exact gaps")
        replay = read_json(replay_path)
        if report.get("replay_manifest_sha256") != canonical_sha256(replay):
            raise ReleaseError("cache report is not bound to the supplied frozen replay manifest")
        designated = report.get("designated_warm")
        if (report.get("schema") != "kogen-cache-report/v1"
                or report.get("qualification") != "qualified"
                or not isinstance(designated, dict) or designated.get("qualified") is not True
                or report.get("live_requests_made_by_tool") != 0):
            raise ReleaseError("designated live warm requests lack complete ≥95% telemetry")
        return report


def add_gate(gates: dict[str, Any], name: str, status: str, details: Any) -> None:
    gates[name] = {"status": status, "details": details}


def audit(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    gates: dict[str, Any] = {}
    blockers: list[str] = []
    report: dict[str, Any] = {
        "schema": "kogen-release-admission-report/v1",
        "target_spec": {"revision": DRAFT_COMMIT, "label": "v1.3-draft"},
        "gates": gates,
        "historical_v1_2": None,
        "documented_v1_2_conflicts": [],
        "shared_v1_3_runs": {},
        "scored_cells_launched": False,
        "scored_cell_admission": False,
    }

    try:
        historical_inventory, historical_suite = load_suite_inventory(args.historical_suite, historical=True)
        if (len(historical_inventory), sum(historical_inventory.values())) != (
                EXPECTED_HISTORICAL_CASES, EXPECTED_HISTORICAL_INSTANCES):
            raise ReleaseError(
                f"frozen v1.2 suite resolves to {len(historical_inventory)} cases / "
                f"{sum(historical_inventory.values())} instances, expected 236 / 570"
            )
        if historical_suite["input_tree_sha256"] != EXPECTED_HISTORICAL_INPUT_SHA256:
            raise ReleaseError("frozen v1.2 suite content digest differs from the pinned historical oracle")
        literal_ids = read_literal_case_list(ROOT / "docs" / "work" / "I8-release-comparison-admission.cases")
        if set(literal_ids) != set(historical_inventory):
            missing = sorted(set(historical_inventory) - set(literal_ids))
            extra = sorted(set(literal_ids) - set(historical_inventory))
            raise ReleaseError("I8 literal case list differs from the frozen suite: "
                               + f"missing={missing}, extra={extra}")
        historical_suite["literal_case_list_count"] = len(literal_ids)
        history = summarize_run(args.historical_results, historical_inventory, label="v1.2",
                                exclude_profiles={"exunit"})
        history_meta = history.get("runner_metadata")
        if not isinstance(history_meta, dict):
            raise ReleaseError("historical results are missing the conformance runner metadata row")
        if history_meta.get("suite") != "kogen-conformance" or "v1.2" not in str(
                history_meta.get("suite_version", "")
        ).lower():
            raise ReleaseError("historical results metadata does not identify the v1.2 conformance runner")
        if history_meta.get("profiles") != list(HISTORICAL_PROFILES):
            raise ReleaseError("historical run did not use the complete required standard profiles and v1.2 overlay")
        if history_meta.get("kogen") != str((ROOT / "bin" / "kogen").resolve()):
            raise ReleaseError("historical results were not produced by this worktree's kogen binary path")
        if history_meta.get("time_scale") != 0.02:
            raise ReleaseError("historical run metadata does not record the required 0.02 time scale")
        report["historical_v1_2"] = {"suite": historical_suite, **history,
                                     "historical_assertions_only": True,
                                     "not_a_v1_3_behavior_claim": True}
        report["documented_v1_2_conflicts"] = documented_v12_conflicts(history)
        if not history["inventory_complete"]:
            raise ReleaseError("historical v1.2 result rows do not exactly match the frozen case/instance inventory")
        add_gate(gates, "historical_v1_2_inventory", "pass", {
            "cases": history["resolved"]["cases"], "instances": history["resolved"]["instances"],
            "case_passes": history["statuses"].get("pass", 0),
            "case_nonpasses": history["resolved"]["cases"] - history["statuses"].get("pass", 0),
            "instance_counts": history["instances"],
        })
        compatibility = audit_historical_compatibility(
            history, args.gate_policy, ROOT / "docs" / "work" / "I8-release-comparison-admission.gate.json"
        )
        report["historical_compatibility"] = compatibility
        if compatibility["status"] == "pass":
            add_gate(gates, "historical_v1_2_compatibility", "pass", compatibility)
        else:
            add_gate(gates, "historical_v1_2_compatibility", "blocked", compatibility)
            blockers.append("compatible v1.2 assertions must pass; integration failures and unmapped exact conflicts remain unresolved")
    except (ReleaseError, OSError, TypeError, ValueError) as error:
        add_gate(gates, "historical_v1_2_inventory", "invalid", str(error))
        blockers.append("complete frozen v1.2 historical inventory is missing or invalid")
        history = None

    comparison_manifest: dict[str, Any] | None = None
    if args.comparison_manifest:
        try:
            validator = load_module(ROOT / "tools" / "compare-manifest.py", "kogen_release_compare_manifest")
            comparison_manifest = validator.read_json(args.comparison_manifest)
            errors = validator.validate_manifest(comparison_manifest, require_frozen=True)
            if errors:
                raise ReleaseError("comparison manifest rejected: " + "; ".join(errors))
            if history is not None:
                historical_ref = comparison_manifest.get("authority", {}).get("historical_v1_2", {})
                if historical_ref.get("suite_revision") != EXPECTED_HISTORICAL_SUITE_REVISION:
                    raise ReleaseError("comparison authority names a different historical v1.2 suite revision")
                if historical_ref.get("input_manifest_sha256") != history["suite"]["input_tree_sha256"]:
                    raise ReleaseError("comparison authority historical input digest differs from frozen suite bytes")
                if historical_ref.get("results_sha256") != history["results_sha256"]:
                    raise ReleaseError("comparison authority historical result digest differs from retained JSONL")
                if (historical_ref.get("results_case_count") != EXPECTED_HISTORICAL_CASES
                        or historical_ref.get("results_instance_count") != EXPECTED_HISTORICAL_INSTANCES):
                    raise ReleaseError("comparison authority historical result denominator is not 236/570")
                ledger = historical_ref.get("conflicts", [])
                ledger_ids = {
                    row.get("case_id", row.get("id")) for row in ledger if isinstance(row, dict)
                } if isinstance(ledger, list) else set()
                required_conflicts = {row["case_id"] for row in report["documented_v1_2_conflicts"]}
                if not required_conflicts <= ledger_ids:
                    raise ReleaseError("comparison authority omits documented exact v1.2 audit conflict IDs")
            add_gate(gates, "comparison_manifest", "pass", {
                "path": str(args.comparison_manifest.resolve()),
                "state": comparison_manifest.get("state"),
                "study_id": comparison_manifest.get("study_id"),
                "arms": [
                    {
                        "id": arm.get("id"),
                        "source_revision": arm.get("source_revision"),
                        "roles": {
                            role: {field: settings.get(field) for field in ("provider", "model", "effort")}
                            for role, settings in arm.get("roles", {}).items()
                            if isinstance(settings, dict)
                        } if isinstance(arm.get("roles"), dict) else {},
                        "recipe": {
                            "name": arm.get("recipe", {}).get("name"),
                            "rungs": [
                                {field: rung.get(field) for field in ("index", "id", "provider", "model", "effort")}
                                for rung in arm.get("recipe", {}).get("rungs", [])
                                if isinstance(rung, dict)
                            ],
                        } if isinstance(arm.get("recipe"), dict) else None,
                    }
                    for arm in comparison_manifest.get("arms", []) if isinstance(arm, dict)
                ],
            })
        except (ReleaseError, OSError, TypeError, ValueError) as error:
            add_gate(gates, "comparison_manifest", "invalid", str(error))
            blockers.append("comparison roles, recipes, P1–P13 and D-CACHE mappings are not validated")
    else:
        add_gate(gates, "comparison_manifest", "missing", "No comparison manifest supplied")
        blockers.append("effective roles, offline measurement digests, and literal P1–P13/D-CACHE mappings")

    if args.shared_v13_manifest and args.shared_v13_suite:
        try:
            suite_inventory, suite_details = load_suite_inventory(args.shared_v13_suite, historical=False)
            shared_sha = file_sha256(args.shared_v13_manifest)
            if comparison_manifest is None:
                raise ReleaseError("a valid comparison manifest is required to bind the frozen oracle digest")
            authority = comparison_manifest.get("authority", {})
            shared_ref = authority.get("shared_v1_3", {}) if isinstance(authority, dict) else {}
            expected_sha = shared_ref.get("full_oracle_manifest_sha256") if isinstance(shared_ref, dict) else None
            if expected_sha != shared_sha:
                raise ReleaseError("frozen shared v1.3 manifest SHA-256 differs from the comparison authority")
            report["shared_v1_3_inventory"] = {
                "manifest_path": str(args.shared_v13_manifest.resolve()),
                "manifest_sha256": shared_sha,
                "suite": suite_details,
                "expected_case_ids": sorted(suite_inventory),
            }
            add_gate(gates, "shared_v1_3_frozen_inventory", "pass", {
                "manifest_sha256": shared_sha, "cases": len(suite_inventory),
                "instances": sum(suite_inventory.values()),
            })
            runs: dict[str, Any] = {}
            for raw in args.v13_run:
                os_name, path = parse_named_path(raw, allowed={"darwin", "linux"})
                if os_name in runs:
                    raise ReleaseError(f"duplicate {os_name} v1.3 result run")
                run = summarize_run(path, suite_inventory, label=os_name)
                meta_os = str((run.get("runner_metadata") or {}).get("platform", "")).lower()
                meta_version = str((run.get("runner_metadata") or {}).get("suite_version", "")).lower()
                if "v1.3" not in meta_version:
                    raise ReleaseError(f"{os_name} runner metadata does not identify the frozen v1.3 suite")
                if os_name == "darwin" and "macos" not in meta_os and "darwin" not in meta_os:
                    raise ReleaseError("darwin run metadata does not identify macOS")
                if os_name == "linux" and "linux" not in meta_os:
                    raise ReleaseError("linux run metadata does not identify Linux")
                runs[os_name] = run
            report["shared_v1_3_runs"] = runs
            if set(runs) != {"darwin", "linux"}:
                raise ReleaseError("both darwin and linux full v1.3 results are required")
            for os_name, run in runs.items():
                if not run["inventory_complete"] or not run["all_cases_pass"]:
                    raise ReleaseError(f"{os_name} full v1.3 run is incomplete or has nonpassing rows")
                if run["skipped_or_unimplemented"] != 0:
                    raise ReleaseError(f"{os_name} v1.3 run has skipped or unimplemented cases")
                if run["unmatched_request_cases"]:
                    raise ReleaseError(f"{os_name} v1.3 run reports unmatched fake requests")
            if comparison_manifest is not None:
                coverage = comparison_manifest.get("coverage", {})
                required_case_ids: set[str] = set()
                for row in coverage.get("p1_p13", []) if isinstance(coverage, dict) else []:
                    if isinstance(row, dict) and isinstance(row.get("case_ids"), list):
                        required_case_ids.update(case_id for case_id in row["case_ids"] if isinstance(case_id, str))
                for row in coverage.get("d_cache_01_06", []) if isinstance(coverage, dict) else []:
                    if isinstance(row, dict) and isinstance(row.get("case_id"), str):
                        required_case_ids.add(row["case_id"])
                if not required_case_ids or not required_case_ids <= set(suite_inventory):
                    missing = sorted(required_case_ids - set(suite_inventory))
                    raise ReleaseError("P1–P13/D-CACHE mappings do not resolve in the frozen shared suite"
                                       + (": " + ", ".join(missing) if missing else ""))
                for os_name, run in runs.items():
                    nonpass = set(run["missing_ids"]) | set(run["unmatched_ids"])
                    nonpass.update(row["id"] for row in run["exact_nonpass_rows"])
                    if required_case_ids & nonpass:
                        raise ReleaseError(f"{os_name} has nonpassing mapped P/draft cases: "
                                           + ", ".join(sorted(required_case_ids & nonpass)))
            add_gate(gates, "shared_v1_3_full_oracle", "pass", {
                "platforms": {name: {"cases": run["resolved"]["cases"],
                                     "instances": run["resolved"]["instances"],
                                     "all_pass": run["all_cases_pass"]}
                              for name, run in runs.items()},
                "zero_skipped_unimplemented_unmatched": True,
            })
        except (ReleaseError, OSError, TypeError, ValueError) as error:
            add_gate(gates, "shared_v1_3_full_oracle", "blocked", str(error))
            blockers.append("both OS full shared v1.3 runs must pass every frozen case and instance")
    else:
        add_gate(gates, "shared_v1_3_full_oracle", "missing", "Coordinator-frozen manifest, suite, or runs are missing")
        blockers.append("coordinator-frozen shared v1.3 oracle and both OS result sets")

    if args.cohort_manifest and args.production_replay_manifest:
        try:
            cohort = validate_cohort(args.cohort_manifest)
            replay = validate_replay(args.production_replay_manifest, cohort)
            if comparison_manifest is None:
                raise ReleaseError("a valid comparison manifest is required to bind production replay")
            authority = comparison_manifest.get("authority", {})
            shared_ref = authority.get("shared_v1_3", {}) if isinstance(authority, dict) else {}
            if shared_ref.get("g_manifest_sha256") != file_sha256(args.cohort_manifest):
                raise ReleaseError("frozen Quint cohort SHA-256 differs from comparison authority")
            replay_ref = authority.get("production_replay", {}) if isinstance(authority, dict) else {}
            if replay_ref.get("manifest_sha256") != file_sha256(args.production_replay_manifest):
                raise ReleaseError("production replay SHA-256 differs from the comparison authority")
            add_gate(gates, "production_replay", "pass", {
                "manifest_sha256": file_sha256(args.production_replay_manifest),
                "go_revision": replay["go_revision"],
                "g_slices": len(replay["g_slices"]),
                "seeds": list(SEEDS),
                "traces_per_seed": 500,
                "steps_per_trace": 25,
                "d_slices_counted_as_g": False,
            })
        except (ReleaseError, OSError, TypeError, ValueError, KeyError) as error:
            add_gate(gates, "production_replay", "invalid", str(error))
            blockers.append("same-revision full-observation G replay and frozen D reconciliation")
    else:
        add_gate(gates, "production_replay", "missing", "Frozen cohort or production replay manifest is missing")
        blockers.append("same-revision production replay manifest for all eight G slices")

    gate_names = {
        "I7": "I7-production-replay-and-draft-reconciliation",
        "71": "71-offline-performance-collector",
        "D1": "D1-observational-audit-regression",
        "D2": "D2-baseline-independence-regression",
        "D3": "D3-durable-crashed-work-preservation",
    }
    closures: dict[str, Any] = {}
    for key, package in gate_names.items():
        receipt_path = ROOT / "docs" / "work" / f"{package}.gate.json"
        evidence_path = ROOT / "docs" / "work" / f"{package}.evidence.md"
        try:
            receipt = read_json(receipt_path)
            accepted = receipt.get("accepted_gate") if isinstance(receipt, dict) else None
            evidence_sha = file_sha256(evidence_path) if evidence_path.is_file() else None
            closures[key] = {"accepted_gate": accepted, "evidence_sha256": evidence_sha,
                             "receipt_path": str(receipt_path)}
            if comparison_manifest is not None:
                authority = comparison_manifest.get("authority", {})
                refs = authority.get("closure_receipts", {}) if isinstance(authority, dict) else {}
                closure_ref = refs.get(key, {}) if isinstance(refs, dict) else {}
                if (not isinstance(closure_ref, dict) or closure_ref.get("accepted_gate") != accepted
                        or closure_ref.get("evidence_sha256") != evidence_sha):
                    blockers.append(f"comparison authority does not bind current {package} evidence")
            if accepted != "behaviour" or not evidence_sha:
                blockers.append(f"{package} behavior closure receipt")
        except (ReleaseError, OSError, TypeError, ValueError) as error:
            closures[key] = {"error": str(error)}
            blockers.append(f"{package} behavior closure receipt")
    report["closure_receipts"] = closures
    if all(item.get("accepted_gate") == "behaviour" and item.get("evidence_sha256") for item in closures.values()):
        add_gate(gates, "I7_71_D1_D2_D3_closure", "pass", closures)
    else:
        add_gate(gates, "I7_71_D1_D2_D3_closure", "blocked", closures)

    platform_results: dict[str, Any] = {}
    try:
        for raw in args.platform_receipt:
            os_name, path = parse_named_path(raw, allowed={"darwin", "linux"})
            if os_name in platform_results:
                raise ReleaseError(f"duplicate {os_name} platform receipt")
            platform_results[os_name] = validate_platform_receipt(path, os_name)
        if set(platform_results) != {"darwin", "linux"}:
            raise ReleaseError("both darwin and linux check/race receipts are required")
        revisions = {receipt["source_revision"] for receipt in platform_results.values()}
        if len(revisions) != 1:
            raise ReleaseError("macOS and Linux check/race receipts must bind the same source revision")
        if comparison_manifest is not None:
            go_arms = [arm for arm in comparison_manifest.get("arms", [])
                       if isinstance(arm, dict) and arm.get("id") == "go"]
            if len(go_arms) != 1 or go_arms[0].get("source_revision") not in revisions:
                raise ReleaseError("platform check/race revision differs from the comparison Go arm")
        add_gate(gates, "hermetic_check_and_race", "pass", {
            "platforms": sorted(platform_results), "source_revision": next(iter(revisions)),
        })
    except (ReleaseError, OSError, TypeError, ValueError, KeyError) as error:
        add_gate(gates, "hermetic_check_and_race", "blocked", str(error))
        blockers.append("same-revision hermetic check and race evidence on macOS and Linux")

    if args.performance_manifest and args.performance_results:
        try:
            if comparison_manifest is None:
                raise ReleaseError("a valid comparison manifest is required to bind offline measurements")
            authority = comparison_manifest.get("authority", {})
            performance_ref = authority.get("offline_performance", {}) if isinstance(authority, dict) else {}
            if performance_ref.get("manifest_sha256") != file_sha256(args.performance_manifest):
                raise ReleaseError("offline performance manifest SHA-256 differs from comparison authority")
            if performance_ref.get("measurements_sha256") != file_sha256(args.performance_results):
                raise ReleaseError("offline measurements SHA-256 differs from comparison authority")
            records = read_records_jsonl(args.performance_results)
            manifest_rows = [row for row in records if row.get("record_type") == "manifest"]
            measurements = [row for row in records if row.get("record_type") != "manifest"]
            if len(manifest_rows) != 1 or manifest_rows[0].get("schema_version") != 1:
                raise ReleaseError("offline measurement JSONL must contain one schema-v1 manifest row")
            if any(row.get("schema_version") != 1 for row in measurements):
                raise ReleaseError("offline measurement JSONL contains a non-v1 measurement row")
            add_gate(gates, "offline_timing", "pass", {
                "manifest_sha256": file_sha256(args.performance_manifest),
                "measurements_sha256": file_sha256(args.performance_results),
                "measurement_rows": len(measurements),
                "host": {
                    field: manifest_rows[0].get("host", {}).get(field)
                    for field in ("os", "os_version", "os_release", "architecture", "logical_cpus", "memory_bytes")
                },
                "languages": [
                    {field: row.get(field) for field in ("language", "revision", "toolchain_pin")}
                    for row in manifest_rows[0].get("languages", []) if isinstance(row, dict)
                ],
                "measurements": [
                    {field: row.get(field) for field in (
                        "language", "phase", "cache_state", "status", "exit_code", "timed_out",
                        "wall_ms", "cpu_user_ms", "cpu_system_ms", "peak_rss_bytes",
                        "binary_bytes", "binary_sha256", "provider_mode", "provider_requests",
                    )}
                    for row in measurements
                ],
                "statuses": {status: sum(row.get("status") == status for row in measurements)
                             for status in sorted({str(row.get("status")) for row in measurements})},
                "interpretation": "descriptive offline evidence; preserve unavailable and failed rows",
            })
        except (ReleaseError, OSError, TypeError, ValueError) as error:
            add_gate(gates, "offline_timing", "invalid", str(error))
            blockers.append("offline timing measurements bound to the comparison authority")
    else:
        add_gate(gates, "offline_timing", "missing", "Package 71 manifest or measurements are missing")
        blockers.append("offline timing measurements and pinned host/tool manifests")

    if args.build_effort:
        try:
            if comparison_manifest is None:
                raise ReleaseError("a valid comparison manifest is required to resolve build effort")
            compare_results = load_module(ROOT / "tools" / "compare-results.py", "kogen_release_compare_results")
            effort = compare_results.load_build_effort(args.build_effort, comparison_manifest)
            if effort.get("status") != "complete":
                raise ReleaseError("build effort is incomplete: " + "; ".join(effort.get("errors", [])))
            add_gate(gates, "build_effort", "pass", {
                "input_sha256": effort["input_sha256"],
                "arms": [record.get("arm") for record in effort.get("records", [])],
                "records": effort.get("records", []),
            })
        except (ReleaseError, OSError, TypeError, ValueError, KeyError) as error:
            add_gate(gates, "build_effort", "invalid", str(error))
            blockers.append("measured worker/coordinator effort and parity milestones for all comparison arms")
    else:
        add_gate(gates, "build_effort", "missing", "No measured kogen-build-effort/v1 records supplied")
        blockers.append("measured build effort by arm; planning estimates do not count")

    if args.live_cache_replay and args.live_cache_requests:
        try:
            cache_report = validate_live_cache(args.live_cache_replay, args.live_cache_requests)
            if comparison_manifest is None:
                raise ReleaseError("a valid comparison manifest is required to bind the live replay")
            cache_ref = comparison_manifest.get("cache_replay", {})
            if cache_ref.get("replay_manifest_sha256") != cache_report.get("replay_manifest_sha256"):
                raise ReleaseError("live replay digest differs from the comparison manifest")
            add_gate(gates, "live_cache_replay", "pass", {
                "replay_manifest_sha256": file_sha256(args.live_cache_replay),
                "request_rows": cache_report["request_count"],
                "designated_warm": cache_report.get("designated_warm"),
                "telemetry_completeness": "complete",
            })
        except (ReleaseError, OSError, TypeError, ValueError) as error:
            add_gate(gates, "live_cache_replay", "invalid", str(error))
            blockers.append("frozen feasible live cache replay with complete telemetry and ≥95% per warm request")
    else:
        add_gate(gates, "live_cache_replay", "not_run", "No live provider replay was run by this package")
        blockers.append("separately authorized feasible live cache replay before scored admission")

    report["blockers"] = sorted(set(blockers))
    report["release_admitted"] = not report["blockers"]
    report["scored_cell_admission"] = report["release_admitted"]
    report["result"] = "admitted" if report["release_admitted"] else "blocked"
    return report, 0 if report["release_admitted"] else 2


def write_report(path: Path, report: dict[str, Any]) -> None:
    encoded = (json.dumps(report, ensure_ascii=False, sort_keys=True, indent=2, allow_nan=False) + "\n").encode()
    destination = Path(os.path.abspath(path))
    if destination.name in {"", ".", ".."}:
        raise ReleaseError(f"invalid release report path: {path}")
    directory_flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
    directory_fd = os.open(destination.anchor, directory_flags)
    try:
        for component in destination.parent.parts[1:]:
            try:
                next_fd = os.open(component, directory_flags, dir_fd=directory_fd)
            except FileNotFoundError:
                os.mkdir(component, 0o700, dir_fd=directory_fd)
                next_fd = os.open(component, directory_flags, dir_fd=directory_fd)
            os.close(directory_fd)
            directory_fd = next_fd
        leaf = destination.name
        temp_name = f".{leaf}.{os.getpid()}.{os.urandom(8).hex()}.tmp"
        try:
            temp_fd = os.open(temp_name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0),
                              0o600, dir_fd=directory_fd)
            try:
                with os.fdopen(temp_fd, "wb", closefd=False) as stream:
                    stream.write(encoded)
                    stream.flush()
                    os.fsync(stream.fileno())
            finally:
                os.close(temp_fd)
            os.link(temp_name, leaf, src_dir_fd=directory_fd, dst_dir_fd=directory_fd, follow_symlinks=False)
        except FileExistsError as error:
            raise ReleaseError(f"refusing to overwrite release report: {destination}") from error
        finally:
            try:
                os.unlink(temp_name, dir_fd=directory_fd)
            except FileNotFoundError:
                pass
        os.fsync(directory_fd)
    finally:
        os.close(directory_fd)


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    audit_parser = sub.add_parser("audit", help="audit supplied release evidence without launching cells")
    audit_parser.add_argument("--historical-suite", type=Path,
                              default=Path.home() / "cx/kgo/inputs/conformance-v1.2")
    audit_parser.add_argument("--historical-results", type=Path, required=True)
    audit_parser.add_argument("--gate-policy", type=Path,
                              default=Path.home() / "cx/kgo/gate-policy.json")
    audit_parser.add_argument("--shared-v13-suite", type=Path)
    audit_parser.add_argument("--shared-v13-manifest", type=Path)
    audit_parser.add_argument("--v13-run", action="append", default=[], metavar="darwin|linux=JSONL")
    audit_parser.add_argument("--cohort-manifest", type=Path)
    audit_parser.add_argument("--production-replay-manifest", type=Path)
    audit_parser.add_argument("--platform-receipt", action="append", default=[], metavar="darwin|linux=JSON")
    audit_parser.add_argument("--comparison-manifest", type=Path)
    audit_parser.add_argument("--performance-manifest", type=Path)
    audit_parser.add_argument("--performance-results", type=Path)
    audit_parser.add_argument("--build-effort", type=Path)
    audit_parser.add_argument("--live-cache-replay", type=Path)
    audit_parser.add_argument("--live-cache-requests", type=Path)
    audit_parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        report, exit_code = audit(args)
        write_report(args.output, report)
        print(f"release admission: {report['result']} ({args.output.resolve()})")
        return exit_code
    except (ReleaseError, OSError, TypeError, ValueError) as error:
        print(f"release-check: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
