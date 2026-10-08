#!/usr/bin/env python3
"""Summarize preserved historical oracle rows or validate prepared real-task cells."""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import statistics
import sys
import tempfile
from typing import Any


HEX64 = re.compile(r"^[0-9a-f]{64}$")
OPAQUE_ID = re.compile(r"^[A-Za-z0-9_:-]{1,256}$")
USAGE_FIELDS = ("input", "cached_input", "cache_write", "output", "reasoning")
CELL_SCHEMA = "kogen-real-task-cell/v1"
VALID_OUTCOMES = {"landed", "parked", "failed", "stopped", "interrupted", "infrastructure", "not_started"}
VALID_GRADES = {"pass", "fail", "ungraded"}


class ResultError(ValueError):
    pass


def read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"), parse_constant=lambda v: (_ for _ in ()).throw(ResultError(f"invalid number: {v}")))
    except OSError as error:
        raise ResultError(f"cannot read {path}: {error}") from error
    except json.JSONDecodeError as error:
        raise ResultError(f"invalid JSON in {path}:{error.lineno}:{error.colno}: {error.msg}") from error


def read_jsonl(path: Path) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    try:
        for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if not line.strip():
                continue
            try:
                row = json.loads(line)
            except json.JSONDecodeError as error:
                raise ResultError(f"invalid JSONL at {path}:{number}: {error.msg}") from error
            if not isinstance(row, dict):
                raise ResultError(f"JSONL row {number} must be an object")
            reject_sensitive_keys(row, f"{path}:{number}")
            rows.append(row)
    except OSError as error:
        raise ResultError(f"cannot read {path}: {error}") from error
    return rows


def reject_sensitive_keys(value: Any, where: str) -> None:
    if isinstance(value, dict):
        for key, child in value.items():
            lowered = str(key).lower()
            if lowered in {"prompt", "prompt_text", "request_body", "response_body", "authorization", "access_token", "refresh_token", "headers"}:
                raise ResultError(f"{where} contains forbidden raw/secret field {key!r}; provide allowlisted telemetry only")
            reject_sensitive_keys(child, where)
    elif isinstance(value, list):
        for child in value:
            reject_sensitive_keys(child, where)


def file_sha(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def emit(value: dict[str, Any], output: Path | None) -> None:
    raw = json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2, allow_nan=False) + "\n"
    if output is None:
        print(raw, end="")
    else:
        output.parent.mkdir(parents=True, exist_ok=True)
        temporary: Path | None = None
        try:
            fd, temp_name = tempfile.mkstemp(prefix="." + output.name + ".", dir=output.parent)
            temporary = Path(temp_name)
            os.fchmod(fd, 0o600)
            with os.fdopen(fd, "w", encoding="utf-8") as stream:
                stream.write(raw)
                stream.flush()
                os.fsync(stream.fileno())
            os.link(temporary, output)
            temporary.unlink()
            directory_fd = os.open(output.parent, os.O_RDONLY)
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


def historical(args: argparse.Namespace) -> dict[str, Any]:
    source_rows = read_jsonl(args.results)
    rows = [r for r in source_rows if isinstance(r.get("id"), str) and r.get("profile") != args.exclude_profile]
    ids = [r["id"] for r in rows]
    duplicates = sorted({case_id for case_id in ids if ids.count(case_id) > 1})
    if duplicates:
        raise ResultError("duplicate case IDs: " + ", ".join(duplicates))
    instance_total = 0
    instance_passed = 0
    instance_skipped = 0
    instance_errors = 0
    cases_by_status: dict[str, int] = {}
    failures: list[dict[str, Any]] = []
    for row in rows:
        status = str(row.get("status", "unknown"))
        cases_by_status[status] = cases_by_status.get(status, 0) + 1
        instances = row.get("instances") or {}
        if not isinstance(instances, dict):
            raise ResultError(f"{row.get('id')}: instances must be an object")
        total = instances.get("total", 0)
        passed = instances.get("passed", 0)
        skipped = instances.get("skipped", 0)
        errors = instances.get("errors", 0)
        if any(not nonnegative_int(v) for v in (total, passed, skipped, errors)) or passed + skipped + errors > total:
            raise ResultError(f"{row.get('id')}: invalid instance accounting")
        instance_total += total
        instance_passed += passed
        instance_skipped += skipped
        instance_errors += errors
        failed = total - passed - skipped - errors
        if status != "pass" or failed:
            failures.append({
                "id": row["id"], "profile": row.get("profile"), "title": row.get("title"), "status": status,
                "instances": {"total": total, "passed": passed, "failed": failed, "skipped": skipped, "errors": errors},
                "failures": row.get("failures", []), "hints": row.get("hints", []),
            })
    if len(rows) != args.expected_cases or instance_total != args.expected_instances:
        raise ResultError(
            f"historical denominator mismatch: resolved {len(rows)} cases/{instance_total} instances; "
            f"expected {args.expected_cases}/{args.expected_instances} after excluding profile {args.exclude_profile!r}"
        )
    instance_failed = instance_total - instance_passed - instance_skipped - instance_errors
    result = {
        "schema": "kogen-historical-oracle-report/v1",
        "oracle_version": "v1.2",
        "historical_only": True,
        "result_file": str(args.results.resolve()),
        "results_sha256": file_sha(args.results),
        "excluded_profile": args.exclude_profile,
        "denominator": {"cases": len(rows), "instances": instance_total},
        "cases": {"statuses": cases_by_status, "passed": cases_by_status.get("pass", 0),
                  "nonpass": len(rows) - cases_by_status.get("pass", 0)},
        "instances": {"passed": instance_passed, "failed": instance_failed,
                      "skipped": instance_skipped, "errors": instance_errors},
        "exact_nonpass_rows": failures,
        "not_a_v1_3_behavior_claim": True,
    }
    if args.conflict_ledger:
        conflicts = read_json(args.conflict_ledger)
        if not isinstance(conflicts, list):
            raise ResultError("conflict ledger must be a JSON array")
        result["documented_conflicts"] = conflicts
    emit(result, args.output)
    return result


