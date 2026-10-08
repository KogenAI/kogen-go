#!/usr/bin/env python3
"""Freeze and validate a predeclared, content-addressed Kogen comparison."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import tempfile
from typing import Any


SCHEMA = "kogen-comparison-manifest/v1"
HEX64 = re.compile(r"^[0-9a-f]{64}$")
LANGUAGES = {"go", "rust", "bun"}
LANES = {"build", "end_to_end"}
ROLES = {"shaper", "fallback_shaper", "planner", "builder", "auditor", "context", "reviewer"}
FAILURE_STATES = {
    "landed", "parked", "failed", "stopped", "interrupted", "infrastructure", "not_started"
}
CAPS = {
    "shape_logical_turns", "shape_validation_passes", "shape_style_repairs",
    "build_wall_ms", "max_rungs", "generation_tokens", "tool_result_tokens",
    "request_attempts", "request_idle_ms", "request_total_ms", "verification_ms",
}


class ManifestError(ValueError):
    pass


def _object_no_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ManifestError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=_object_no_duplicates,
                          parse_constant=lambda value: (_ for _ in ()).throw(ManifestError(f"invalid number: {value}")))
    except OSError as error:
        raise ManifestError(f"cannot read {path}: {error}") from error
    except json.JSONDecodeError as error:
        raise ManifestError(f"invalid JSON in {path}:{error.lineno}:{error.colno}: {error.msg}") from error


def canonical_bytes(value: Any) -> bytes:
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False) + "\n").encode("utf-8")


def digest(value: Any) -> str:
    return hashlib.sha256(canonical_bytes(value)).hexdigest()


def nonempty(value: Any) -> bool:
    return isinstance(value, str) and bool(value.strip())


def sha(value: Any) -> bool:
    return isinstance(value, str) and HEX64.fullmatch(value) is not None


def model_matches_provider(model: Any, provider: Any) -> bool:
    if not isinstance(model, str) or not isinstance(provider, str):
        return False
    if model.startswith("gpt-"):
        return provider == "chatgpt"
    if model.startswith("grok-"):
        return provider == "grok"
    return True


def need(condition: bool, message: str, errors: list[str]) -> None:
    if not condition:
        errors.append(message)


def validate_manifest(manifest: Any, require_frozen: bool = False) -> list[str]:
    errors: list[str] = []
    need(isinstance(manifest, dict), "manifest must be a JSON object", errors)
    if not isinstance(manifest, dict):
        return errors

    need(manifest.get("schema") == SCHEMA, f"schema must be {SCHEMA}", errors)
    need(nonempty(manifest.get("study_id")), "study_id is required", errors)
    state = manifest.get("state")
    need(state in {"draft", "frozen"}, "state must be draft or frozen", errors)
    if require_frozen:
        need(state == "frozen", "scored-cell admission requires state=frozen", errors)

    authority = manifest.get("authority")
    need(isinstance(authority, dict), "authority must bind spec, v1.2, v1.3, and replay inputs", errors)
    if isinstance(authority, dict):
        spec = authority.get("target_spec")
        need(isinstance(spec, dict), "authority.target_spec is required", errors)
        if isinstance(spec, dict):
            need(nonempty(spec.get("revision")), "target_spec.revision is required", errors)
            need(sha(spec.get("content_manifest_sha256")), "target_spec.content_manifest_sha256 must be SHA-256", errors)
        historical = authority.get("historical_v1_2")
        need(isinstance(historical, dict), "authority.historical_v1_2 is required", errors)
        if isinstance(historical, dict):
            need(historical.get("case_count") == 236, "historical_v1_2.case_count must be the frozen 236-case denominator", errors)
            need(historical.get("instance_count") == 570, "historical_v1_2.instance_count must be the frozen 570-instance denominator", errors)
            need(nonempty(historical.get("suite_revision")), "historical_v1_2.suite_revision is required", errors)
            need(sha(historical.get("input_manifest_sha256")), "historical_v1_2.input_manifest_sha256 must be SHA-256", errors)
            need(sha(historical.get("results_sha256")), "historical_v1_2.results_sha256 must bind complete preserved results", errors)
            need(historical.get("results_case_count") == 236, "historical_v1_2.results_case_count must be 236", errors)
            need(historical.get("results_instance_count") == 570, "historical_v1_2.results_instance_count must be 570", errors)
            need(isinstance(historical.get("conflicts"), list), "historical_v1_2.conflicts must preserve the exact conflict ledger (possibly empty)", errors)
        v13 = authority.get("shared_v1_3")
        need(isinstance(v13, dict), "authority.shared_v1_3 must identify the frozen full oracle and G manifest", errors)
        if isinstance(v13, dict):
            need(v13.get("frozen") is True, "shared_v1_3.frozen must be true", errors)
            need(sha(v13.get("full_oracle_manifest_sha256")), "shared_v1_3.full_oracle_manifest_sha256 is required", errors)
            need(sha(v13.get("g_manifest_sha256")), "shared_v1_3.g_manifest_sha256 is required", errors)
            need(v13.get("all_g_slices") == 8, "shared_v1_3.all_g_slices must identify all eight G slices", errors)
        replay = authority.get("production_replay")
        need(isinstance(replay, dict), "authority.production_replay must bind the same-revision production replay manifest", errors)
        if isinstance(replay, dict):
            need(replay.get("complete") is True, "production_replay.complete must be true before cell admission", errors)
            need(sha(replay.get("manifest_sha256")), "production_replay.manifest_sha256 is required", errors)
        closure = authority.get("closure_receipts")
        need(isinstance(closure, dict), "authority.closure_receipts must bind accepted I7, 71, D1, D2, and D3 evidence", errors)
        if isinstance(closure, dict):
            for name in ("I7", "71", "D1", "D2", "D3"):
                receipt = closure.get(name)
                need(isinstance(receipt, dict), f"authority.closure_receipts.{name} is required", errors)
                if isinstance(receipt, dict):
                    need(receipt.get("accepted_gate") == "behaviour", f"closure {name} must be behavior accepted before scored cells", errors)
                    need(sha(receipt.get("evidence_sha256")), f"closure {name} must bind its evidence digest", errors)
        performance = authority.get("offline_performance")
        need(isinstance(performance, dict), "authority.offline_performance must bind the package-71 manifest and measurements", errors)
        if isinstance(performance, dict):
            need(sha(performance.get("manifest_sha256")) and sha(performance.get("measurements_sha256")),
                 "offline_performance manifest and measurement SHA-256 digests are required", errors)

    design = manifest.get("design")
    need(isinstance(design, dict), "design is required", errors)
    if isinstance(design, dict):
        lanes = design.get("lanes")
        need(isinstance(lanes, list) and set(lanes) == LANES, "design.lanes must freeze build and end_to_end", errors)
        repetitions = design.get("repetitions")
        need(isinstance(repetitions, int) and repetitions > 0, "design.repetitions must be a positive integer", errors)
        need(isinstance(design.get("failure_taxonomy"), list) and set(design.get("failure_taxonomy", [])) >= FAILURE_STATES,
             "design.failure_taxonomy must include every terminal and not-started cell state", errors)
        need(nonempty(design.get("stopping_rule")), "design.stopping_rule is required", errors)
        spend = design.get("spend_cap")
        need(isinstance(spend, dict) and isinstance(spend.get("amount"), (int, float))
             and not isinstance(spend.get("amount"), bool) and spend.get("amount") >= 0
             and nonempty(spend.get("currency")),
             "design.spend_cap must freeze amount and currency before launch", errors)
        need(nonempty(design.get("pairing_protocol")), "design.pairing_protocol must freeze host-paired/interleaved order", errors)
        need(nonempty(design.get("denominator_rule")) and "failed" in str(design.get("denominator_rule")).lower(),
             "design.denominator_rule must state that failed/unstarted cells remain in the denominator", errors)
        effort = design.get("build_effort_protocol")
        need(isinstance(effort, dict), "design.build_effort_protocol must freeze worker/coordinator effort accounting", errors)
        if isinstance(effort, dict):
            for field in ("worker_time_source", "coordinator_time_source", "model_usage_source", "rework_definition", "parity_milestones"):
                need(nonempty(effort.get(field)), f"design.build_effort_protocol.{field} is required", errors)

    tasks = manifest.get("tasks")
    need(isinstance(tasks, list) and len(tasks) > 0, "tasks must contain the exact held-out task set", errors)
    task_ids: set[str] = set()
    if isinstance(tasks, list):
        for index, task in enumerate(tasks):
            prefix = f"tasks[{index}]"
            if not isinstance(task, dict):
                errors.append(f"{prefix} must be an object")
                continue
            task_id = task.get("id")
            need(nonempty(task_id), f"{prefix}.id is required", errors)
            if nonempty(task_id):
                need(task_id not in task_ids, f"duplicate task id: {task_id}", errors)
                task_ids.add(task_id)
            need(isinstance(task.get("lanes"), list) and bool(task.get("lanes")) and set(task.get("lanes", [])) <= LANES,
                 f"{prefix}.lanes must select one or both frozen lanes", errors)
            for field in ("task_tree_sha256", "request_sha256", "acceptance_sha256"):
                need(sha(task.get(field)), f"{prefix}.{field} must bind the exact task/request/acceptance bytes", errors)
            grader = task.get("grader")
            need(isinstance(grader, dict), f"{prefix}.grader must identify the independent blinded grader", errors)
            if isinstance(grader, dict):
                need(nonempty(grader.get("id")) and nonempty(grader.get("version")), f"{prefix}.grader id/version are required", errors)
                need(sha(grader.get("rubric_sha256")), f"{prefix}.grader.rubric_sha256 must bind the rubric", errors)
                need(grader.get("independent") is True, f"{prefix}.grader.independent must be true", errors)
                need(grader.get("blinded") is True, f"{prefix}.grader.blinded must be true", errors)
                need(nonempty(grader.get("result_schema")), f"{prefix}.grader.result_schema is required", errors)
            need(nonempty(task.get("domain")), f"{prefix}.domain is required", errors)
            need(nonempty(task.get("held_out_split")), f"{prefix}.held_out_split must identify the held-out partition", errors)

    coverage = manifest.get("coverage")
    need(isinstance(coverage, dict), "coverage must map P1–P13 and D-CACHE-01–06 to literal frozen case IDs", errors)
    if isinstance(coverage, dict):
        p_cases = coverage.get("p1_p13")
        need(isinstance(p_cases, list) and len(p_cases) == 13,
             "coverage.p1_p13 must contain one evidence mapping for each P1–P13 obligation", errors)
        if isinstance(p_cases, list):
            expected_p = {f"P{number}" for number in range(1, 14)}
            got_p = {row.get("requirement") for row in p_cases if isinstance(row, dict)}
            need(got_p == expected_p, "coverage.p1_p13 must name P1 through P13 exactly once", errors)
            for row in p_cases:
                if not isinstance(row, dict):
                    continue
                need(isinstance(row.get("case_ids"), list) and bool(row.get("case_ids")) and
                     all(nonempty(case_id) and "planned" not in case_id.lower() for case_id in row.get("case_ids", [])),
                     f"{row.get('requirement')} must use literal frozen case IDs", errors)
                need(sha(row.get("evidence_sha256")), f"{row.get('requirement')} must bind compatible evidence", errors)
        d_cache = coverage.get("d_cache_01_06")
        need(isinstance(d_cache, list) and len(d_cache) == 6,
             "coverage.d_cache_01_06 must map all six planned cache fixtures to real IDs", errors)
        if isinstance(d_cache, list):
            got_d = {row.get("fixture") for row in d_cache if isinstance(row, dict)}
            expected_d = {f"D-CACHE-0{number}" for number in range(1, 7)}
            need(got_d == expected_d, "coverage.d_cache_01_06 must name D-CACHE-01 through D-CACHE-06 exactly once", errors)
            for row in d_cache:
                if isinstance(row, dict):
                    need(nonempty(row.get("case_id")) and "planned" not in str(row.get("case_id", "")).lower(),
                         f"{row.get('fixture')} must bind a literal shared case ID", errors)
                    need(sha(row.get("evidence_sha256")), f"{row.get('fixture')} must bind compatible evidence", errors)

    arms = manifest.get("arms")
    need(isinstance(arms, list) and {a.get("id") for a in arms if isinstance(a, dict)} == LANGUAGES,
         "arms must define exactly one go, rust, and bun arm", errors)
    arm_ids: set[str] = set()
    if isinstance(arms, list):
        for index, arm in enumerate(arms):
            prefix = f"arms[{index}]"
            if not isinstance(arm, dict):
                errors.append(f"{prefix} must be an object")
                continue
            arm_id = arm.get("id")
            need(arm_id in LANGUAGES, f"{prefix}.id must be go, rust, or bun", errors)
            if nonempty(arm_id):
                need(arm_id not in arm_ids, f"duplicate arm id: {arm_id}", errors)
                arm_ids.add(arm_id)
            for field in ("source_revision", "toolchain_pin", "build_command", "development_history_context", "binary_sha256", "sandbox_policy_sha256"):
                value = arm.get(field)
                if field.endswith("sha256"):
                    need(sha(value), f"{prefix}.{field} must be SHA-256", errors)
                else:
                    need(nonempty(value), f"{prefix}.{field} is required", errors)
            need(isinstance(arm.get("binary_bytes"), int) and arm.get("binary_bytes", 0) > 0,
                 f"{prefix}.binary_bytes must bind the measured executable", errors)
            host = arm.get("host")
            need(isinstance(host, dict), f"{prefix}.host must freeze OS, version, architecture, CPU, and memory", errors)
            if isinstance(host, dict):
                for field in ("id", "os", "os_release", "architecture", "cpu_model", "runtime_manifest_sha256"):
                    value = host.get(field)
                    need(sha(value) if field.endswith("sha256") else nonempty(value), f"{prefix}.host.{field} is required", errors)
                need(isinstance(host.get("logical_cpus"), int) and host.get("logical_cpus", 0) > 0,
                     f"{prefix}.host.logical_cpus must be positive", errors)
                need(isinstance(host.get("memory_bytes"), int) and host.get("memory_bytes", 0) > 0,
                     f"{prefix}.host.memory_bytes must be positive", errors)
            provider = arm.get("provider")
            need(isinstance(provider, dict), f"{prefix}.provider must freeze provider mode and endpoint", errors)
            if isinstance(provider, dict):
                for field in ("name", "mode", "endpoint_host", "endpoint_path", "account_namespace_id"):
                    need(nonempty(provider.get(field)), f"{prefix}.provider.{field} is required", errors)
            role_manifest = arm.get("roles")
            need(isinstance(role_manifest, dict) and set(role_manifest) == ROLES,
                 f"{prefix}.roles must resolve all seven roles including fallback_shaper, context and reviewer", errors)
            if isinstance(role_manifest, dict):
                for role in ROLES:
                    setting = role_manifest.get(role)
                    need(isinstance(setting, dict), f"{prefix}.roles.{role} is required", errors)
                    if isinstance(setting, dict):
                        for field in ("provider", "model", "effort"):
                            need(nonempty(setting.get(field)), f"{prefix}.roles.{role}.{field} is required", errors)
                        if nonempty(setting.get("provider")) and nonempty(setting.get("model")):
                            need(model_matches_provider(setting.get("model"), setting.get("provider")),
                                 f"{prefix}.roles.{role} has a cross-provider model override", errors)
                shaper = role_manifest.get("shaper")
                fallback = role_manifest.get("fallback_shaper")
                need(isinstance(shaper, dict) and isinstance(fallback, dict) and
                     all(shaper.get(k) == fallback.get(k) for k in ("provider", "model", "effort")),
                     f"{prefix}.roles.fallback_shaper must alias the effective shaper provider/model/effort", errors)
                if isinstance(provider, dict):
                    for role, setting in role_manifest.items():
                        if isinstance(setting, dict) and nonempty(setting.get("provider")):
                            need(setting.get("provider") == provider.get("name"),
                                 f"{prefix}.roles.{role}.provider must match the arm provider", errors)
            recipe = arm.get("recipe")
            need(isinstance(recipe, dict) and nonempty(recipe.get("name")), f"{prefix}.recipe must freeze the exact recipe", errors)
            if isinstance(recipe, dict):
                rungs = recipe.get("rungs")
                need(isinstance(rungs, list) and bool(rungs), f"{prefix}.recipe.rungs must freeze every rung/model escalation", errors)
                if isinstance(rungs, list):
                    indices = [r.get("index") for r in rungs if isinstance(r, dict)]
                    need(indices == list(range(1, len(rungs) + 1)), f"{prefix}.recipe.rungs must have contiguous ordered indices", errors)
                    for rung_index, rung in enumerate(rungs):
                        if not isinstance(rung, dict):
                            errors.append(f"{prefix}.recipe.rungs[{rung_index}] must be an object")
                            continue
                        for field in ("id", "provider", "model", "effort", "input", "tools"):
                            need(nonempty(rung.get(field)), f"{prefix}.recipe.rungs[{rung_index}].{field} is required", errors)
                        if nonempty(rung.get("provider")) and nonempty(rung.get("model")):
                            need(model_matches_provider(rung.get("model"), rung.get("provider")),
                                 f"{prefix}.recipe.rungs[{rung_index}] has a cross-provider model", errors)
                        if isinstance(provider, dict) and nonempty(rung.get("provider")):
                            need(rung.get("provider") == provider.get("name"),
                                 f"{prefix}.recipe.rungs[{rung_index}].provider must match the arm provider", errors)
            caps = arm.get("caps")
            need(isinstance(caps, dict) and CAPS <= set(caps), f"{prefix}.caps must freeze all Shape/Build/provider/verification caps: {sorted(CAPS)}", errors)
            if isinstance(caps, dict):
                for field in CAPS:
                    value = caps.get(field)
                    optional_cap = field == "generation_tokens" and value is None
                    need(optional_cap or (isinstance(value, int) and not isinstance(value, bool) and value >= 0),
                         f"{prefix}.caps.{field} must be a nonnegative integer (generation_tokens may be null when unset)", errors)

    if isinstance(arms, list):
        host_ids = {arm.get("host", {}).get("id") for arm in arms
                    if isinstance(arm, dict) and isinstance(arm.get("host"), dict)}
        need(len(host_ids) == 1 and None not in host_ids,
             "all three language arms must be paired on the same frozen host", errors)

    rates = manifest.get("rate_snapshots")
    need(isinstance(rates, list) and bool(rates), "rate_snapshots must freeze verified rates or explicitly identify subscription-only usage", errors)
    rate_keys: set[tuple[str, str, str]] = set()
    if isinstance(rates, list):
        for index, rate in enumerate(rates):
            prefix = f"rate_snapshots[{index}]"
            if not isinstance(rate, dict):
                errors.append(f"{prefix} must be an object")
                continue
            need(nonempty(rate.get("provider")) and nonempty(rate.get("model")) and nonempty(rate.get("endpoint_host")),
                 f"{prefix} must bind provider/model/endpoint", errors)
            rate_key = (str(rate.get("provider")), str(rate.get("model")), str(rate.get("endpoint_host")))
            need(rate_key not in rate_keys, f"duplicate rate snapshot identity {rate_key}", errors)
            rate_keys.add(rate_key)
            basis = rate.get("basis")
            need(basis in {"verified_rate_card", "subscription_measured_usage"}, f"{prefix}.basis is invalid", errors)
            need(nonempty(rate.get("captured_at")) and nonempty(rate.get("source")), f"{prefix} must freeze capture time and source", errors)
            if basis == "verified_rate_card":
                need(sha(rate.get("source_sha256")), f"{prefix}.source_sha256 must bind the rate source", errors)
                prices = rate.get("per_million")
                need(isinstance(prices, dict) and all(isinstance(prices.get(k), (int, float)) and prices.get(k) >= 0
                     for k in ("uncached_input", "cached_input", "cache_write", "output", "reasoning")),
                     f"{prefix}.per_million must price every token class", errors)
                need(nonempty(rate.get("currency")), f"{prefix}.currency is required", errors)
            else:
                need(rate.get("cost_claim") == "measured_usage_only", f"{prefix} subscription arm must not invent per-cell invoice cost", errors)

    if isinstance(arms, list) and isinstance(rates, list):
        for arm in arms:
            if not isinstance(arm, dict) or not isinstance(arm.get("provider"), dict):
                continue
            endpoint_host = arm["provider"].get("endpoint_host")
            settings: list[dict[str, Any]] = []
            if isinstance(arm.get("roles"), dict):
                settings.extend(value for value in arm["roles"].values() if isinstance(value, dict))
            recipe = arm.get("recipe")
            if isinstance(recipe, dict) and isinstance(recipe.get("rungs"), list):
                settings.extend(value for value in recipe["rungs"] if isinstance(value, dict))
            for setting in settings:
                identity = (str(setting.get("provider")), str(setting.get("model")), str(endpoint_host))
                need(identity in rate_keys, f"missing frozen rate/usage basis for provider/model/endpoint {identity}", errors)

    cache = manifest.get("cache_replay")
    need(isinstance(cache, dict), "cache_replay must separately freeze a feasible replay proposal", errors)
    if isinstance(cache, dict):
        need(cache.get("launch_authorized") is False, "comparison preparation must not launch a scored/live cache cell", errors)
        need(nonempty(cache.get("replay_manifest_sha256")) and sha(cache.get("replay_manifest_sha256")),
             "cache_replay.replay_manifest_sha256 must bind its frozen request definition", errors)
        need(cache.get("required_eligible_ratio") == 0.95 and cache.get("required_observed_ratio") == 0.95,
             "cache_replay must keep both 95% thresholds", errors)

    if isinstance(design, dict) and isinstance(tasks, list) and isinstance(arms, list):
        covered_lanes = {lane for task in tasks if isinstance(task, dict)
                         for lane in task.get("lanes", []) if isinstance(task.get("lanes"), list)}
        need(covered_lanes == LANES, "the held-out task set must cover both frozen lanes", errors)
        repetitions = design.get("repetitions")
        if isinstance(repetitions, int) and repetitions > 0:
            planned = sum(len(task.get("lanes", [])) for task in tasks if isinstance(task, dict)
                          and isinstance(task.get("lanes"), list)) * repetitions * len(arms)
            need(design.get("planned_cell_count") == planned,
                 f"design.planned_cell_count must equal the exact task × lane × repetition × arm denominator ({planned})", errors)

    if state == "frozen":
        recorded = manifest.get("manifest_sha256")
        unhashed = dict(manifest)
        unhashed.pop("manifest_sha256", None)
        need(sha(recorded) and recorded == digest(unhashed), "manifest_sha256 does not match canonical manifest bytes", errors)
    return errors


def write_new(path: Path, data: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.exists() or path.is_symlink():
        raise ManifestError(f"refusing to replace existing freeze artifact: {path}")
    temporary: Path | None = None
    try:
        fd, temp_name = tempfile.mkstemp(prefix="." + path.name + ".", dir=path.parent)
        temporary = Path(temp_name)
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        if path.exists() or path.is_symlink():
            raise ManifestError(f"refusing to replace existing freeze artifact: {path}")
        # Publish a completed file without replacing a concurrently created
        # frozen artifact. The temporary file shares the target directory.
        os.link(temporary, path)
        temporary.unlink()
        directory_fd = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        if temporary is not None:
            try:
                temporary.unlink()
            except FileNotFoundError:
                pass


def freeze(args: argparse.Namespace) -> None:
    manifest = read_json(args.input)
    if not isinstance(manifest, dict):
        raise ManifestError("input proposal must be an object")
    if manifest.get("state") != "draft":
        raise ManifestError("freeze requires state=draft input; it will not re-freeze an existing manifest")
    errors = validate_manifest(manifest)
    if errors:
        raise ManifestError("cannot freeze incomplete proposal:\n- " + "\n- ".join(errors))
    frozen = dict(manifest)
    frozen["state"] = "frozen"
    frozen.pop("manifest_sha256", None)
    frozen["manifest_sha256"] = digest(frozen)
    errors = validate_manifest(frozen, require_frozen=True)
    if errors:
        raise ManifestError("internal frozen-manifest validation failed:\n- " + "\n- ".join(errors))
    write_new(args.output, canonical_bytes(frozen))
    print(f"frozen {args.output} sha256={frozen['manifest_sha256']}")


def validate(args: argparse.Namespace) -> None:
    manifest = read_json(args.manifest)
    errors = validate_manifest(manifest, require_frozen=args.require_frozen)
    if errors:
        raise ManifestError("manifest rejected:\n- " + "\n- ".join(errors))
    state = manifest["state"]
    print(f"valid {state} manifest sha256={digest({k: v for k, v in manifest.items() if k != 'manifest_sha256'})}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    freeze_parser = subparsers.add_parser("freeze", help="freeze a complete draft; output is create-only")
    freeze_parser.add_argument("--input", type=Path, required=True)
    freeze_parser.add_argument("--output", type=Path, required=True)
    freeze_parser.set_defaults(func=freeze)
    validate_parser = subparsers.add_parser("validate", help="check a proposal or canonical frozen manifest")
    validate_parser.add_argument("--manifest", type=Path, required=True)
    validate_parser.add_argument("--require-frozen", action="store_true")
    validate_parser.set_defaults(func=validate)
    args = parser.parse_args()
    try:
        args.func(args)
    except (ManifestError, OSError, TypeError, ValueError) as error:
        print(f"compare-manifest: {error}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
