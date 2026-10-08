#!/usr/bin/env python3
"""Collect offline Kogen timing and resource measurements for Go, Rust and Bun.

The tool reads source repositories and builds in disposable shared-object clones.
It never edits the source checkouts and only uses the frozen conformance runner
for the status and optional fake-provider pipeline workloads.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


SCHEMA_VERSION = 1
PROFILE = "cli,state,approval,shape,build,ladder,provider,custody,format,v1.2"
STATUS_CASE = "state-30"
LANGUAGES = ("go", "rust", "bun")
ROOT = Path(__file__).resolve().parents[1]


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def sha256_tree(path: Path) -> str | None:
    """Hash the immutable spec inputs, excluding Git metadata and caches."""
    if not path.is_dir():
        return None
    digest = hashlib.sha256()
    for item in sorted(path.rglob("*")):
        if (
            not item.is_file()
            or ".git" in item.parts
            or "__pycache__" in item.parts
            or item.suffix == ".pyc"
        ):
            continue
        rel = item.relative_to(path).as_posix().encode()
        digest.update(len(rel).to_bytes(4, "big"))
        digest.update(rel)
        digest.update(bytes.fromhex(sha256_file(item)))
    return digest.hexdigest()


def git(root: Path, *args: str, check: bool = True) -> str:
    result = subprocess.run(
        ["git", "-C", str(root), *args],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env=offline_env(),
        check=False,
    )
    if check and result.returncode:
        raise RuntimeError(f"git {' '.join(args)} failed in {root}: {result.stderr.strip()}")
    return result.stdout.strip()


def offline_env(base: dict[str, str] | None = None) -> dict[str, str]:
    env = dict(os.environ if base is None else base)
    env.update(
        {
            "GIT_CONFIG_GLOBAL": "/dev/null",
            "GIT_CONFIG_SYSTEM": "/dev/null",
            "GIT_TERMINAL_PROMPT": "0",
            "MISE_AUTO_INSTALL": "0",
            "GOTOOLCHAIN": "local",
            "GOWORK": "off",
            "GOPROXY": "off",
            "GOSUMDB": "off",
            "GOMAXPROCS": "2",
            "CARGO_NET_OFFLINE": "true",
            "RUSTUP_AUTO_INSTALL": "0",
            "PYTHONDONTWRITEBYTECODE": "1",
            "NO_PROXY": "localhost,127.0.0.1,::1",
            "no_proxy": "localhost,127.0.0.1,::1",
        }
    )
    for key in list(env):
        upper = key.upper()
        if any(part in upper for part in ("API_KEY", "ACCESS_TOKEN", "REFRESH_TOKEN", "CLIENT_SECRET")):
            env.pop(key, None)
    return env


def tool_version(name: str, argv: list[str], cwd: Path | None = None) -> dict[str, Any]:
    executable = shutil.which(name)
    if not executable:
        return {"path": None, "version": None, "available": False}
    return tool_version_path(Path(executable), argv, cwd)


def tool_version_path(path: Path, argv: list[str], cwd: Path | None = None) -> dict[str, Any]:
    # Keep rustup/mise shim basenames intact; their dispatch depends on argv[0].
    executable = path.expanduser().absolute()
    if not executable.is_file():
        return {"path": str(executable), "version": None, "available": False}
    result = subprocess.run(
        [str(executable), *argv],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        cwd=cwd,
        env=offline_env(),
        check=False,
    )
    return {
        "path": str(executable),
        "version": result.stdout.strip().splitlines()[0] if result.stdout.strip() else None,
        "available": result.returncode == 0,
    }


def host_manifest(bun_root: Path, rust_root: Path) -> dict[str, Any]:
    system = platform.system()
    release = platform.release()
    version = platform.mac_ver()[0] if system == "Darwin" else platform.version()
    try:
        if system == "Darwin":
            memory = int(
                subprocess.run(
                    ["/usr/sbin/sysctl", "-n", "hw.memsize"],
                    check=True,
                    capture_output=True,
                    text=True,
                ).stdout.strip()
            )
        else:
            memory = int(os.sysconf("SC_PHYS_PAGES") * os.sysconf("SC_PAGE_SIZE"))
    except (OSError, ValueError, subprocess.CalledProcessError, AttributeError):
        memory = None
    bun_lock = json.loads((bun_root / "toolchain.lock.json").read_text(encoding="utf-8"))
    bun_binary = Path(bun_lock["tools"]["bun"]["resolved_binary"])
    node_binary = Path(bun_lock["tools"]["node"]["resolved_binary"])
    return {
        "os": system,
        "os_release": release,
        "os_version": version,
        "architecture": platform.machine(),
        "logical_cpus": os.cpu_count(),
        "memory_bytes": memory,
        "python": tool_version(sys.executable, ["--version"]),
        "git": tool_version("git", ["--version"]),
        "go": tool_version("go", ["version"]),
        "rustc": tool_version("rustc", ["--version"], rust_root),
        "cargo": tool_version("cargo", ["--version"], rust_root),
        "bun": {**tool_version_path(bun_binary, ["--version"]), "binary_sha256": sha256_file(bun_binary)},
        "node": {**tool_version_path(node_binary, ["--version"]), "binary_sha256": sha256_file(node_binary)},
        "c_compiler": tool_version("cc", ["--version"]),
    }


def source_record(name: str, root: Path) -> dict[str, Any]:
    root = root.expanduser().resolve()
    if not root.is_dir():
        raise RuntimeError(f"{name} source root does not exist: {root}")
    revision = git(root, "rev-parse", "HEAD")
    dirty = git(root, "status", "--porcelain", "--untracked-files=all")
    dirty_paths = [line[3:] for line in dirty.splitlines() if len(line) >= 4]
    auxiliary_paths = {
        "tools/perf-offline.py",
        "docs/work/PERFORMANCE.md",
        "docs/work/71-offline-performance-collector.evidence.md",
        "docs/work/71-offline-performance-collector.gate.json",
    }
    application_changes = [path for path in dirty_paths if path not in auxiliary_paths]
    if application_changes:
        raise RuntimeError(
            f"{name} source root has uncommitted application changes; refusing an unbound measurement: "
            + ", ".join(application_changes)
        )
    if name == "go":
        text = (root / "go.mod").read_text(encoding="utf-8")
        match = re.search(r"(?m)^go\s+(\S+)", text)
        toolchain = match.group(1) if match else None
        build = ["make", "build"]
        binary = "bin/kogen"
    elif name == "rust":
        toolchain_file = root / "rust-toolchain.toml"
        text = toolchain_file.read_text(encoding="utf-8") if toolchain_file.exists() else ""
        match = re.search(r'(?m)^\s*(?:toolchain|channel)\s*=\s*"([^"]+)"', text)
        toolchain = match.group(1) if match else None
        build = ["cargo", "build", "--release", "--locked", "--offline", "-p", "kogen"]
        binary = "target/release/kogen"
    else:
        text = (root / "package.json").read_text(encoding="utf-8")
        match = re.search(r'"packageManager"\s*:\s*"bun@([^"]+)"', text)
        toolchain = match.group(1) if match else None
        build = ["make", "build-foundation"]
        binary = "dist/kogen"
    return {
        "language": name,
        "source_root": str(root),
        "revision": revision,
        "source_tree": git(root, "rev-parse", f"{revision}^{{tree}}"),
        "clean": not dirty_paths,
        "auxiliary_uncommitted_paths": dirty_paths,
        "toolchain_pin": toolchain,
        "build_argv": build,
        "check_argv": ["make", "check"],
        "binary_relative": binary,
    }


def write_manifest(args: argparse.Namespace) -> None:
    suite = args.suite.expanduser().resolve()
    spec = args.spec.expanduser().resolve()
    if not (suite / "bin/kogen-conformance").is_file():
        raise RuntimeError(f"frozen conformance runner is missing from {suite}")
    suite_source = args.suite_source.expanduser().resolve()
    suite_revision = git(suite_source, "rev-parse", "HEAD")
    suite_tree = git(suite_source, "rev-parse", "HEAD^{tree}")
    manifest = {
        "schema_version": SCHEMA_VERSION,
        "created_at": datetime.now(timezone.utc).isoformat(),
        "host": host_manifest(args.bun_root.expanduser().resolve(), args.rust_root.expanduser().resolve()),
        "spec": {
            "revision": git(spec, "rev-parse", "HEAD"),
            "tree_sha256": sha256_tree(spec),
            "clean": not bool(git(spec, "status", "--porcelain", "--untracked-files=all")),
            "working_tree_paths": git(spec, "status", "--porcelain", "--untracked-files=all").splitlines(),
            "path": str(spec),
        },
        "suite": {
            "revision": suite_revision,
            "source_tree": suite_tree,
            "source_clean": not bool(git(suite_source, "status", "--porcelain", "--untracked-files=all")),
            "version": (suite / "VERSION").read_text(encoding="utf-8").strip(),
            "runner_sha256": sha256_file(suite / "bin/kogen-conformance"),
            "input_tree_sha256": sha256_tree(suite),
            "path": str(suite),
            "source_path": str(suite_source),
        },
        "status_workload": {"case_id": STATUS_CASE, "intents": 50, "run_records": 200},
        "languages": [
            source_record("go", args.go_root),
            source_record("rust", args.rust_root),
            source_record("bun", args.bun_root),
        ],
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(args.output)


def clone_source(source: Path, revision: str, destination: Path, language: str) -> None:
    destination.parent.mkdir(parents=True, exist_ok=True)
    result = subprocess.run(
        ["git", "clone", "--quiet", "--shared", "--no-checkout", str(source), str(destination)],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env=offline_env(),
        check=False,
    )
    if result.returncode:
        raise RuntimeError(f"could not create isolated {language} clone: {result.stderr.strip()}")
    result = subprocess.run(
        ["git", "-C", str(destination), "checkout", "--quiet", "--detach", revision],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env=offline_env(),
        check=False,
    )
    if result.returncode:
        raise RuntimeError(f"could not check out {language} revision: {result.stderr.strip()}")
    if language == "bun":
        modules = source / "node_modules"
        if not modules.is_dir():
            raise RuntimeError("Bun source checkout has no provisioned node_modules")
        os.symlink(modules, destination / "node_modules", target_is_directory=True)


def rusage_rss_bytes(value: int) -> int:
    # Darwin reports bytes; Linux and the other supported Unix hosts report KiB.
    return int(value if platform.system() == "Darwin" else value * 1024)


def process_measurement(
    argv: list[str], cwd: Path, env: dict[str, str], timeout_s: float, stdout_path: Path, stderr_path: Path
) -> dict[str, Any]:
    import resource

    stdout_path.parent.mkdir(parents=True, exist_ok=True)
    started = time.monotonic_ns()
    wall_start = datetime.now(timezone.utc).isoformat()
    with stdout_path.open("wb") as out, stderr_path.open("wb") as err:
        process = subprocess.Popen(
            argv,
            cwd=cwd,
            env=env,
            stdin=subprocess.DEVNULL,
            stdout=out,
            stderr=err,
            start_new_session=True,
            close_fds=True,
        )
        timed_out = False
        deadline = time.monotonic() + timeout_s
        status = None
        usage = None
        while True:
            waited, maybe_status, maybe_usage = os.wait4(process.pid, os.WNOHANG)
            if waited:
                status, usage = maybe_status, maybe_usage
                break
            if time.monotonic() >= deadline:
                timed_out = True
                try:
                    os.killpg(process.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                grace_end = time.monotonic() + 2.0
                while time.monotonic() < grace_end:
                    waited, maybe_status, maybe_usage = os.wait4(process.pid, os.WNOHANG)
                    if waited:
                        status, usage = maybe_status, maybe_usage
                        break
                    time.sleep(0.02)
                if status is None:
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    try:
                        _, status, usage = os.wait4(process.pid, 0)
                    except ChildProcessError:
                        status, usage = 0, None
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                break
            time.sleep(0.01)
        process.returncode = os.waitstatus_to_exitcode(status or 0)
    ended = time.monotonic_ns()
    user_ms = round(usage.ru_utime * 1000, 3) if usage is not None else None
    system_ms = round(usage.ru_stime * 1000, 3) if usage is not None else None
    peak_rss = rusage_rss_bytes(usage.ru_maxrss) if usage is not None else None
    return {
        "started_at": wall_start,
        "wall_ms": round((ended - started) / 1_000_000, 3),
        "cpu_user_ms": user_ms,
        "cpu_system_ms": system_ms,
        "peak_rss_bytes": peak_rss,
        "resource_method": "wait4-command-process",
        "timed_out": timed_out,
        "exit_code": process.returncode,
        "stdout_sha256": sha256_file(stdout_path),
        "stderr_sha256": sha256_file(stderr_path),
    }


def measurement_row(
    manifest: dict[str, Any], language: dict[str, Any], phase: str, cache_state: str, **fields: Any
) -> dict[str, Any]:
    return {
        "schema_version": SCHEMA_VERSION,
        "language": language["language"],
        "source_revision": language["revision"],
        "toolchain_pin": language.get("toolchain_pin"),
        "host": manifest["host"],
        "spec_revision": manifest["spec"]["revision"],
        "suite_revision": manifest["suite"]["revision"],
        "phase": phase,
        "cache_state": cache_state,
        "workload": manifest["status_workload"] if phase == "status" else None,
        "provider_mode": "fake" if phase == "pipeline" else "none",
        "provider_requests": None,
        "binary_sha256": None,
        "binary_bytes": None,
        "status": "measured",
        "wall_ms": None,
        "cpu_user_ms": None,
        "cpu_system_ms": None,
        "peak_rss_bytes": None,
        "resource_method": None,
        "exit_code": None,
        "stdout_sha256": None,
        "stderr_sha256": None,
        **fields,
    }


def suite_command(suite: Path, binary: Path, case_id: str, work: Path, out: Path, jobs: int) -> list[str]:
    return [
        str(suite / "bin/kogen-conformance"),
        "run",
        "--kogen",
        str(binary),
        "--profile",
        PROFILE,
        "--case",
        case_id,
        "--jobs",
        str(jobs),
        "--time-scale",
        "0.02",
        "--workdir",
        str(work),
        "--out",
        str(out),
    ]


def find_case(suite: Path, case_id: str) -> dict[str, Any] | None:
    for path in (suite / "cases").glob("*/*.json"):
        try:
            case = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            continue
        if case.get("id") == case_id:
            return case
    return None


def write_measuring_shim(path: Path, binary: Path, metrics: Path) -> None:
    """Wrap each public CLI call while forwarding its output bytes unchanged."""
    path.parent.mkdir(parents=True, exist_ok=True)
    shim = f'''#!{sys.executable}
import hashlib, json, os, shutil, subprocess, sys, tempfile, time
import resource
binary = {str(binary)!r}
metrics = {str(metrics)!r}
started = time.monotonic_ns()
with tempfile.TemporaryFile() as out, tempfile.TemporaryFile() as err:
    child = subprocess.Popen([binary, *sys.argv[1:]], stdin=sys.stdin.buffer,
                             stdout=out, stderr=err, close_fds=True)
    pid, status, usage = os.wait4(child.pid, 0)
    code = os.waitstatus_to_exitcode(status)
    out.seek(0); out_hash = hashlib.sha256(); out_bytes = 0
    while True:
        chunk = out.read(65536)
        if not chunk: break
        out_hash.update(chunk); out_bytes += len(chunk); sys.stdout.buffer.write(chunk)
    sys.stdout.buffer.flush()
    err.seek(0); err_hash = hashlib.sha256(); err_bytes = 0
    while True:
        chunk = err.read(65536)
        if not chunk: break
        err_hash.update(chunk); err_bytes += len(chunk); sys.stderr.buffer.write(chunk)
    sys.stderr.buffer.flush()
row = {{"wall_ms": round((time.monotonic_ns() - started) / 1_000_000, 3),
       "cpu_user_ms": round(usage.ru_utime * 1000, 3),
       "cpu_system_ms": round(usage.ru_stime * 1000, 3),
       "peak_rss_bytes": int(usage.ru_maxrss if sys.platform == "darwin" else usage.ru_maxrss * 1024),
       "exit_code": code, "stdout_sha256": out_hash.hexdigest(),
       "stderr_sha256": err_hash.hexdigest(), "stdout_bytes": out_bytes,
       "stderr_bytes": err_bytes}}
os.makedirs(os.path.dirname(metrics), exist_ok=True)
with open(metrics, "a", encoding="utf-8") as stream:
    stream.write(json.dumps(row, separators=(",", ":")) + "\\n")
sys.exit(code)
'''
    path.write_text(shim, encoding="utf-8")
    path.chmod(0o700)


def shim_summary(path: Path) -> dict[str, Any]:
    rows = []
    if path.is_file():
        for line in path.read_text(encoding="utf-8").splitlines():
            if line.strip():
                rows.append(json.loads(line))
    return {
        "cli_invocations": len(rows),
        "cli_wall_ms_sum": round(sum(row["wall_ms"] for row in rows), 3),
        "cli_wall_ms_mean": round(sum(row["wall_ms"] for row in rows) / len(rows), 3) if rows else None,
        "cpu_user_ms": round(sum(row["cpu_user_ms"] for row in rows), 3),
        "cpu_system_ms": round(sum(row["cpu_system_ms"] for row in rows), 3),
        "peak_rss_bytes": max((row["peak_rss_bytes"] for row in rows), default=None),
        "cli_exit_codes": [row["exit_code"] for row in rows],
        "public_stdout_sha256": hashlib.sha256(
            json.dumps([row["stdout_sha256"] for row in rows], separators=(",", ":")).encode()
        ).hexdigest(),
        "public_stderr_sha256": hashlib.sha256(
            json.dumps([row["stderr_sha256"] for row in rows], separators=(",", ":")).encode()
        ).hexdigest(),
        "public_stdout_bytes": sum(row["stdout_bytes"] for row in rows),
        "public_stderr_bytes": sum(row["stderr_bytes"] for row in rows),
        "resource_method": "wait4-per-public-kogen-invocation; RSS is max individual process",
    }


def case_result(path: Path, case_id: str) -> dict[str, Any] | None:
    if not path.is_file():
        return None
    for line in path.read_text(encoding="utf-8").splitlines():
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            continue
        if row.get("id") == case_id:
            return row
    return None


def arm_env(language: str, scratch: Path, checkout: Path, manifest: dict[str, Any]) -> dict[str, str]:
    env = offline_env()
    env["TMPDIR"] = str(scratch / "tmp")
    (scratch / "tmp").mkdir(parents=True, exist_ok=True)
    if language == "go":
        env["GOCACHE"] = str(scratch / "gocache")
        env["GOMAXPROCS"] = "2"
        env["GOFLAGS"] = "-mod=vendor -p=2"
    elif language == "rust":
        env["CARGO_TARGET_DIR"] = str(checkout / "target")
        env["CARGO_BUILD_JOBS"] = "2"
    else:
        env["BUN_INSTALL_CACHE_DIR"] = str(scratch / "bun-cache")
        env["BUN"] = manifest["host"]["bun"]["path"]
        # The detached clone is a new mise trust root. Use the exact locked
        # executable paths already in the manifest instead of consulting mise.
        env["MISE"] = ""
        env["GIT_TOOL"] = manifest["host"]["git"]["path"]
        env["KTS_CHECK_GIT"] = manifest["host"]["git"]["path"]
    return env


def emit_row(stream: Any, row: dict[str, Any]) -> None:
    stream.write(json.dumps(row, sort_keys=True, separators=(",", ":")) + "\n")
    stream.flush()


def collect(args: argparse.Namespace) -> int:
    manifest = json.loads(args.manifest.read_text(encoding="utf-8"))
    if manifest.get("schema_version") != SCHEMA_VERSION:
        raise RuntimeError("unsupported manifest schema version")
    languages = {arm["language"]: arm for arm in manifest.get("languages", [])}
    if tuple(sorted(languages)) != tuple(sorted(LANGUAGES)):
        raise RuntimeError("manifest must contain exactly Go, Rust and Bun arms")
    suite = Path(manifest["suite"]["path"])
    spec = Path(manifest["spec"]["path"])
    if sha256_tree(suite) != manifest["suite"]["input_tree_sha256"]:
        raise RuntimeError("frozen suite input tree changed after the manifest snapshot")
    if sha256_file(suite / "bin/kogen-conformance") != manifest["suite"]["runner_sha256"]:
        raise RuntimeError("frozen suite runner changed after the manifest snapshot")
    if git(Path(manifest["suite"]["source_path"]), "rev-parse", "HEAD") != manifest["suite"]["revision"]:
        raise RuntimeError("canonical frozen suite revision changed after the manifest snapshot")
    if (
        git(spec, "rev-parse", "HEAD") != manifest["spec"]["revision"]
        or sha256_tree(spec) != manifest["spec"]["tree_sha256"]
    ):
        raise RuntimeError("target spec revision/content changed after the manifest snapshot")
    for arm in languages.values():
        source = Path(arm["source_root"])
        if git(source, "rev-parse", f"{arm['revision']}^{{tree}}") != arm["source_tree"]:
            raise RuntimeError(f"{arm['language']} source tree is unavailable or changed")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    work_root = args.work_root.resolve() if args.work_root else Path(tempfile.mkdtemp(prefix="kogen-perf-offline-"))
    work_root.mkdir(parents=True, exist_ok=True)
    all_ok = True
    with args.output.open("w", encoding="utf-8") as stream:
        emit_row(
            stream,
            {
                "schema_version": SCHEMA_VERSION,
                "record_type": "manifest",
                "collector_work_root": str(work_root),
                **manifest,
            },
        )
        for language_name in LANGUAGES:
            arm = languages[language_name]
            source = Path(arm["source_root"])
            language_work = work_root / language_name
            compile_root = language_work / "compile-source"
            check_root = language_work / "check-source"
            try:
                clone_source(source, arm["revision"], compile_root, language_name)
                clone_source(source, arm["revision"], check_root, language_name)
            except Exception as error:  # produce a record for each arm even if setup is unavailable
                all_ok = False
                for phase in ("compile", "check", "status", "pipeline"):
                    emit_row(
                        stream,
                        measurement_row(
                            manifest,
                            arm,
                            phase,
                            "cold",
                            status="unavailable",
                            error=str(error),
                        ),
                    )
                continue

            for phase, clone, command in (
                ("compile", compile_root, arm["build_argv"]),
                ("check", check_root, arm["check_argv"]),
            ):
                env = arm_env(language_name, language_work / phase, clone, manifest)
                for cache_state in ("cold", "warm"):
                    stdout = language_work / "logs" / f"{phase}-{cache_state}.stdout"
                    stderr = language_work / "logs" / f"{phase}-{cache_state}.stderr"
                    try:
                        result = process_measurement(
                            command,
                            clone,
                            env,
                            args.command_timeout,
                            stdout,
                            stderr,
                        )
                        row = measurement_row(manifest, arm, phase, cache_state, **result)
                        if result["exit_code"] != 0 or result["timed_out"]:
                            row["status"] = "failed"
                            all_ok = False
                        else:
                            row["status"] = "passed"
                    except (OSError, RuntimeError) as error:
                        row = measurement_row(
                            manifest, arm, phase, cache_state, status="unavailable", error=str(error)
                        )
                        all_ok = False
                    emit_row(stream, row)

            binary = compile_root / arm["binary_relative"]
            binary_hash = sha256_file(binary) if binary.is_file() else None
            binary_size = binary.stat().st_size if binary.is_file() else None
            status_case_id = manifest["status_workload"]["case_id"]
            for repetition in range(args.repetitions):
                status_work = language_work / "status" / f"run-{repetition + 1}"
                result_path = language_work / "status" / f"run-{repetition + 1}.jsonl"
                result_path.parent.mkdir(parents=True, exist_ok=True)
                if binary_hash is None:
                    result = {"status": "unavailable", "error": "built CLI binary is missing"}
                else:
                    metrics_path = language_work / "status" / f"run-{repetition + 1}.cli-metrics.jsonl"
                    shim = language_work / "status" / f"run-{repetition + 1}.bin" / "kogen"
                    write_measuring_shim(shim, binary, metrics_path)
                    stdout = language_work / "logs" / f"status-{repetition + 1}.stdout"
                    stderr = language_work / "logs" / f"status-{repetition + 1}.stderr"
                    try:
                        result = process_measurement(
                            suite_command(suite, shim, status_case_id, status_work, result_path, args.jobs),
                            suite,
                            offline_env(),
                            args.command_timeout,
                            stdout,
                            stderr,
                        )
                        detail = case_result(result_path, status_case_id)
                        if detail is None:
                            result["status"] = "invalid"
                            result["error"] = "runner did not report state-30"
                        else:
                            result["case_status"] = detail.get("status")
                            result["case_duration_ms"] = detail.get("duration_ms")
                            result["harness_wall_ms"] = result["wall_ms"]
                            result["case_wall_ms"] = detail.get("duration_ms")
                            result["instance_count"] = (detail.get("instances") or {}).get("total")
                            result["instance_passed"] = (detail.get("instances") or {}).get("passed")
                            result["result_jsonl_sha256"] = sha256_file(result_path)
                            result.update(shim_summary(metrics_path))
                            result["wall_ms"] = result["cli_wall_ms_mean"]
                            if detail.get("status") == "pass":
                                result["status"] = "passed"
                            else:
                                result["status"] = "incompatible"
                            if result.get("exit_code") != 0:
                                result["status"] = "incompatible"
                        if result.get("status") in ("unavailable", "invalid", "incompatible"):
                            all_ok = False
                    except (OSError, RuntimeError) as error:
                        result = {"status": "unavailable", "error": str(error)}
                        all_ok = False
                row = measurement_row(
                    manifest,
                    arm,
                    "status",
                    "not_applicable",
                    repetition=repetition + 1,
                    binary_sha256=binary_hash,
                    binary_bytes=binary_size,
                    **result,
                )
                emit_row(stream, row)

            pipeline_case_id = args.pipeline_case
            pipeline_case = find_case(suite, pipeline_case_id)
            pipeline_metrics = language_work / "pipeline" / "cli-metrics.jsonl"
            pipeline_work = language_work / "pipeline" / "work"
            pipeline_result = language_work / "pipeline" / "results.jsonl"
            pipeline_result.parent.mkdir(parents=True, exist_ok=True)
            pipeline_status = "unavailable"
            pipeline_error = None
            pipeline_values: dict[str, Any] = {}
            if binary_hash is None:
                pipeline_error = "built CLI binary is missing"
            elif not pipeline_case or pipeline_case.get("profile") not in ("build", "v1.2"):
                pipeline_error = f"{pipeline_case_id} is not a frozen Build or Build-replacement case"
            elif not pipeline_case.get("fake") or "login" in set(pipeline_case.get("needs") or []):
                pipeline_error = "pipeline case is not explicitly marked fake/offline"
            else:
                shim = language_work / "pipeline" / "bin" / "kogen"
                write_measuring_shim(shim, binary, pipeline_metrics)
                stdout = language_work / "logs" / "pipeline.stdout"
                stderr = language_work / "logs" / "pipeline.stderr"
                try:
                    result = process_measurement(
                        suite_command(suite, shim, pipeline_case_id, pipeline_work, pipeline_result, args.jobs),
                        suite,
                        offline_env(),
                        args.command_timeout,
                        stdout,
                        stderr,
                    )
                    detail = case_result(pipeline_result, pipeline_case_id)
                    if detail is None:
                        pipeline_status = "invalid"
                        pipeline_error = "runner did not report the pipeline case"
                    else:
                        pipeline_status = detail.get("status", "invalid")
                        no_fake_request = any(
                            "no provider request reached the fake server" in hint
                            for hint in detail.get("hints", [])
                        )
                        pipeline_values = {
                            "case_duration_ms": detail.get("duration_ms"),
                            "instance_count": (detail.get("instances") or {}).get("total"),
                            "instance_passed": (detail.get("instances") or {}).get("passed"),
                            "fake_provider_requests": 0 if no_fake_request else None,
                            "result_jsonl_sha256": sha256_file(pipeline_result),
                            "runner_exit_code": result["exit_code"],
                            "harness_wall_ms": result["wall_ms"],
                            "wall_ms": detail.get("duration_ms"),
                            **shim_summary(pipeline_metrics),
                        }
                    if pipeline_status != "pass" or result["exit_code"] != 0:
                        pipeline_status = "incompatible"
                        all_ok = False
                except (OSError, RuntimeError) as error:
                    pipeline_error = str(error)
                    all_ok = False
            if pipeline_status != "pass":
                all_ok = False
            emit_row(
                stream,
                measurement_row(
                    manifest,
                    arm,
                    "pipeline",
                    "not_applicable",
                    status=pipeline_status,
                    pipeline_case=pipeline_case_id,
                    binary_sha256=binary_hash,
                    binary_bytes=binary_size,
                    provider_mode="frozen-suite-local-fake-server",
                    provider_requests=None,
                    error=pipeline_error,
                    **pipeline_values,
                ),
            )

    print(args.output)
    return 0 if all_ok else 1


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    snapshot = subparsers.add_parser("snapshot", help="record pinned host, source and suite identities")
    snapshot.add_argument("--go-root", type=Path, default=ROOT)
    snapshot.add_argument("--rust-root", type=Path, default=Path.home() / "Areas/Kogen/kogen-rs")
    snapshot.add_argument("--bun-root", type=Path, default=Path.home() / "Areas/Kogen/kogen-ts")
    snapshot.add_argument("--suite", type=Path, default=Path.home() / "cx/kgo/inputs/conformance-v1.2")
    snapshot.add_argument("--suite-source", type=Path, default=Path.home() / "Areas/Kogen/kogen-conformance")
    snapshot.add_argument("--spec", type=Path, default=Path.home() / "Areas/Kogen/kogen-spec")
    snapshot.add_argument("--output", type=Path, required=True)
    snapshot.set_defaults(func=write_manifest)

    run = subparsers.add_parser("collect", help="measure all three arms in isolated source clones")
    run.add_argument("--manifest", type=Path, required=True)
    run.add_argument("--output", type=Path, required=True)
    run.add_argument("--work-root", type=Path)
    run.add_argument(
        "--pipeline-case",
        default="v1.2-37-build-02",
        help="literal effective fake Build case (default: v1.2-37-build-02)",
    )
    run.add_argument("--repetitions", type=int, default=3)
    run.add_argument("--jobs", type=int, default=2)
    run.add_argument("--command-timeout", type=float, default=900)
    run.set_defaults(func=collect)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if getattr(args, "repetitions", 1) < 1:
        raise SystemExit("--repetitions must be positive")
    if getattr(args, "jobs", 1) < 1:
        raise SystemExit("--jobs must be positive")
    try:
        result = args.func(args)
    except (OSError, RuntimeError, ValueError, KeyError, json.JSONDecodeError) as error:
        print(f"perf-offline: {error}", file=sys.stderr)
        return 2
    return result if isinstance(result, int) else 0


if __name__ == "__main__":
    raise SystemExit(main())