def load_manifest(path: Path) -> dict[str, Any]:
    script_path = Path(__file__).with_name("compare-manifest.py")
    spec = importlib.util.spec_from_file_location("kogen_compare_manifest", script_path)
    if spec is None or spec.loader is None:
        raise ResultError("cannot load compare-manifest validator")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    manifest = module.read_json(path)
    errors = module.validate_manifest(manifest, require_frozen=True)
    if errors:
        raise ResultError("frozen manifest rejected:\n- " + "\n- ".join(errors))
    return manifest


def tuple_equal(left: Any, right: Any, fields: tuple[str, ...]) -> bool:
    return isinstance(left, dict) and isinstance(right, dict) and all(left.get(key) == right.get(key) for key in fields)


def nonnegative_int(value: Any) -> bool:
    return isinstance(value, int) and not isinstance(value, bool) and value >= 0


def validate_tokens(value: Any, where: str) -> list[str]:
    errors: list[str] = []
    if not isinstance(value, dict):
        return [f"{where} is required; unknown token totals remain null"]
    for field in USAGE_FIELDS:
        token_count = value.get(field)
        if token_count is not None and not nonnegative_int(token_count):
            errors.append(f"{where}.{field} must be null or a nonnegative integer")
    return errors


def validate_shape_accounting(value: Any, cap: dict[str, Any], where: str) -> list[str]:
    errors: list[str] = []
    if not isinstance(value, dict):
        return [f"{where} is required"]
    if value.get("schema") != 1 or value.get("profile") != "shape-v1.3":
        errors.append(f"{where} must identify schema 1 / shape-v1.3")
    if not isinstance(value.get("outcome"), str) or value.get("outcome") not in {"success", "failure"}:
        errors.append(f"{where}.outcome must be success or failure")
    for field in ("validation_passes", "validation_traversals", "finish_guards", "unknown_usage_attempts", "elapsed_ms"):
        if not nonnegative_int(value.get(field)):
            errors.append(f"{where}.{field} must be a nonnegative integer")
    if nonnegative_int(value.get("validation_passes")) and value["validation_passes"] > cap.get("shape_validation_passes", -1):
        errors.append(f"{where}.validation_passes exceeds the frozen arm cap")
    conversations = value.get("conversations")
    roles = value.get("roles")
    if not isinstance(conversations, list) or not isinstance(roles, list):
        errors.append(f"{where} must include conversation and role accounting arrays")
        return errors
    if len(conversations) > 2:
        errors.append(f"{where}.conversations exceeds the primary/fallback two-conversation limit")
    if value.get("fallback_started") is not True and value.get("fallback_started") is not False:
        errors.append(f"{where}.fallback_started must be boolean")
    fallback_present = any(isinstance(item, dict) and item.get("assigned_role") == "fallback_shaper" for item in conversations)
    if value.get("fallback_started") is True and not fallback_present:
        errors.append(f"{where}.fallback_started requires a fallback_shaper conversation record")
    if value.get("fallback_started") is False and fallback_present:
        errors.append(f"{where} has a fallback_shaper conversation while fallback_started is false")

    conversation_roles: set[str] = set()
    conversation_ids: set[str] = set()
    session_ids: set[str] = set()
    conversation_totals: dict[str, dict[str, int]] = {}
    conversation_tokens: dict[str, dict[str, int]] = {}
    count_fields = ("logical_turns", "http_attempts", "continuations", "validation_passes", "validation_traversals",
                    "finish_guards", "unknown_usage_attempts")
    for index, conversation in enumerate(conversations):
        if not isinstance(conversation, dict):
            errors.append(f"{where}.conversations[{index}] must be an object")
            continue
        assigned = conversation.get("assigned_role")
        if not isinstance(assigned, str) or assigned not in {"shaper", "fallback_shaper"}:
            errors.append(f"{where}.conversations[{index}].assigned_role must be shaper or fallback_shaper")
        elif assigned in conversation_roles:
            errors.append(f"{where} has duplicate conversation records for assigned role {assigned}")
        else:
            conversation_roles.add(assigned)
        if conversation.get("effective_role") != "shaper":
            errors.append(f"{where}.conversations[{index}].effective_role must resolve through shaper")
        for field in ("provider", "model", "effort"):
            if not isinstance(conversation.get(field), str) or not conversation[field]:
                errors.append(f"{where}.conversations[{index}].{field} is required")
        for field in ("logical_turns", "http_attempts", "continuations", "validation_passes", "validation_traversals",
                      "finish_guards", "unknown_usage_attempts"):
            if not nonnegative_int(conversation.get(field)):
                errors.append(f"{where}.conversations[{index}].{field} must be nonnegative")
        if nonnegative_int(conversation.get("logical_turns")) and conversation["logical_turns"] > cap.get("shape_logical_turns", -1):
            errors.append(f"{where}.conversations[{index}].logical_turns exceeds the frozen cap")
        if (nonnegative_int(conversation.get("http_attempts")) and nonnegative_int(conversation.get("logical_turns"))
                and conversation["http_attempts"] < conversation["logical_turns"]):
            errors.append(f"{where}.conversations[{index}] has fewer HTTP attempts than logical turns")
        conversation_id = conversation.get("conversation_id")
        session_id = conversation.get("session_id")
        if conversation_id and (not isinstance(conversation_id, str) or not OPAQUE_ID.fullmatch(conversation_id)):
            errors.append(f"{where}.conversations[{index}].conversation_id must be an opaque thread ID")
        if session_id and (not isinstance(session_id, str) or not OPAQUE_ID.fullmatch(session_id)):
            errors.append(f"{where}.conversations[{index}].session_id must be an opaque cache/session ID")
        if nonnegative_int(conversation.get("http_attempts")) and conversation["http_attempts"] > 0 and (not conversation_id or not session_id):
            errors.append(f"{where}.conversations[{index}] must identify the thread and session for measured attempts")
        if isinstance(conversation_id, str) and conversation_id:
            if conversation_id in conversation_ids:
                errors.append(f"{where} reused a thread ID across Shape conversations")
            conversation_ids.add(conversation_id)
        if isinstance(session_id, str) and session_id:
            session_ids.add(session_id)
        if isinstance(assigned, str) and assigned in {"shaper", "fallback_shaper"}:
            conversation_totals[assigned] = {field: conversation.get(field, 0) for field in count_fields}
            tokens = conversation.get("tokens")
            errors.extend(validate_tokens(tokens, f"{where}.conversations[{index}].tokens"))
            conversation_tokens[assigned] = {
                field: tokens.get(field) for field in USAGE_FIELDS
            } if isinstance(tokens, dict) else {}
    if len(session_ids) > 1:
        errors.append(f"{where} changed the run-level cache/session ID between Shape conversations")
    expected_roles = {"shaper", "fallback_shaper", "auditor"}
    role_by_name: dict[str, dict[str, Any]] = {}
    for index, role in enumerate(roles):
        if not isinstance(role, dict):
            errors.append(f"{where}.roles[{index}] must be an object")
            continue
        assigned = role.get("assigned_role")
        if not isinstance(assigned, str) or assigned not in expected_roles:
            errors.append(f"{where}.roles[{index}].assigned_role is not a Shape accounting role")
            continue
        if assigned in role_by_name:
            errors.append(f"{where} has duplicate role aggregate {assigned}")
        role_by_name[assigned] = role
        expected_effective = "auditor" if assigned == "auditor" else "shaper"
        if role.get("effective_role") != expected_effective:
            errors.append(f"{where}.roles[{index}].effective_role does not match its assigned role")
        for field in ("provider", "model", "effort"):
            if not isinstance(role.get(field), str) or not role[field]:
                errors.append(f"{where}.roles[{index}].{field} is required")
        for field in ("logical_turns", "http_attempts", "continuations", "validation_passes", "unknown_usage_attempts"):
            if not nonnegative_int(role.get(field)):
                errors.append(f"{where}.roles[{index}].{field} must be a nonnegative integer")
        errors.extend(validate_tokens(role.get("tokens"), f"{where}.roles[{index}].tokens"))
    if set(role_by_name) != expected_roles:
        errors.append(f"{where}.roles must contain shaper, fallback_shaper, and auditor aggregates")
    for assigned, conv_counts in conversation_totals.items():
        role = role_by_name.get(assigned)
        if role is not None:
            for field, expected in conv_counts.items():
                if field in role and role[field] != expected:
                    errors.append(f"{where}.roles.{assigned}.{field} differs from its conversation total")
            tokens = role.get("tokens")
            if isinstance(tokens, dict):
                conv_tokens = conversation_tokens.get(assigned, {})
                for field in USAGE_FIELDS:
                    if tokens.get(field) != conv_tokens.get(field):
                        errors.append(f"{where}.roles.{assigned}.tokens.{field} differs from its conversation total")
    for assigned in {"shaper", "fallback_shaper"} - set(conversation_totals):
        role = role_by_name.get(assigned)
        if role is not None:
            if any(nonnegative_int(role.get(field)) and role[field] != 0 for field in
                   ("logical_turns", "http_attempts", "continuations", "validation_passes", "unknown_usage_attempts")):
                errors.append(f"{where}.roles.{assigned} has activity without a conversation record")
    for field in ("repairs", "tokens"):
        if field == "repairs":
            repairs = value.get(field)
            if not isinstance(repairs, dict) or any(not isinstance(k, str) or not nonnegative_int(v) for k, v in (repairs or {}).items()):
                errors.append(f"{where}.repairs must contain nonnegative repair counters")
        else:
            errors.extend(validate_tokens(value.get(field), f"{where}.tokens"))
    for field in ("validation_passes", "validation_traversals", "finish_guards"):
        if nonnegative_int(value.get(field)) and all(
            isinstance(conversation, dict) and nonnegative_int(conversation.get(field))
            for conversation in conversations
        ):
            conversation_sum = sum(conversation[field] for conversation in conversations)
            if conversation_sum != value[field]:
                errors.append(f"{where}.{field} differs from conversation totals")
    if nonnegative_int(value.get("unknown_usage_attempts")) and len(role_by_name) == len(expected_roles) and all(
        nonnegative_int(role.get("unknown_usage_attempts")) for role in role_by_name.values()
    ):
        role_sum = sum(role["unknown_usage_attempts"] for role in role_by_name.values())
        if role_sum != value["unknown_usage_attempts"]:
            errors.append(f"{where}.unknown_usage_attempts differs from role totals")
    global_tokens = value.get("tokens")
    if isinstance(global_tokens, dict) and len(role_by_name) == len(expected_roles):
        for field in USAGE_FIELDS:
            role_values = [role.get("tokens", {}).get(field) for role in role_by_name.values()
                           if isinstance(role.get("tokens"), dict)]
            known = [token_count for token_count in role_values if nonnegative_int(token_count)]
            if global_tokens.get(field) != (sum(known) if known else None):
                errors.append(f"{where}.tokens.{field} differs from role totals")
    return errors


