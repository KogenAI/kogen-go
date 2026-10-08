#!/usr/bin/env python3
"""Preflight and report cache telemetry without making provider requests."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from collections import defaultdict
from pathlib import Path
import re
import sys
import tempfile
from typing import Any


REPLAY_SCHEMA = "kogen-cache-replay/v1"
HEX64 = re.compile(r"^[0-9a-f]{64}$")
OPAQUE_ID = re.compile(r"^[A-Za-z0-9_:-]{1,256}$")
USAGE_FIELDS = ("input", "cached_input", "cache_write", "output", "reasoning")
METADATA_FIELDS = (
    "request_id", "stage", "provider", "model", "effort", "adapter_version", "prompt_version",
    "attempt_id", "rung_id", "epoch", "thread_scope_id",
    "invocation_id", "conversation_id", "cache_namespace_id", "endpoint_host", "endpoint_path",
    "request_started_ms", "response_ended_ms", "body_bytes", "prefix_sha256", "static_prefix_sha256", "cache_key", "session_id",
    "eligibility_rule_id", "eligibility_rule_sha256", "thread_id", "routing_headers", "usage",
)


class ReportError(ValueError):
    pass


def read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"), parse_constant=lambda v: (_ for _ in ()).throw(ReportError(f"invalid number {v}")))
    except OSError as error:
        raise ReportError(f"cannot read {path}: {error}") from error
    except json.JSONDecodeError as error:
        raise ReportError(f"invalid JSON in {path}:{error.lineno}:{error.colno}: {error.msg}") from error


def read_jsonl(path: Path) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    try:
        for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if not line.strip():
                continue
            try:
                row = json.loads(line)
            except json.JSONDecodeError as error:
                raise ReportError(f"invalid JSONL at {path}:{number}: {error.msg}") from error
            if not isinstance(row, dict):
                raise ReportError(f"JSONL row {number} must be an object")
            reject_sensitive_keys(row, f"{path}:{number}")
            rows.append(row)
    except OSError as error:
        raise ReportError(f"cannot read {path}: {error}") from error
    return rows


def reject_sensitive_keys(value: Any, where: str) -> None:
    if isinstance(value, dict):
        for key, child in value.items():
            lowered = str(key).lower()
            if lowered in {"prompt", "prompt_text", "request_body", "response_body", "authorization", "access_token", "refresh_token", "headers"}:
                raise ReportError(f"{where} contains forbidden raw/secret field {key!r}; provide allowlisted telemetry only")
            reject_sensitive_keys(child, where)
    elif isinstance(value, list):
        for child in value:
            reject_sensitive_keys(child, where)


def sha256(value: Any) -> str:
    raw = (json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False) + "\n").encode()
    return hashlib.sha256(raw).hexdigest()


def ratio(a: int, b: int) -> float | None:
    return a / b if b > 0 else None


def validate_replay(replay: Any) -> list[str]:
    errors: list[str] = []
    if not isinstance(replay, dict):
        return ["replay manifest must be an object"]
    if replay.get("schema") != REPLAY_SCHEMA:
        errors.append(f"schema must be {REPLAY_SCHEMA}")
    for field in ("replay_id", "provider", "model", "effort", "adapter_version", "prompt_version", "tokenizer_id",
                  "cache_namespace_id", "affinity_policy"):
        if not isinstance(replay.get(field), str) or not replay[field].strip():
            errors.append(f"{field} is required and must be nonempty")
    for field in ("replay_id", "cache_namespace_id"):
        value = replay.get(field)
        if isinstance(value, str) and not OPAQUE_ID.fullmatch(value):
            errors.append(f"{field} must be a path/secret-free opaque ID")
    endpoint = replay.get("endpoint")
    if not isinstance(endpoint, dict) or not all(isinstance(endpoint.get(k), str) and endpoint[k] for k in ("host", "path")):
        errors.append("endpoint.host and endpoint.path are required")
    static_prefix = replay.get("static_prefix_sha256")
    if not isinstance(static_prefix, str) or not HEX64.fullmatch(static_prefix):
        errors.append("static_prefix_sha256 must freeze the immutable generic-instructions/schema prefix digest")
    if not isinstance(replay.get("eligibility_rule_id"), str) or not replay["eligibility_rule_id"].strip():
        errors.append("eligibility_rule_id must identify the adapter-specific cache eligibility rule")
    rule_sha = replay.get("eligibility_rule_sha256")
    if not isinstance(rule_sha, str) or not HEX64.fullmatch(rule_sha):
        errors.append("eligibility_rule_sha256 must bind the exact adapter-specific cache eligibility rule")
    if replay.get("affinity_policy") not in {"run_scoped", "shared_verified"}:
        errors.append("affinity_policy must be run_scoped or shared_verified")
    if replay.get("affinity_policy") == "shared_verified" and not HEX64.fullmatch(str(replay.get("shared_affinity_evidence_sha256", ""))):
        errors.append("shared_verified affinity requires shared_affinity_evidence_sha256")
    for field in ("retention_ms", "minimum_eligible_tokens", "block_size_tokens", "appended_input_budget_tokens"):
        value = replay.get(field)
        if not isinstance(value, int) or isinstance(value, bool) or value < 0 or (field in {"retention_ms", "block_size_tokens"} and value == 0):
            errors.append(f"{field} must be a positive/nonnegative integer as appropriate")
    if replay.get("required_eligible_ratio") != 0.95:
        errors.append("required_eligible_ratio must be exactly 0.95")
    if replay.get("required_observed_ratio") != 0.95:
        errors.append("required_observed_ratio must be exactly 0.95")
    requests = replay.get("request_sequence")
    if not isinstance(requests, list) or len(requests) < 3:
        errors.append("request_sequence must include at least two warmup and one designated warm request")
        return errors
    ids: set[str] = set()
    scope_bindings: dict[tuple[str, ...], tuple[str, str]] = {}
    scope_id_bindings: dict[str, tuple[str, ...]] = {}
    thread_scope_bindings: dict[str, tuple[str, ...]] = {}
    invocation_cache_keys: dict[str, str] = {}
    for index, request in enumerate(requests, 1):
        where = f"request_sequence[{index - 1}]"
        if not isinstance(request, dict):
            errors.append(f"{where} must be an object")
            continue
        if request.get("ordinal") != index:
            errors.append(f"{where}.ordinal must be {index}; order is part of the frozen request sequence")
        rid = request.get("request_id")
        if not isinstance(rid, str) or not OPAQUE_ID.fullmatch(rid):
            errors.append(f"{where}.request_id must be a path/secret-free opaque ID")
        elif rid in ids:
            errors.append(f"duplicate request_id {rid}")
        else:
            ids.add(rid)
        for field in ("cache_key", "session_id", "thread_id"):
            value = request.get(field)
            if not isinstance(value, str) or not OPAQUE_ID.fullmatch(value):
                errors.append(f"{where}.{field} must be a path/secret-free opaque ID")
        if request.get("session_id") != request.get("cache_key"):
            errors.append(f"{where}.session_id must equal the persisted Build cache_key")
        if request.get("thread_id") == request.get("cache_key"):
            errors.append(f"{where}.thread_id must remain distinct from the Build cache_key")
        if request.get("conversation_id") != request.get("thread_id"):
            errors.append(f"{where}.conversation_id must equal its stable thread_id")
        if not all(field in request for field in ("attempt_id", "rung_id", "epoch", "thread_scope_id")):
            errors.append(f"{where} must freeze attempt/rung/epoch and thread_scope_id")
        for field in ("attempt_id", "rung_id", "epoch"):
            value = request.get(field)
            if value is not None and (not isinstance(value, str) or not OPAQUE_ID.fullmatch(value)):
                errors.append(f"{where}.{field} must be null or a path/secret-free opaque ID")
        scope_id = request.get("thread_scope_id")
        if not isinstance(scope_id, str) or not OPAQUE_ID.fullmatch(scope_id):
            errors.append(f"{where}.thread_scope_id must be a path/secret-free opaque ID")
        scope_parts = (request.get("invocation_id"), request.get("stage"), request.get("attempt_id"),
                       request.get("rung_id"), request.get("epoch"))
        if all(isinstance(part, str) for part in scope_parts[:2]) and all(part is None or isinstance(part, str) for part in scope_parts[2:]):
            scope_key = tuple("" if part is None else part for part in scope_parts)
            if isinstance(scope_id, str) and isinstance(request.get("thread_id"), str):
                binding = (scope_id, request["thread_id"])
                if scope_key in scope_bindings and scope_bindings[scope_key] != binding:
                    errors.append(f"{where} changed the stable thread within one stage/attempt/rung/epoch scope")
                scope_bindings[scope_key] = binding
                if scope_id in scope_id_bindings and scope_id_bindings[scope_id] != scope_key:
                    errors.append(f"{where} reused thread_scope_id across different stage/attempt/rung/epoch scopes")
                scope_id_bindings[scope_id] = scope_key
                thread_id = request["thread_id"]
                if thread_id in thread_scope_bindings and thread_scope_bindings[thread_id] != scope_key:
                    errors.append(f"{where} reused thread_id across different stage/attempt/rung/epoch scopes")
                thread_scope_bindings[thread_id] = scope_key
        invocation, cache_key = request.get("invocation_id"), request.get("cache_key")
        if isinstance(invocation, str) and isinstance(cache_key, str):
            if invocation in invocation_cache_keys and invocation_cache_keys[invocation] != cache_key:
                errors.append(f"{where} changed cache_key within one Build invocation")
            invocation_cache_keys[invocation] = cache_key
        for field in ("stage", "invocation_id", "conversation_id"):
            if not isinstance(request.get(field), str) or not request[field].strip():
                errors.append(f"{where}.{field} is required to freeze cache/thread scope")
        for field in ("invocation_id", "conversation_id"):
            value = request.get(field)
            if isinstance(value, str) and not OPAQUE_ID.fullmatch(value):
                errors.append(f"{where}.{field} must be a path/secret-free opaque ID")
        for field in ("total_input_tokens", "eligible_input_tokens", "appended_tokens"):
            val = request.get(field)
            if not isinstance(val, int) or isinstance(val, bool) or val < 0:
                errors.append(f"{where}.{field} must be a nonnegative frozen token count")
        if isinstance(request.get("appended_tokens"), int) and not isinstance(request.get("appended_tokens"), bool) and request["appended_tokens"] > replay.get("appended_input_budget_tokens", -1):
            errors.append(f"{where}.appended_tokens exceeds the frozen budget")
        if request.get("designated_warm") is True and index < 3:
            errors.append(f"{where} is designated warm before request 3")
        if request.get("designated_warm") is not True and request.get("designated_warm") is not False:
            errors.append(f"{where}.designated_warm must be boolean")
        total = request.get("total_input_tokens")
        eligible = request.get("eligible_input_tokens")
        if isinstance(total, int) and not isinstance(total, bool) and isinstance(eligible, int) and not isinstance(eligible, bool) and eligible > total:
            errors.append(f"{where}.eligible_input_tokens exceeds total_input_tokens")
    designated = [r for r in requests if isinstance(r, dict) and r.get("designated_warm") is True]
    if not designated:
        errors.append("at least one third-or-later request must be designated_warm")
    for request in designated:
        total = request.get("total_input_tokens")
        eligible = request.get("eligible_input_tokens")
        if isinstance(total, int) and isinstance(eligible, int) and total > 0 and eligible / total < 0.95:
            errors.append(f"designated request {request.get('request_id')} is infeasible: eligible/total={eligible/total:.8f} < 0.95")
        elif total == 0:
            errors.append(f"designated request {request.get('request_id')} has zero total input and cannot meet feasibility")
    return errors


def preflight(args: argparse.Namespace) -> dict[str, Any]:
    replay = read_json(args.replay)
    errors = validate_replay(replay)
    report = {
        "schema": "kogen-cache-preflight-report/v1",
        "replay_id": replay.get("replay_id") if isinstance(replay, dict) else None,
        "replay_manifest_sha256": sha256(replay),
        "eligible_threshold": 0.95,
        "designated_requests": [],
        "prelaunch_feasible": not errors,
        "launch_authorized_by_tool": False,
        "errors": errors,
        "live_requests_made": 0,
    }
    if isinstance(replay, dict):
        for row in replay.get("request_sequence", []) if isinstance(replay.get("request_sequence"), list) else []:
            if isinstance(row, dict) and row.get("designated_warm") is True:
                total, eligible = row.get("total_input_tokens"), row.get("eligible_input_tokens")
                report["designated_requests"].append({
                    "request_id": row.get("request_id"), "ordinal": row.get("ordinal"),
                    "eligible_input_tokens": eligible, "total_input_tokens": total,
                    "eligible_ratio": ratio(eligible, total) if isinstance(eligible, int) and isinstance(total, int) else None,
                })
    emit(report, args.output)
    return report


def usage_values(row: dict[str, Any]) -> dict[str, int | None]:
    usage = row.get("usage")
    if not isinstance(usage, dict):
        return {field: None for field in USAGE_FIELDS}
    result: dict[str, int | None] = {}
    for field in USAGE_FIELDS:
        value = usage.get(field)
        if value is not None and (not isinstance(value, int) or isinstance(value, bool) or value < 0):
            raise ReportError(f"request {row.get('request_id')}: usage.{field} must be null or a nonnegative integer")
        result[field] = value
    return result


def validate_telemetry(row: dict[str, Any]) -> list[str]:
    errors: list[str] = []
    for field in METADATA_FIELDS:
        if field not in row:
            errors.append(f"missing {field}")
    for field in ("request_id", "stage", "provider", "model", "effort", "adapter_version", "prompt_version", "invocation_id",
                  "conversation_id", "cache_namespace_id", "endpoint_host", "endpoint_path", "cache_key", "session_id", "thread_id"):
        if field in row and (not isinstance(row[field], str) or not row[field]):
            errors.append(f"empty/invalid {field}")
    for field in ("request_id", "invocation_id", "conversation_id", "cache_namespace_id", "cache_key", "session_id", "thread_id"):
        value = row.get(field)
        if isinstance(value, str) and not OPAQUE_ID.fullmatch(value):
            errors.append(f"{field} is not a path/secret-free opaque ID")
    for field in ("request_started_ms", "response_ended_ms", "body_bytes"):
        value = row.get(field)
        if not isinstance(value, int) or isinstance(value, bool) or value < 0:
            errors.append(f"invalid {field}")
    if isinstance(row.get("request_started_ms"), int) and isinstance(row.get("response_ended_ms"), int):
        if row["response_ended_ms"] < row["request_started_ms"]:
            errors.append("response_ended_ms precedes request_started_ms")
    digests = row.get("prefix_sha256")
    if not isinstance(digests, list) or not digests or any(not isinstance(value, str) or not HEX64.fullmatch(value) for value in digests):
        errors.append("prefix_sha256 must contain one or more 64-hex static-prefix digests")
    if not isinstance(row.get("static_prefix_sha256"), str) or not HEX64.fullmatch(row.get("static_prefix_sha256", "")):
        errors.append("static_prefix_sha256 must identify the immutable generic-instructions/schema prefix")
    if not isinstance(row.get("eligibility_rule_id"), str) or not row["eligibility_rule_id"]:
        errors.append("eligibility_rule_id must identify the adapter-specific eligibility rule")
    if not isinstance(row.get("eligibility_rule_sha256"), str) or not HEX64.fullmatch(row.get("eligibility_rule_sha256", "")):
        errors.append("eligibility_rule_sha256 must bind the exact eligibility rule")
    eligible = row.get("eligible_input_tokens")
    if eligible is not None and (not isinstance(eligible, int) or isinstance(eligible, bool) or eligible < 0):
        errors.append("eligible_input_tokens must be null or a nonnegative integer")
    headers = row.get("routing_headers")
    if not isinstance(headers, list) or any(not isinstance(v, str) for v in headers):
        errors.append("routing_headers must be a list of names (values are prohibited)")
    usage = usage_values(row)
    if row.get("usage") is None:
        errors.append("usage object is unknown")
    elif any(usage[field] is None for field in USAGE_FIELDS):
        errors.append("one or more nullable usage counters are unknown")
    if isinstance(row.get("body_bytes"), int) and row["body_bytes"] <= 0:
        errors.append("body_bytes must be positive for a measured request")
    if row.get("conversation_id") != row.get("thread_id"):
        errors.append("conversation_id must equal the stable thread_id")
    if row.get("session_id") != row.get("cache_key"):
        errors.append("session_id must equal the persisted Build cache_key")
    if row.get("cache_key") == row.get("thread_id"):
        errors.append("cache_key and thread_id must remain separate identities")
    if isinstance(row.get("cache_key"), str) and not OPAQUE_ID.fullmatch(row["cache_key"]):
        errors.append("cache_key is not a path/secret-free opaque ID")
    if isinstance(row.get("thread_id"), str) and not OPAQUE_ID.fullmatch(row["thread_id"]):
        errors.append("thread_id is not a path/secret-free opaque ID")
    for field in ("attempt_id", "rung_id", "epoch"):
        value = row.get(field)
        if value is not None and (not isinstance(value, str) or not OPAQUE_ID.fullmatch(value)):
            errors.append(f"{field} must be null or a path/secret-free opaque ID")
    if not isinstance(row.get("thread_scope_id"), str) or not OPAQUE_ID.fullmatch(row.get("thread_scope_id", "")):
        errors.append("thread_scope_id is not a path/secret-free opaque ID")
    return errors


def group_key(row: dict[str, Any]) -> tuple[str, ...]:
    return tuple(str(row.get(key) or "") for key in (
        "provider", "model", "adapter_version", "prompt_version", "invocation_id", "conversation_id"
    ))


def summarize(rows: list[dict[str, Any]], replay: dict[str, Any] | None = None) -> dict[str, Any]:
    replay_errors = validate_replay(replay) if replay is not None else []
    expected = {row.get("request_id"): row for row in replay.get("request_sequence", []) if isinstance(row, dict)} if replay and isinstance(replay.get("request_sequence"), list) else {}
    observed: dict[str, dict[str, Any]] = {}
    duplicate_ids: list[str] = []
    for row in rows:
        rid = row.get("request_id")
        if isinstance(rid, str):
            if rid in observed:
                duplicate_ids.append(rid)
            observed[rid] = row

    group_acc: dict[tuple[str, ...], dict[str, Any]] = defaultdict(lambda: {
        "requests": 0, "known_input_requests": 0, "unknown_input_requests": 0,
        "known_cached_requests": 0, "unknown_usage_requests": 0,
        "total_input_tokens": 0, "cached_input_tokens": 0,
        "eligible_tokens": 0, "eligible_cached_tokens": 0, "unknown_eligibility_requests": 0,
        "unknown_eligible_usage_requests": 0, "excess_cached_tokens": 0,
    })
    per_request: list[dict[str, Any]] = []
    telemetry_gaps: list[dict[str, Any]] = []
    eligible_denominator = 0
    eligible_numerator = 0
    total_input = 0
    cached_input = 0
    known_input_requests = 0
    unknown_input_requests = 0
    unknown_usage_requests = 0
    unknown_eligibility_requests = 0
    unknown_eligible_usage_requests = 0
    excess_cached = 0
    qualified = True
    designated_results: list[dict[str, Any]] = []
    identity_conflicts: list[dict[str, Any]] = []
    invocation_keys: dict[tuple[str, str, str], set[str]] = defaultdict(set)
    conversation_bindings: dict[tuple[str, str, str, str], tuple[str, str]] = {}
    thread_bindings: dict[tuple[str, str, str], tuple[str, ...]] = {}
    scope_bindings: dict[tuple[str, ...], tuple[str, str]] = {}
    scope_id_bindings: dict[str, tuple[str, ...]] = {}
    shared_key_bindings: dict[str, dict[str, set[tuple[str, ...]]]] = defaultdict(lambda: defaultdict(set))

    for row in rows:
        rid = row.get("request_id")
        usage = usage_values(row)
        uncached, cached = usage["input"], usage["cached_input"]
        t = uncached + cached if uncached is not None and cached is not None else None
        configured = expected.get(rid) if isinstance(rid, str) else None
        eligibility = configured.get("eligible_input_tokens") if configured else row.get("eligible_input_tokens")
        telem_errors = validate_telemetry(row)
        if configured is not None and replay is not None:
            exact = {
                "stage": configured.get("stage"),
                "provider": replay.get("provider"), "model": replay.get("model"), "effort": replay.get("effort"),
                "adapter_version": replay.get("adapter_version"), "prompt_version": replay.get("prompt_version"),
                "eligibility_rule_id": replay.get("eligibility_rule_id"),
                "eligibility_rule_sha256": replay.get("eligibility_rule_sha256"),
                "cache_namespace_id": replay.get("cache_namespace_id"),
                "cache_key": configured.get("cache_key"), "session_id": configured.get("session_id"),
                "thread_id": configured.get("thread_id"),
                "attempt_id": configured.get("attempt_id"), "rung_id": configured.get("rung_id"),
                "epoch": configured.get("epoch"), "thread_scope_id": configured.get("thread_scope_id"),
                "endpoint_host": (replay.get("endpoint") or {}).get("host"),
                "endpoint_path": (replay.get("endpoint") or {}).get("path"),
                "invocation_id": configured.get("invocation_id"), "conversation_id": configured.get("conversation_id"),
            }
            for field, value in exact.items():
                exact_nullable = field in {"attempt_id", "rung_id", "epoch"}
                if (value is not None or exact_nullable) and row.get(field) != value:
                    telem_errors.append(f"{field} does not match frozen replay")
            if row.get("static_prefix_sha256") != replay.get("static_prefix_sha256"):
                telem_errors.append("static_prefix_sha256 does not match the frozen cross-session static prefix")
            if isinstance(configured.get("total_input_tokens"), int) and t is not None and configured["total_input_tokens"] != t:
                telem_errors.append("reported total input differs from the frozen tokenizer count")
        if replay is not None:
            provider = str(row.get("provider") or "")
            namespace = str(row.get("cache_namespace_id") or "")
            invocation = str(row.get("invocation_id") or "")
            conversation = str(row.get("conversation_id") or "")
            cache_key = str(row.get("cache_key") or "")
            thread_id = str(row.get("thread_id") or "")
            scope_key = tuple(str(row.get(key) or "") for key in
                              ("invocation_id", "stage", "attempt_id", "rung_id", "epoch"))
            scope_id = str(row.get("thread_scope_id") or "")
            invocation_key = (provider, namespace, invocation)
            invocation_keys[invocation_key].add(cache_key)
            conv_key = (provider, namespace, invocation, conversation)
            binding = (cache_key, thread_id)
            if conv_key in conversation_bindings and conversation_bindings[conv_key] != binding:
                identity_conflicts.append({"request_id": rid, "reason": "cache_key/thread_id changed within one conversation"})
            conversation_bindings[conv_key] = binding
            thread_key = (provider, namespace, thread_id)
            owner = (*scope_key, scope_id)
            if thread_key in thread_bindings and thread_bindings[thread_key] != owner:
                identity_conflicts.append({"request_id": rid, "reason": "one thread_id was reused across stage/attempt/rung/epoch scopes"})
            thread_bindings[thread_key] = owner
            binding = (scope_id, thread_id)
            if scope_key in scope_bindings and scope_bindings[scope_key] != binding:
                identity_conflicts.append({"request_id": rid, "reason": "one stage/attempt/rung/epoch scope changed thread identity"})
            scope_bindings[scope_key] = binding
            if scope_id in scope_id_bindings and scope_id_bindings[scope_id] != scope_key:
                identity_conflicts.append({"request_id": rid, "reason": "thread_scope_id was reused for a different stage/attempt/rung/epoch"})
            scope_id_bindings[scope_id] = scope_key
            static_identity = (provider, str(row.get("model") or ""), str(row.get("adapter_version") or ""),
                               str(row.get("prompt_version") or ""), namespace,
                               str(row.get("static_prefix_sha256") or ""))
            shared_key_bindings[cache_key][invocation].add(static_identity)
        if telem_errors:
            qualified = False
            telemetry_gaps.append({"request_id": rid, "reasons": sorted(set(telem_errors))})
        if t is None:
            unknown_input_requests += 1
        else:
            total_input += t
            known_input_requests += 1
        if cached is not None:
            cached_input += cached
        group = group_acc[group_key(row)]
        group["requests"] += 1
        if any(usage[field] is None for field in USAGE_FIELDS):
            unknown_usage_requests += 1
            group["unknown_usage_requests"] += 1
        if t is None:
            group["unknown_input_requests"] += 1
        else:
            group["known_input_requests"] += 1
            group["total_input_tokens"] += t
        if cached is not None:
            group["known_cached_requests"] += 1
            group["cached_input_tokens"] += cached

        eligible_reuse = None
        excess = None
        if isinstance(eligibility, int) and not isinstance(eligibility, bool) and eligibility > 0:
            if cached is None:
                unknown_eligible_usage_requests += 1
                group["unknown_eligible_usage_requests"] += 1
            else:
                eligible_denominator += eligibility
                eligible_reuse_tokens = min(cached, eligibility)
                eligible_numerator += eligible_reuse_tokens
                eligible_reuse = eligible_reuse_tokens / eligibility
                excess = max(0, cached - eligibility)
                excess_cached += excess
                group["eligible_tokens"] += eligibility
                group["eligible_cached_tokens"] += eligible_reuse_tokens
                group["excess_cached_tokens"] += excess
        elif eligibility == 0 and not isinstance(eligibility, bool):
            eligible_reuse = None
        else:
            unknown_eligibility_requests += 1
            group["unknown_eligibility_requests"] += 1

        result = {
            "request_id": rid,
            "ordinal": configured.get("ordinal") if configured else row.get("ordinal"),
            "designated_warm": bool(configured and configured.get("designated_warm")),
            "input_uncached_tokens": uncached,
            "cached_input_tokens": cached,
            "total_input_tokens": t,
            "eligible_input_tokens": eligibility,
            "raw_hit_rate": ratio(cached, t) if cached is not None and t is not None else None,
            "eligible_prefix_reuse": eligible_reuse,
            "excess_cached_tokens": excess,
            "telemetry_complete": not telem_errors,
            "telemetry_gaps": sorted(set(telem_errors)),
        }
        per_request.append(result)
        if configured and configured.get("designated_warm"):
            observed_ratio = ratio(cached, t) if cached is not None and t is not None else None
            request_pass = not telem_errors and observed_ratio is not None and observed_ratio >= 0.95
            if not request_pass:
                qualified = False
            designated_results.append({
                "request_id": rid, "total_input_tokens": t, "cached_input_tokens": cached,
                "observed_ratio": observed_ratio, "complete_telemetry": not telem_errors,
                "passes_95_percent": request_pass,
            })
    missing_request_ids = sorted(set(expected) - set(observed)) if replay is not None else []
    if missing_request_ids:
        qualified = False
        for rid in missing_request_ids:
            config = expected[rid]
            if config.get("designated_warm"):
                designated_results.append({"request_id": rid, "observed_ratio": None,
                                           "complete_telemetry": False, "passes_95_percent": False,
                                           "status": "not_observed"})
    unexpected_ids = sorted(set(observed) - set(expected)) if replay is not None else []
    if replay is not None:
        by_namespace: dict[tuple[str, str], dict[str, set[str]]] = defaultdict(lambda: defaultdict(set))
        for (provider, namespace, invocation), cache_keys in invocation_keys.items():
            for cache_key in cache_keys:
                by_namespace[(provider, namespace)][invocation].add(cache_key)
        for (provider, namespace), invocations in by_namespace.items():
            for invocation, keys in invocations.items():
                if len(keys) != 1:
                    identity_conflicts.append({"provider": provider, "namespace": namespace,
                                               "invocation_id": invocation,
                                               "reason": "cache_key changed within one invocation/Build"})
            if replay.get("affinity_policy") == "run_scoped" and len(invocations) > 1:
                seen_by_invocation: dict[str, set[str]] = invocations
                for left_index, left in enumerate(sorted(seen_by_invocation)):
                    for right in sorted(seen_by_invocation)[left_index + 1:]:
                        if seen_by_invocation[left] & seen_by_invocation[right]:
                            identity_conflicts.append({"provider": provider, "namespace": namespace,
                                                       "invocations": [left, right],
                                                       "reason": "run_scoped affinity reused a cache_key across invocations"})
            if replay.get("affinity_policy") == "shared_verified":
                for cache_key, bindings in shared_key_bindings.items():
                    if len(bindings) > 1:
                        binding_sets = [bindings[invocation] for invocation in sorted(bindings)]
                        if any(binding_set != binding_sets[0] for binding_set in binding_sets[1:]):
                            identity_conflicts.append({"cache_key": cache_key,
                                                       "reason": "shared affinity crossed provider/model/adapter/prompt/security/static-prefix boundaries"})
        if identity_conflicts:
            qualified = False
    if duplicate_ids or replay_errors or unexpected_ids:
        qualified = False
    if replay is None:
        qualified = False

    group_rows = []
    for key, value in sorted(group_acc.items()):
        raw_rate = ratio(value["cached_input_tokens"], value["total_input_tokens"])
        eligible_rate = ratio(value["eligible_cached_tokens"], value["eligible_tokens"])
        group_rows.append({
            "provider": key[0], "model": key[1], "adapter_version": key[2], "prompt_version": key[3],
            "invocation_id": key[4], "conversation_id": key[5], **value,
            "raw_hit_rate": raw_rate,
            "raw_rate_completeness": "complete" if value["unknown_input_requests"] == 0 else "partial",
            "eligible_prefix_reuse": eligible_rate,
            "eligibility_completeness": "partial" if value["unknown_eligibility_requests"] or value["unknown_eligible_usage_requests"]
                                        else ("complete" if value["eligible_tokens"] else "inapplicable"),
        })
    if replay is None:
        qualification = "descriptive_no_universal_gate"
    elif replay_errors or telemetry_gaps or missing_request_ids or unexpected_ids or duplicate_ids or identity_conflicts:
        qualification = "incomplete"
    elif any(not item.get("passes_95_percent") for item in designated_results):
        qualification = "observed_fail"
    else:
        qualification = "qualified"
    return {
        "schema": "kogen-cache-report/v1",
        "replay_id": replay.get("replay_id") if replay else None,
        "replay_manifest_sha256": sha256(replay) if replay else None,
        "request_count": len(rows),
        "raw_weighted": {
            "cached_input_tokens": cached_input,
            "total_input_tokens": total_input,
            "hit_rate": ratio(cached_input, total_input),
            "known_input_requests": known_input_requests,
            "unknown_input_requests": unknown_input_requests,
            "completeness": "complete" if unknown_input_requests == 0 else ("partial" if known_input_requests else "unknown"),
        },
        "eligible_prefix_weighted": {
            "reused_eligible_tokens": eligible_numerator,
            "eligible_input_tokens": eligible_denominator,
            "reuse_rate": ratio(eligible_numerator, eligible_denominator),
            "unknown_eligibility_requests": unknown_eligibility_requests,
            "unknown_eligible_usage_requests": unknown_eligible_usage_requests,
            "excess_cached_tokens": excess_cached,
            "completeness": "partial" if unknown_eligibility_requests or unknown_eligible_usage_requests
                           else ("complete" if eligible_denominator else "inapplicable"),
        },
        "designated_warm": {
            "threshold": 0.95,
            "requests": designated_results,
            "passes": bool(designated_results) and all(x.get("passes_95_percent") for x in designated_results),
            "qualified": bool(replay is not None and not replay_errors and designated_results and
                              all(x.get("passes_95_percent") for x in designated_results)),
        },
        "qualification": qualification,
        "replay_manifest_errors": replay_errors,
        "duplicate_request_ids": sorted(set(duplicate_ids)),
        "missing_request_ids": missing_request_ids,
        "unexpected_request_ids": unexpected_ids,
        "identity_conflicts": identity_conflicts,
        "telemetry_gaps": telemetry_gaps,
        "requests": per_request,
        "groups": group_rows,
        "live_requests_made_by_tool": 0,
    }


def emit(value: dict[str, Any], output: Path | None) -> None:
    raw = json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2, allow_nan=False) + "\n"
    if output is None:
        print(raw, end="")
        return
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


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    pre = sub.add_parser("preflight", help="refuse an infeasible warm replay before any launch")
    pre.add_argument("--replay", type=Path, required=True)
    pre.add_argument("--output", type=Path)
    report = sub.add_parser("report", help="report redacted request telemetry; never contacts a provider")
    report.add_argument("--requests", type=Path, required=True)
    report.add_argument("--replay", type=Path, help="frozen replay definition; omit for arbitrary-task diagnostics")
    report.add_argument("--output", type=Path)
    args = parser.parse_args()
    try:
        if args.command == "preflight":
            result = preflight(args)
            return 0 if result["prelaunch_feasible"] else 2
        rows = read_jsonl(args.requests)
        replay = read_json(args.replay) if args.replay else None
        if replay is not None:
            preflight_errors = validate_replay(replay)
            if preflight_errors:
                result = summarize(rows, replay)
                emit(result, args.output)
                return 2
        result = summarize(rows, replay)
        emit(result, args.output)
        return 0 if result["qualification"] == "qualified" or replay is None else 1
    except (ReportError, OSError, TypeError, ValueError) as error:
        print(f"cache-report: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
