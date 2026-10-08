#!/usr/bin/env python3
"""Run the frozen, migrated v1.3 G-slice replay without touching its source.

The required cohort manifest is deliberately external to this Go worker. It
must bind the clean spec commit and the migrated schema/scenarios/hand goldens
for all eight G slices. D-slice observations are reported separately and are
never included in this replay denominator.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys


FORMAT = "kogen-shared-v1.3-cohort/v1"
DRAFT_COMMIT = "e19dd1c21c19c5be1201c3b6a42c59c28b5c2887"
G_SLICES = ("intent", "approve", "queue", "status", "recovery", "rebase", "stream", "session")
D_SLICES = ("gate", "orchestration", "accounts", "setup-cache")
SEEDS = (17, 23, 41)
TRACE_COUNT = 500
STEP_COUNT = 25


class ReplayError(Exception):
    pass


def command(args: list[str], *, cwd: Path | None = None, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    try:
        result = subprocess.run(args, cwd=cwd, env=env, text=True, capture_output=True, check=False)
    except OSError as error:
        raise ReplayError(f"cannot run {args[0]!r}: {error}") from error
    if result.returncode != 0:
        detail = (result.stdout + result.stderr).strip()
        raise ReplayError(f"command failed ({result.returncode}): {args!r}\n{detail[-4000:]}")
    return result


def git(root: Path, *args: str, allow_failure: bool = False) -> str:
    env = os.environ.copy()
    env["GIT_CONFIG_GLOBAL"] = "/dev/null"
    env["GIT_CONFIG_NOSYSTEM"] = "1"
    result = subprocess.run(["git", "-C", str(root), *args], env=env, text=True, capture_output=True, check=False)
    if result.returncode != 0 and not allow_failure:
        raise ReplayError(f"git {' '.join(args)} failed in {root}: {(result.stderr or result.stdout).strip()}")
    return result.stdout.strip()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def slice_files(root: Path) -> list[Path]:
    files = [root / "xspec.json"]
    files += sorted((root / "spec").rglob("*.qnt"))
    files += sorted((root / "scenarios").glob("*.json"))
    files += sorted((root / "golden" / "hand").glob("*.json"))
    if not files or any(not path.is_file() or path.is_symlink() for path in files):
        raise ReplayError(f"slice has missing or unsafe cohort files: {root}")
    return files


def digest_files(root: Path, files: list[Path]) -> str:
    digest = hashlib.sha256()
    for path in sorted(files):
        relative = path.relative_to(root).as_posix()
        digest.update(relative.encode("utf-8"))
        digest.update(b"\0")
        digest.update(path.read_bytes())
        digest.update(b"\0")
    return digest.hexdigest()


def clean_revision(root: Path, label: str) -> str:
    revision = git(root, "rev-parse", "HEAD")
    dirty = git(root, "status", "--porcelain=v1", "--untracked-files=all")
    if dirty:
        preview = "\n".join(dirty.splitlines()[:20])
        raise ReplayError(f"{label} source tree is dirty; refusing a frozen replay:\n{preview}")
    return revision


def build_revision(go: str, binary: Path) -> tuple[str, str]:
    result = command([go, "version", "-m", str(binary)])
    revision = re.search(r"(?m)^\s*build\s+vcs\.revision=(\S+)\s*$", result.stdout)
    modified = re.search(r"(?m)^\s*build\s+vcs\.modified=(\S+)\s*$", result.stdout)
    if revision is None or modified is None:
        raise ReplayError(f"{binary} has no Go VCS build metadata; refusing same-revision claims")
    return revision.group(1), modified.group(1)


def validate_manifest(spec_root: Path, manifest_path: Path, spec_revision: str) -> dict[str, object]:
    if not manifest_path.is_file() or manifest_path.is_symlink():
        raise ReplayError(f"frozen cohort manifest is missing or unsafe: {manifest_path}")
    manifest_path = manifest_path.resolve()
    try:
        relative_manifest = manifest_path.relative_to(spec_root.resolve())
    except ValueError as error:
        raise ReplayError("cohort manifest must be committed inside the authoritative spec checkout") from error
    tracked = git(spec_root, "ls-files", "--error-unmatch", relative_manifest.as_posix(), allow_failure=True)
    if not tracked:
        raise ReplayError("cohort manifest is not tracked by the authoritative spec revision")
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise ReplayError(f"cannot read cohort manifest: {error}") from error
    if not isinstance(manifest, dict) or manifest.get("format") != FORMAT:
        raise ReplayError(f"cohort manifest format must be {FORMAT!r}")
    if manifest.get("status") != "frozen" or manifest.get("draft_migration") != "v1.3-draft":
        raise ReplayError("cohort manifest must explicitly identify a frozen v1.3-draft migration")
    if manifest.get("spec_revision") != spec_revision:
        raise ReplayError("cohort manifest spec_revision does not match the clean spec checkout")
    if manifest.get("draft_commit") != DRAFT_COMMIT:
        raise ReplayError("cohort manifest does not bind the authoritative e19dd1c v1.3-draft commit")
    ancestry = subprocess.run(
        ["git", "-C", str(spec_root), "merge-base", "--is-ancestor", DRAFT_COMMIT, "HEAD"],
        capture_output=True,
        check=False,
    )
    if ancestry.returncode != 0:
        raise ReplayError("spec checkout is not descended from the authoritative v1.3-draft commit")

    slices = manifest.get("g_slices")
    if not isinstance(slices, dict) or set(slices) != set(G_SLICES):
        raise ReplayError("cohort manifest must list exactly the eight required G slices")
    for name in G_SLICES:
        root = spec_root / "quint" / "slices" / name
        entry = slices[name]
        if not isinstance(entry, dict) or entry.get("classification") != "G" or entry.get("migrated") is not True:
            raise ReplayError(f"cohort manifest does not mark {name} as a migrated G slice")
        files = slice_files(root)
        scenarios = sorted((root / "scenarios").glob("*.json"))
        goldens = sorted((root / "golden" / "hand").glob("*.json"))
        if not scenarios or len(scenarios) != len(goldens) or entry.get("hand_count") != len(scenarios):
            raise ReplayError(f"{name} scenario and hand-golden counts are incomplete or differ from the manifest")
        if entry.get("tree_sha256") != digest_files(root, files):
            raise ReplayError(f"{name} schema/scenario/hand-golden digest differs from the frozen manifest")
    d_slices = manifest.get("d_slices")
    if not isinstance(d_slices, dict) or set(d_slices) != set(D_SLICES):
        raise ReplayError("cohort manifest must list all four D slices separately from G slices")
    for name, entry in d_slices.items():
        if (
            not isinstance(entry, dict)
            or entry.get("classification") != "D"
            or entry.get("migration") != "observational"
            or entry.get("counts_toward_g") is not False
        ):
            raise ReplayError(
                f"D slice {name} must declare observational migration and be excluded from G counts"
            )
    harness = spec_root / "quint" / "prototype" / "harness" / "xspec.py"
    if manifest.get("harness_sha256") != sha256_file(harness):
        raise ReplayError("the frozen cohort manifest does not match the shared xspec harness bytes")
    return manifest


def probe_xspec(binary: Path, name: str) -> None:
    result = subprocess.run(
        [str(binary), name], input='{"op":"reset"}\n', text=True, capture_output=True, check=False,
    )
    if result.returncode != 0:
        detail = (result.stderr or result.stdout).strip()
        raise ReplayError(f"kogen-xspec {name} is not available at this revision: {detail}")
    lines = result.stdout.splitlines()
    if len(lines) != 1:
        raise ReplayError(f"kogen-xspec {name} did not return exactly one full reset observation")
    try:
        observation = json.loads(lines[0])
    except json.JSONDecodeError as error:
        raise ReplayError(f"kogen-xspec {name} returned invalid reset JSON: {error}") from error
    if not isinstance(observation, dict) or has_projection_marker(observation):
        raise ReplayError(f"kogen-xspec {name} reset result is not a full object observation")


def has_projection_marker(value: object) -> bool:
    if value == "no_seam":
        return True
    if isinstance(value, list):
        return any(has_projection_marker(item) for item in value)
    if isinstance(value, dict):
        return "no_seam" in value or any(has_projection_marker(item) for item in value.values())
    return False


def run_logged(args: list[str], cwd: Path, env: dict[str, str], log_path: Path) -> tuple[int, str]:
    result = subprocess.run(args, cwd=cwd, env=env, text=True, capture_output=True, check=False)
    content = "$ " + " ".join(args) + "\n"
    content += f"cwd: {cwd}\nexit: {result.returncode}\n\n"
    content += result.stdout
    if result.stderr:
        content += "\n[stderr]\n" + result.stderr
    log_path.parent.mkdir(parents=True, exist_ok=True)
    log_path.write_text(content, encoding="utf-8")
    return result.returncode, result.stdout + result.stderr


def conform_counts(output: str) -> tuple[int, int] | None:
    match = re.search(r"conform: (\d+)/(\d+) traces agree with the spec", output)
    return (int(match.group(1)), int(match.group(2))) if match else None


def run_replay(args: argparse.Namespace) -> int:
    spec_root = args.spec_root.resolve()
    go_root = Path(__file__).resolve().parents[1]
    out = args.out.resolve()
    if not spec_root.is_dir() or not (spec_root / ".git").exists():
        raise ReplayError(f"spec root is not a Git checkout: {spec_root}")
    if out == spec_root or spec_root in out.parents or out == go_root or go_root in out.parents:
        raise ReplayError("replay output must be outside both source checkouts")
    if out.exists() and (not out.is_dir() or any(out.iterdir())):
        raise ReplayError(f"refusing to overwrite a nonempty replay directory: {out}")
    out.mkdir(parents=True, exist_ok=True)

    go_revision = clean_revision(go_root, "Go")
    spec_revision = clean_revision(spec_root, "spec")
    manifest = validate_manifest(spec_root, args.cohort_manifest, spec_revision)
    cli_revision, cli_modified = build_revision(args.go, args.cli.resolve())
    xspec_revision, xspec_modified = build_revision(args.go, args.xspec.resolve())
    if (cli_revision, xspec_revision) != (go_revision, go_revision) or cli_modified != "false" or xspec_modified != "false":
        raise ReplayError(
            "CLI and xspec binaries must both be clean builds from the current Go revision "
            f"{go_revision}; got CLI=({cli_revision}, modified={cli_modified}), "
            f"xspec=({xspec_revision}, modified={xspec_modified})"
        )
    for name in G_SLICES:
        probe_xspec(args.xspec.resolve(), name)

    scratch = out / "scratch" / "quint"
    prototype = spec_root / "quint" / "prototype"
    slices_root = spec_root / "quint" / "slices"
    if not (prototype / "node_modules" / ".bin" / "quint").exists():
        raise ReplayError("the frozen Quint prototype has no installed quint executable")
    scratch.mkdir(parents=True)
    shutil.copytree(prototype, scratch / "prototype", ignore=shutil.ignore_patterns("build", "__pycache__", "*.pyc"))
    (scratch / "slices").mkdir()
    for name in G_SLICES:
        shutil.copytree(slices_root / name, scratch / "slices" / name)

    report: dict[str, object] = {
        "format": "kogen-production-replay/v1",
        "go_revision": go_revision,
        "cli_revision": cli_revision,
        "xspec_revision": xspec_revision,
        "spec_revision": spec_revision,
        "cohort_format": manifest["format"],
        "g_slices": {},
        "d_slices": {
            name: {
                "classification": "D",
                "migration": manifest["d_slices"][name]["migration"],
                "counts_toward_g": False,
                "result": "not_run_requires_separate_production_observation",
            }
            for name in D_SLICES
        },
    }
    all_ok = True
    for name in G_SLICES:
        source_slice = slices_root / name
        replay_dir = out / "replay" / name
        replay_dir.mkdir(parents=True, exist_ok=True)
        scratch_slice = scratch / "slices" / name
        env = os.environ.copy()
        env.update({
            "XSPEC_SLICE": str(scratch_slice),
            "XSPEC_BUILD": str(replay_dir / "build"),
            "XSPEC_GOLDEN": str(replay_dir / "golden"),
            "PYTHONDONTWRITEBYTECODE": "1",
        })
        harness = scratch / "prototype" / "harness" / "xspec.py"
        entry: dict[str, object] = {"hand_count": manifest["g_slices"][name]["hand_count"], "seeds": {}}
        report["g_slices"][name] = entry
        code, output = run_logged([args.python, str(harness), "spec"], scratch / "prototype", env, replay_dir / "spec.log")
        if code == 0:
            generated_hand = sorted((replay_dir / "golden" / "hand").glob("*.json"))
            source_hand = {path.name: sha256_file(path) for path in (source_slice / "golden" / "hand").glob("*.json")}
            generated_hand_hashes = {path.name: sha256_file(path) for path in generated_hand}
            code = code if len(generated_hand) == entry["hand_count"] and generated_hand_hashes == source_hand else 1
            if code != 0:
                output += "\nreplay driver: generated hand goldens do not match the frozen file set\n"
                (replay_dir / "spec.log").write_text((replay_dir / "spec.log").read_text(encoding="utf-8") + output, encoding="utf-8")
        entry["spec"] = "pass" if code == 0 else "fail"
        if code != 0:
            all_ok = False
            continue

        for seed in SEEDS:
            seed_result: dict[str, object] = {}
            entry["seeds"][str(seed)] = seed_result
            gen_args = [args.python, str(harness), "gen", "--traces", str(TRACE_COUNT), "--steps", str(STEP_COUNT), "--seed", str(seed)]
            code, gen_output = run_logged(gen_args, scratch / "prototype", env, replay_dir / f"seed-{seed}-gen.log")
            generated_dir = replay_dir / "golden" / "gen"
            try:
                gen_manifest = json.loads((generated_dir / "manifest.json").read_text(encoding="utf-8"))
                gen_files = [p for p in generated_dir.glob("*.json") if p.name != "manifest.json"]
                generated_names = sorted(path.stem for path in gen_files)
                manifest_names = gen_manifest.get("trace_names")
                gen_ok = (
                    gen_manifest.get("source") == "gen"
                    and gen_manifest.get("seed") == str(seed)
                    and gen_manifest.get("requested_traces") == TRACE_COUNT
                    and gen_manifest.get("steps") == STEP_COUNT
                    and len(gen_files) == TRACE_COUNT
                    and isinstance(manifest_names, list)
                    and len(manifest_names) == TRACE_COUNT
                    and sorted(manifest_names) == generated_names
                )
            except (OSError, UnicodeError, json.JSONDecodeError, AttributeError):
                gen_ok = False
            seed_result["gen"] = "pass" if code == 0 and gen_ok else "fail"
            if code != 0 or not gen_ok:
                all_ok = False
                continue
            conform_args = [args.python, str(harness), "conform", "--", str(args.xspec.resolve()), name]
            code, conform_output = run_logged(conform_args, scratch / "prototype", env, replay_dir / f"seed-{seed}-conform.log")
            counts = conform_counts(conform_output)
            conforms = code == 0 and counts is not None and counts[0] == counts[1] and counts[1] == TRACE_COUNT + entry["hand_count"]
            seed_result["conform"] = "pass" if conforms else "fail"
            seed_result["traces"] = {"matched": counts[0], "total": counts[1]} if counts else None
            if not conforms:
                all_ok = False

    report_path = out / "production-replay.json"
    report["result"] = "pass" if all_ok else "fail"
    report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"production replay: {report['result']} ({report_path})")
    return 0 if all_ok else 1


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--spec-root", type=Path, required=True)
    parser.add_argument("--cohort-manifest", type=Path, required=True)
    parser.add_argument("--cli", type=Path, required=True)
    parser.add_argument("--xspec", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--go", default="go", help="Go executable used to inspect embedded build metadata")
    parser.add_argument("--python", default=sys.executable, help="Python executable for the shared harness")
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    try:
        return run_replay(parse_args(argv))
    except ReplayError as error:
        print(f"production replay refused: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