def validate_build_accounting(value: Any, cap: dict[str, Any], where: str) -> list[str]:
    errors: list[str] = []
    if not isinstance(value, dict):
        return [f"{where} is required for every planned cell"]
    for field in ("status", "elapsed_ms", "model_request_count", "unknown_usage_attempts", "rung_count", "provider_wait_ms"):
        if field not in value:
            errors.append(f"{where}.{field} is required")
    if not isinstance(value.get("status"), str) or value.get("status") not in VALID_OUTCOMES:
        errors.append(f"{where}.status must preserve a terminal/stopped/interrupted/infrastructure outcome")
    for field in ("elapsed_ms", "model_request_count", "unknown_usage_attempts", "rung_count", "provider_wait_ms"):
        if field in value and not nonnegative_int(value[field]):
            errors.append(f"{where}.{field} must be a nonnegative integer")
    for metric, cap_name in (("elapsed_ms", "build_wall_ms"), ("rung_count", "max_rungs"),
                             ("model_request_count", "request_attempts")):
        if nonnegative_int(value.get(metric)) and nonnegative_int(cap.get(cap_name)) and value[metric] > cap[cap_name]:
            errors.append(f"{where}.{metric} exceeds the frozen {cap_name} cap")
    if nonnegative_int(value.get("provider_wait_ms")) and nonnegative_int(value.get("elapsed_ms")) and value["provider_wait_ms"] > value["elapsed_ms"]:
        errors.append(f"{where}.provider_wait_ms exceeds total Build elapsed time")
    if nonnegative_int(value.get("unknown_usage_attempts")) and nonnegative_int(value.get("model_request_count")) and value["unknown_usage_attempts"] > value["model_request_count"]:
        errors.append(f"{where}.unknown_usage_attempts exceeds Build model request count")
    return errors


def validate_request(request: Any, arm: dict[str, Any], where: str) -> list[str]:
    errors: list[str] = []
    if not isinstance(request, dict):
        return [f"{where} must be an object"]
    for field in ("request_id", "stage", "effective_role", "provider", "model", "effort", "endpoint_host", "endpoint_path",
                  "request_started_ms", "response_ended_ms", "body_bytes", "prefix_sha256", "static_prefix_sha256",
                  "invocation_id", "attempt_id", "rung_id", "epoch", "thread_scope_id", "conversation_id",
                  "cache_namespace_id", "cache_key", "session_id", "thread_id", "routing_headers", "usage"):
        if field not in request:
            errors.append(f"{where}.{field} is missing; request evidence is incomplete")
    for field in ("request_id", "invocation_id", "conversation_id", "cache_namespace_id", "cache_key", "session_id", "thread_id"):
        value = request.get(field)
        if not isinstance(value, str) or not OPAQUE_ID.fullmatch(value):
            errors.append(f"{where}.{field} must be a path/secret-free opaque ID")
    for field in ("stage", "effective_role", "provider", "model", "effort", "endpoint_host", "endpoint_path"):
        if not isinstance(request.get(field), str) or not request[field]:
            errors.append(f"{where}.{field} must be nonempty text")
    if request.get("cache_key") == request.get("thread_id"):
        errors.append(f"{where}.cache_key and thread_id must be separate identities")
    if request.get("session_id") != request.get("cache_key"):
        errors.append(f"{where}.session_id must equal the persisted Build cache_key")
    if request.get("conversation_id") != request.get("thread_id"):
        errors.append(f"{where}.conversation_id must equal the stable thread ID")
    for field in ("attempt_id", "rung_id", "epoch"):
        value = request.get(field)
        if value is not None and (not isinstance(value, str) or not OPAQUE_ID.fullmatch(value)):
            errors.append(f"{where}.{field} must be null or a path/secret-free opaque ID")
    if not isinstance(request.get("thread_scope_id"), str) or not OPAQUE_ID.fullmatch(request.get("thread_scope_id", "")):
        errors.append(f"{where}.thread_scope_id must be a path/secret-free opaque ID")
    if not isinstance(request.get("static_prefix_sha256"), str) or not HEX64.fullmatch(request.get("static_prefix_sha256", "")):
        errors.append(f"{where}.static_prefix_sha256 must identify the immutable generic/schema prefix")
    roles = arm.get("roles", {})
    role_name = request.get("effective_role")
    expected_role = roles.get(role_name) if isinstance(roles, dict) and isinstance(role_name, str) else None
    rung_id = request.get("rung_id")
    recipe = arm.get("recipe", {})
    if role_name == "builder" and rung_id is None:
        errors.append(f"{where}.builder request must name its frozen recipe rung")
    if role_name == "builder" and rung_id is not None and isinstance(recipe, dict):
        rung = next((r for r in recipe.get("rungs", []) if isinstance(r, dict) and r.get("id") == rung_id), None)
        if rung is None:
            errors.append(f"{where}.rung_id is not present in the frozen recipe")
        else:
            expected_role = {"provider": rung.get("provider"), "model": rung.get("model"), "effort": rung.get("effort")}
    if not isinstance(expected_role, dict):
        errors.append(f"{where}.effective_role is absent from the resolved role manifest")
    elif not tuple_equal(request, expected_role, ("provider", "model", "effort")):
        errors.append(f"{where} effective provider/model/effort does not match the frozen role/rung")
    provider = arm.get("provider", {})
    if isinstance(provider, dict):
        for field in ("endpoint_host", "endpoint_path"):
            if request.get(field) != provider.get(field):
                errors.append(f"{where}.{field} does not match the arm's frozen endpoint")
    for field in ("request_started_ms", "response_ended_ms", "body_bytes"):
        if not nonnegative_int(request.get(field)):
            errors.append(f"{where}.{field} must be nonnegative")
    if nonnegative_int(request.get("request_started_ms")) and nonnegative_int(request.get("response_ended_ms")) and request["response_ended_ms"] < request["request_started_ms"]:
        errors.append(f"{where} response precedes request")
    if not isinstance(request.get("prefix_sha256"), list) or not request.get("prefix_sha256") or any(not isinstance(d, str) or not HEX64.fullmatch(d) for d in request.get("prefix_sha256", [])):
        errors.append(f"{where}.prefix_sha256 must contain one or more SHA-256 digests")
    if nonnegative_int(request.get("body_bytes")) and request["body_bytes"] == 0:
        errors.append(f"{where}.body_bytes must be positive for a measured request")
    if not isinstance(request.get("routing_headers"), list) or any(not isinstance(v, str) for v in request.get("routing_headers", [])):
        errors.append(f"{where}.routing_headers must contain names only")
    usage = request.get("usage")
    if usage is not None:
        if not isinstance(usage, dict):
            errors.append(f"{where}.usage must be null or an object of nullable counters")
        else:
            for field in USAGE_FIELDS:
                value = usage.get(field)
                if value is not None and not nonnegative_int(value):
                    errors.append(f"{where}.usage.{field} must be null or nonnegative")
            # `input` excludes cached input, so cached_input can validly be greater.
    return errors


def expected_cells(manifest: dict[str, Any]) -> dict[tuple[str, str, int, str], dict[str, Any]]:
    arms = {arm["id"]: arm for arm in manifest["arms"]}
    repetitions = manifest["design"]["repetitions"]
    expected: dict[tuple[str, str, int, str], dict[str, Any]] = {}
    for task in manifest["tasks"]:
        for lane in task["lanes"]:
            for repetition in range(1, repetitions + 1):
                for arm_id, arm in arms.items():
                    expected[(lane, task["id"], repetition, arm_id)] = {"task": task, "arm": arm}
    return expected


def load_build_effort(path: Path | None, manifest: dict[str, Any]) -> dict[str, Any]:
    if path is None:
        return {"schema": "kogen-build-effort-report/v1", "status": "unavailable",
                "records": [], "errors": ["--effort evidence was not supplied"]}
    source = read_json(path)
    errors: list[str] = []
    if not isinstance(source, dict) or source.get("schema") != "kogen-build-effort/v1":
        return {"schema": "kogen-build-effort-report/v1", "status": "invalid", "records": [],
                "errors": ["input schema must be kogen-build-effort/v1"]}
    if source.get("study_id") != manifest.get("study_id"):
        errors.append("study_id does not match the frozen comparison manifest")
    records = source.get("records")
    if not isinstance(records, list):
        return {"schema": "kogen-build-effort-report/v1", "status": "invalid", "records": [],
                "errors": ["records must be an array"]}
    expected_arms = {arm["id"]: arm for arm in manifest["arms"]}
    seen: set[str] = set()
    result_records: list[dict[str, Any]] = []
    rates = {(r.get("provider"), r.get("model"), r.get("endpoint_host")): r
             for r in manifest.get("rate_snapshots", []) if isinstance(r, dict)}
    numeric_fields = ("worker_wall_hours", "coordinator_hours", "queue_failures", "rebases", "rework_events",
                      "hours_to_first_end_to_end_green", "hours_to_final_parity")
    for index, record in enumerate(records):
        where = f"records[{index}]"
        if not isinstance(record, dict):
            errors.append(f"{where} must be an object")
            continue
        arm_id = record.get("arm")
        if arm_id not in expected_arms:
            errors.append(f"{where}.arm is not a frozen arm")
            continue
        if arm_id in seen:
            errors.append(f"duplicate build-effort record for {arm_id}")
        seen.add(arm_id)
        arm = expected_arms[arm_id]
        if record.get("source_revision") != arm.get("source_revision"):
            errors.append(f"{where}.source_revision differs from the frozen arm")
        if record.get("development_history_context") != arm.get("development_history_context"):
            errors.append(f"{where}.development_history_context differs from the frozen arm")
        required_numeric_fields = {"worker_wall_hours", "coordinator_hours", "queue_failures", "rebases", "rework_events"}
        for field in numeric_fields:
            value = record.get(field)
            if value is not None and (not isinstance(value, (int, float)) or isinstance(value, bool) or value < 0):
                errors.append(f"{where}.{field} must be null or nonnegative")
            if field in required_numeric_fields and value is None:
                errors.append(f"{where}.{field} must be measured; use null only for unfinished time-to-milestone fields")
        parity = record.get("parity_status")
        if parity not in {"reached", "not_reached", "blocked"}:
            errors.append(f"{where}.parity_status must be reached/not_reached/blocked")
        if parity == "reached" and record.get("hours_to_final_parity") is None:
            errors.append(f"{where}.hours_to_final_parity is required when parity was reached")
        if parity != "reached" and record.get("hours_to_final_parity") is not None:
            errors.append(f"{where}.hours_to_final_parity must remain null before parity")
        usage_rows = record.get("model_usage")
        if not isinstance(usage_rows, list):
            errors.append(f"{where}.model_usage must be an array; unknown usage is represented explicitly")
            usage_rows = []
        usage_costs: dict[str, float] = {}
        unknown_usage = 0
        unknown_rates = 0
        for usage_index, usage_row in enumerate(usage_rows):
            if not isinstance(usage_row, dict):
                errors.append(f"{where}.model_usage[{usage_index}] must be an object")
                continue
            provider, model, endpoint = usage_row.get("provider"), usage_row.get("model"), usage_row.get("endpoint_host")
            usage = usage_row.get("usage")
            if not all(isinstance(v, str) and v for v in (provider, model, endpoint)):
                errors.append(f"{where}.model_usage[{usage_index}] must bind provider/model/endpoint")
            if usage is None:
                unknown_usage += 1
                continue
            if not isinstance(usage, dict):
                errors.append(f"{where}.model_usage[{usage_index}].usage must be null or an object")
                unknown_usage += 1
                continue
            for field in USAGE_FIELDS:
                value = usage.get(field)
                if value is not None and not nonnegative_int(value):
                    errors.append(f"{where}.model_usage[{usage_index}].usage.{field} must be null or nonnegative")
            rate = rates.get((provider, model, endpoint))
            if rate is None:
                unknown_rates += 1
            elif rate.get("basis") == "verified_rate_card":
                prices = rate["per_million"]
                mapping = {"input": "uncached_input", "cached_input": "cached_input",
                           "cache_write": "cache_write", "output": "output", "reasoning": "reasoning"}
                if any(not nonnegative_int(usage.get(field)) for field in USAGE_FIELDS):
                    unknown_usage += 1
                else:
                    currency = rate["currency"]
                    usage_costs[currency] = usage_costs.get(currency, 0.0) + sum(
                        usage[field] * prices[mapping[field]] / 1_000_000 for field in USAGE_FIELDS)
        result_records.append({**record, "model_usage_cost_estimate_by_currency": usage_costs,
                               "unknown_usage_records": unknown_usage, "unknown_rate_records": unknown_rates,
                               "usage_cost_completeness": "complete" if not unknown_usage and not unknown_rates else "partial"})
    missing_arms = sorted(set(expected_arms) - seen)
    if missing_arms:
        errors.append("missing build-effort records for arms: " + ", ".join(missing_arms))
    return {"schema": "kogen-build-effort-report/v1", "status": "complete" if not errors else "invalid_or_incomplete",
            "input_sha256": file_sha(path), "records": result_records, "errors": errors}


def cells(args: argparse.Namespace) -> dict[str, Any]:
    manifest = load_manifest(args.manifest)
    effort = load_build_effort(args.effort, manifest)
    expected = expected_cells(manifest)
    rows = read_jsonl(args.cells)
    rate_index = {
        (rate.get("provider"), rate.get("model"), rate.get("endpoint_host")): rate
        for rate in manifest.get("rate_snapshots", []) if isinstance(rate, dict)
    }
    actual: dict[tuple[str, str, int, str], dict[str, Any]] = {}
    cell_errors: list[dict[str, Any]] = []
    for line_no, row in enumerate(rows, 1):
        if row.get("schema") != CELL_SCHEMA:
            cell_errors.append({"line": line_no, "cell_id": row.get("cell_id"), "error": f"schema must be {CELL_SCHEMA}"})
            continue
        key = (row.get("lane"), row.get("task_id"), row.get("repetition"), row.get("arm"))
        if (not isinstance(key[0], str) or not isinstance(key[1], str) or not nonnegative_int(key[2]) or key[2] < 1
                or not isinstance(key[3], str)):
            cell_errors.append({"line": line_no, "cell_id": row.get("cell_id"), "error": "cell identity fields are invalid"})
            continue
        if key not in expected:
            cell_errors.append({"line": line_no, "cell_id": row.get("cell_id"), "error": "cell is outside the frozen denominator"})
            continue
        if key in actual:
            cell_errors.append({"line": line_no, "cell_id": row.get("cell_id"), "error": "duplicate planned cell"})
            continue
        actual[key] = row
        expected_row = expected[key]
        task, arm = expected_row["task"], expected_row["arm"]
        where = f"cell {row.get('cell_id', key)}"
        if not isinstance(row.get("status"), str) or row.get("status") not in VALID_OUTCOMES:
            cell_errors.append({"cell_id": row.get("cell_id"), "error": f"{where}.status is invalid"})
        for actual_field, frozen_field in (("task_tree_sha256", "task_tree_sha256"), ("request_sha256", "request_sha256"), ("acceptance_sha256", "acceptance_sha256")):
            if row.get(actual_field) != task.get(frozen_field):
                cell_errors.append({"cell_id": row.get("cell_id"), "error": f"{where}.{actual_field} differs from frozen task bytes"})
        if row.get("grader") != task.get("grader"):
            cell_errors.append({"cell_id": row.get("cell_id"), "error": f"{where}.grader does not match the frozen independent rubric"})
        for actual_field, frozen_field in (("source_revision", "source_revision"), ("binary_sha256", "binary_sha256"),
                                           ("binary_bytes", "binary_bytes"), ("roles", "roles"), ("recipe", "recipe"),
                                           ("caps", "caps"), ("provider", "provider"), ("sandbox_policy_sha256", "sandbox_policy_sha256")):
            if row.get(actual_field) != arm.get(frozen_field):
                cell_errors.append({"cell_id": row.get("cell_id"), "error": f"{where}.{actual_field} differs from the frozen arm"})
        host = row.get("host")
        if not isinstance(host, dict) or host != arm.get("host"):
            cell_errors.append({"cell_id": row.get("cell_id"), "error": f"{where}.host does not match the frozen host"})
        grade = row.get("grade")
        if not isinstance(grade, dict) or not isinstance(grade.get("status"), str) or grade.get("status") not in VALID_GRADES:
            cell_errors.append({"cell_id": row.get("cell_id"), "error": f"{where}.grade status must be pass/fail/ungraded"})
        elif grade.get("status") != "ungraded" and grade.get("rubric_sha256") != task["grader"].get("rubric_sha256"):
            cell_errors.append({"cell_id": row.get("cell_id"), "error": f"{where}.grade used a different rubric digest"})
        requests = row.get("model_requests")
        if not isinstance(requests, list):
            cell_errors.append({"cell_id": row.get("cell_id"), "error": f"{where}.model_requests must preserve every request or identify none as []"})
            requests = []
        seen_request_ids: set[str] = set()
        scope_bindings: dict[tuple[Any, ...], tuple[str, str]] = {}
        scope_id_bindings: dict[str, tuple[Any, ...]] = {}
        thread_scope_bindings: dict[str, tuple[Any, ...]] = {}
        invocation_cache_keys: dict[str, str] = {}
        for request_index, request in enumerate(requests):
            errs = validate_request(request, arm, f"{where}.model_requests[{request_index}]")
            if isinstance(request, dict):
                request_id = request.get("request_id")
                if not isinstance(request_id, str) or not request_id or request_id in seen_request_ids:
                    errs.append("request_id is missing or duplicated within the cell")
                seen_request_ids.add(str(request_id))
                scope_values = (request.get("invocation_id"), request.get("stage"), request.get("attempt_id"),
                                request.get("rung_id"), request.get("epoch"))
                if (all(isinstance(item, str) for item in scope_values[:2]) and
                        all(item is None or isinstance(item, str) for item in scope_values[2:]) and
                        isinstance(request.get("thread_scope_id"), str) and isinstance(request.get("thread_id"), str)):
                    scope_key = tuple(scope_values)
                    binding = (request["thread_scope_id"], request["thread_id"])
                    if scope_key in scope_bindings and scope_bindings[scope_key] != binding:
                        errs.append("thread identity changed within one stage/attempt/rung/epoch scope")
                    scope_bindings[scope_key] = binding
                    if request["thread_scope_id"] in scope_id_bindings and scope_id_bindings[request["thread_scope_id"]] != scope_key:
                        errs.append("thread_scope_id was reused for a different stage/attempt/rung/epoch")
                    scope_id_bindings[request["thread_scope_id"]] = scope_key
                    if request["thread_id"] in thread_scope_bindings and thread_scope_bindings[request["thread_id"]] != scope_key:
                        errs.append("thread_id was reused across stage/attempt/rung/epoch scopes")
                    thread_scope_bindings[request["thread_id"]] = scope_key
                invocation = request.get("invocation_id")
                cache_key = request.get("cache_key")
                if isinstance(invocation, str) and isinstance(cache_key, str):
                    if invocation in invocation_cache_keys and invocation_cache_keys[invocation] != cache_key:
                        errs.append("Build cache_key changed within one invocation")
                    invocation_cache_keys[invocation] = cache_key
            for error in errs:
                cell_errors.append({"cell_id": row.get("cell_id"), "error": error})
        if key[0] == "end_to_end":
            for error in validate_shape_accounting(row.get("shape_accounting"), arm.get("caps", {}), f"{where}.shape_accounting"):
                cell_errors.append({"cell_id": row.get("cell_id"), "error": error})
        build_accounting = row.get("build_accounting")
        for error in validate_build_accounting(build_accounting, arm.get("caps", {}), f"{where}.build_accounting"):
            cell_errors.append({"cell_id": row.get("cell_id"), "error": error})
        if isinstance(build_accounting, dict) and nonnegative_int(build_accounting.get("model_request_count")):
            build_requests = sum(1 for request in requests if isinstance(request, dict) and request.get("stage") == "build")
            if build_requests != build_accounting["model_request_count"]:
                cell_errors.append({"cell_id": row.get("cell_id"),
                                    "error": f"{where}.build_accounting.model_request_count differs from build-stage request evidence"})
        if key[0] == "end_to_end" and isinstance(row.get("shape_accounting"), dict):
            shape_requests = sum(1 for request in requests if isinstance(request, dict) and request.get("stage") == "shape")
            shape_roles = row["shape_accounting"].get("roles")
            if isinstance(shape_roles, list) and all(isinstance(role, dict) and nonnegative_int(role.get("http_attempts")) for role in shape_roles):
                recorded_shape_attempts = sum(role["http_attempts"] for role in shape_roles)
                if shape_requests != recorded_shape_attempts:
                    cell_errors.append({"cell_id": row.get("cell_id"),
                                        "error": f"{where}.shape_accounting role HTTP attempts differ from shape-stage request evidence"})
            conversations = row["shape_accounting"].get("conversations")
            if isinstance(conversations, list):
                for conversation in conversations:
                    if not isinstance(conversation, dict) or not nonnegative_int(conversation.get("http_attempts")):
                        continue
                    conversation_id = conversation.get("conversation_id")
                    session_id = conversation.get("session_id")
                    observed = [request for request in requests if isinstance(request, dict) and request.get("stage") == "shape"
                                and request.get("thread_id") == conversation_id]
                    if len(observed) != conversation["http_attempts"]:
                        cell_errors.append({"cell_id": row.get("cell_id"),
                                            "error": f"{where}.shape_accounting conversation HTTP attempts differ from matching thread requests"})
                    if any(request.get("session_id") != session_id for request in observed):
                        cell_errors.append({"cell_id": row.get("cell_id"),
                                            "error": f"{where}.shape_accounting session ID differs from matching request cache key"})
        if not isinstance(row.get("phase_ms"), dict) or any(not nonnegative_int(v) for v in row.get("phase_ms", {}).values()):
            cell_errors.append({"cell_id": row.get("cell_id"), "error": f"{where}.phase_ms must preserve nonnegative phase wall times"})

    missing = []
    for key, desc in sorted(expected.items()):
        if key not in actual:
            lane, task_id, repetition, arm_id = key
            missing.append({"lane": lane, "task_id": task_id, "repetition": repetition, "arm": arm_id,
                            "denominator_status": "not_started"})

    # Token accounting reports partial sums per request field; unknown stays null.
    aggregates: dict[str, dict[str, Any]] = {}
    for arm in manifest["arms"]:
        for lane in manifest["design"]["lanes"]:
            aggregates[f"{arm['id']}/{lane}"] = {
                "cells": 0, "grade_pass": 0, "grade_fail": 0, "ungraded": 0,
                "outcomes": {}, "usage": {field: {"known_tokens": 0, "unknown_requests": 0} for field in USAGE_FIELDS},
                "total_input": {"known_tokens": 0, "unknown_requests": 0}, "cached_input": 0,
                "unknown_usage_attempts": 0,
                "cost_estimate": {"by_currency": {}, "unknown_rate_requests": 0,
                                  "unknown_usage_requests": 0, "subscription_requests": 0},
            }
    durations: dict[tuple[str, str], list[int]] = {}
    for key, row in actual.items():
        arm_id, lane = row["arm"], row["lane"]
        aggregate = aggregates.setdefault(f"{arm_id}/{lane}", {
            "cells": 0, "grade_pass": 0, "grade_fail": 0, "ungraded": 0,
            "outcomes": {}, "usage": {field: {"known_tokens": 0, "unknown_requests": 0} for field in USAGE_FIELDS},
            "total_input": {"known_tokens": 0, "unknown_requests": 0}, "cached_input": 0,
            "unknown_usage_attempts": 0,
            "cost_estimate": {"by_currency": {}, "unknown_rate_requests": 0,
                              "unknown_usage_requests": 0, "subscription_requests": 0},
        })
        aggregate["cells"] += 1
        status = row.get("status", "unknown")
        aggregate["outcomes"][status] = aggregate["outcomes"].get(status, 0) + 1
        grade_status = (row.get("grade") or {}).get("status", "ungraded")
        aggregate[{"pass": "grade_pass", "fail": "grade_fail", "ungraded": "ungraded"}.get(grade_status, "ungraded")] += 1
        requests = row.get("model_requests") if isinstance(row.get("model_requests"), list) else []
        for request in requests:
            usage = request.get("usage") if isinstance(request, dict) else None
            if usage is None:
                aggregate["unknown_usage_attempts"] += 1
                usage = {}
            for field in USAGE_FIELDS:
                value = usage.get(field) if isinstance(usage, dict) else None
                if nonnegative_int(value):
                    aggregate["usage"][field]["known_tokens"] += value
                else:
                    aggregate["usage"][field]["unknown_requests"] += 1
            uncached = usage.get("input") if isinstance(usage, dict) else None
            cached = usage.get("cached_input") if isinstance(usage, dict) else None
            if nonnegative_int(uncached) and nonnegative_int(cached):
                aggregate["total_input"]["known_tokens"] += uncached + cached
                aggregate["cached_input"] += cached
            else:
                aggregate["total_input"]["unknown_requests"] += 1
            rate = rate_index.get((request.get("provider"), request.get("model"), request.get("endpoint_host")))
            if rate is None:
                aggregate["cost_estimate"]["unknown_rate_requests"] += 1
            elif rate.get("basis") == "subscription_measured_usage":
                aggregate["cost_estimate"]["subscription_requests"] += 1
            else:
                prices = rate.get("per_million", {})
                if not isinstance(usage, dict) or any(not nonnegative_int(usage.get(field)) for field in USAGE_FIELDS):
                    aggregate["cost_estimate"]["unknown_usage_requests"] += 1
                else:
                    currency = rate.get("currency", "unknown")
                    rate_field = {"input": "uncached_input", "cached_input": "cached_input",
                                  "cache_write": "cache_write", "output": "output", "reasoning": "reasoning"}
                    amount = sum(usage[field] * prices[rate_field[field]] / 1_000_000 for field in USAGE_FIELDS)
                    aggregate["cost_estimate"]["by_currency"][currency] = (
                        aggregate["cost_estimate"]["by_currency"].get(currency, 0.0) + amount
                    )
        phases = row.get("phase_ms") if isinstance(row.get("phase_ms"), dict) else {}
        for phase, value in phases.items():
            durations.setdefault((arm_id, phase), []).append(value)

    task_denominator: dict[tuple[str, str], int] = {}
    for lane, task, repetition, arm in expected:
        task_denominator[(arm, lane)] = task_denominator.get((arm, lane), 0) + 1
    for key, item in aggregates.items():
        arm_id, lane = key.split("/", 1)
        item["denominator_cells"] = task_denominator.get((arm_id, lane), 0)
        item["missing_cells"] = item["denominator_cells"] - item["cells"]
        item["grader_success_rate"] = item["grade_pass"] / item["denominator_cells"] if item["denominator_cells"] else None
        item["raw_cache_hit_rate"] = item["cached_input"] / item["total_input"]["known_tokens"] if item["total_input"]["known_tokens"] else None
        cost = item["cost_estimate"]
        cost["completeness"] = "complete" if not any(cost[key] for key in ("unknown_rate_requests", "unknown_usage_requests")) else "partial"
        if cost["subscription_requests"] and not cost["by_currency"]:
            cost["claim"] = "measured_usage_only"
        elif cost["by_currency"]:
            cost["claim"] = "external_rate_card_estimate"
        else:
            cost["claim"] = "unavailable"

    duration_summary = []
    for (arm_id, phase), samples in sorted(durations.items()):
        ordered = sorted(samples)
        p95_index = max(0, math.ceil(0.95 * len(ordered)) - 1)
        duration_summary.append({"arm": arm_id, "phase": phase, "samples": len(ordered),
                                 "p50_ms": statistics.median(ordered), "p95_ms": ordered[p95_index]})
    result = {
        "schema": "kogen-real-task-comparison-report/v1",
        "manifest_sha256": manifest["manifest_sha256"],
        "input_cells_sha256": file_sha(args.cells),
        "planned_denominator_cells": len(expected),
        "observed_cells": len(actual),
        "missing_cells_retained_as_not_started": missing,
        "arm_lane_summary": aggregates,
        "phase_timing": duration_summary,
        "cell_validation_errors": cell_errors,
        "build_effort": effort,
        "admission": "descriptive_only" if not cell_errors and not missing and effort["status"] == "complete" else "invalid_or_incomplete",
        "winner_claimed": False,
        "paired_significance_test": "not_run_by_this_collector",
        "cost_claim": "token_usage_only_unless_frozen_verified_rate_card_applies",
    }
    emit(result, args.output)
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    hist = sub.add_parser("historical", help="summarize a complete preserved conformance JSONL")
    hist.add_argument("--results", type=Path, required=True)
    hist.add_argument("--expected-cases", type=int, default=236)
    hist.add_argument("--expected-instances", type=int, default=570)
    hist.add_argument("--exclude-profile", default="exunit")
    hist.add_argument("--conflict-ledger", type=Path)
    hist.add_argument("--output", type=Path)
    hist.set_defaults(func=historical)
    compare = sub.add_parser("cells", help="validate and summarize a complete frozen cell JSONL")
    compare.add_argument("--manifest", type=Path, required=True)
    compare.add_argument("--cells", type=Path, required=True)
    compare.add_argument("--effort", type=Path, help="post-run kogen-build-effort/v1 records for all arms")
    compare.add_argument("--output", type=Path)
    compare.set_defaults(func=cells)
    args = parser.parse_args()
    try:
        result = args.func(args)
        if args.command == "cells":
            return 0 if result["admission"] == "descriptive_only" else 1
        return 0
    except (ResultError, OSError, TypeError, ValueError, KeyError) as error:
        print(f"compare-results: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
