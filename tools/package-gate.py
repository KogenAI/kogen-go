#!/usr/bin/env python3
"""Validate a worker's gate receipt; component-ready never means release parity."""
import json
import os
from pathlib import Path
import subprocess
import time
import sys

pkg = sys.argv[1]
root = Path(__file__).resolve().parents[1]
receipt = root / 'docs/work' / (pkg + '.gate.json')
evidence = root / 'docs/work' / (pkg + '.evidence.md')
for path in (receipt, evidence):
    subprocess.run(['git', 'ls-files', '--error-unmatch', str(path.relative_to(root))],
                   cwd=root, check=True, stdout=subprocess.DEVNULL)
    if not path.is_file() or not path.read_text().strip():
        raise SystemExit('Missing gate receipt or evidence: ' + str(path))
data = json.loads(receipt.read_text())
if data.get('package') != pkg or data.get('accepted_gate') not in ('component', 'behaviour'):
    raise SystemExit('Invalid package/accepted_gate')
if not isinstance(data.get('commands'), list) or not data['commands']:
    raise SystemExit('Record actual validation commands')
if not isinstance(data.get('gaps'), list) or not isinstance(data.get('conflicts'), list):
    raise SystemExit('Record remaining gates and exact historical conflicts')
if pkg.startswith('I') and data['accepted_gate'] != 'behaviour':
    raise SystemExit('Integration rounds require behaviour evidence')
if data['accepted_gate'] == 'component':
    print('Accepted compiled component evidence; behaviour remains with closure round')
    sys.exit(0)

# Behaviour gates run the exact compatible anchors after rebase. Every run is
# retained; an oracle failure requires a changed revision before another run.
expected = set((root / 'docs/work' / (pkg + '.cases')).read_text().strip().split(',')) - {''}
# A coordinator-owned closure map can defer cases only to their named later
# integration round, and admit exact observed v1.2/draft conflicts. Workers must
# not create this external map. With no map, every requested case must pass.
policy_path = Path(os.environ.get('KGO_STATE', str(Path.home() / 'cx/kgo'))) / 'gate-policy.json'
policy = json.loads(policy_path.read_text()).get(pkg, {}) if policy_path.exists() else {}
deferred = policy.get('deferred', {})
conflicts = policy.get('conflicts', {})
results = {}
selected = expected - set(deferred)
if selected:
    state = policy_path.parent
    gate_lock = state / 'gates.lock'
    deadline = time.monotonic() + 600
    while True:
        try:
            gate_lock.mkdir()
            break
        except FileExistsError:
            if time.monotonic() >= deadline:
                raise SystemExit('Shared case/custody/login gate lock remains busy')
            time.sleep(2)
    (gate_lock / 'owner').write_text(str(os.getpid()) + '\n')
    try:
        env = os.environ.copy()
        env['PYTHONDONTWRITEBYTECODE'] = '1'
        env['GIT_CONFIG_GLOBAL'] = '/dev/null'
        env['GIT_CONFIG_NOSYSTEM'] = '1'
        suite = Path.home() / 'cx/kgo/inputs/conformance-v1.2'
        env['PYTHONPATH'] = str(suite)
        env['PATH'] = str(Path(sys.executable).parent) + ':' + env.get('PATH', '')
        subprocess.run(['make', 'build'], cwd=root, check=True, env=env)
        evidence_dir = state / 'evidence' / pkg / ('merge-' + str(time.time_ns()))
        evidence_dir.mkdir(parents=True)
        report = evidence_dir / 'results.jsonl'
        profiles = 'cli,state,approval,shape,build,ladder,provider,custody,format,v1.2'
        if any(case.startswith('exunit-') for case in selected):
            profiles += ',exunit'
        command = [sys.executable, '-m', 'kogen_conformance', 'run',
                   '--kogen', str(root / 'bin/kogen'), '--profile', profiles,
                   '--case', ','.join(sorted(selected)), '--jobs', '2',
                   '--time-scale', '0.02', '--workdir', str(evidence_dir / 'work'),
                   '--out', str(report)]
        (evidence_dir / 'command.json').write_text(json.dumps(command) + '\n')
        subprocess.run(command, cwd=root, env=env, check=False)
        for line in report.read_text().splitlines():
            row = json.loads(line)
            if 'id' in row and 'status' in row:
                if row['id'] in results:
                    raise SystemExit('Duplicate oracle case: ' + row['id'])
                results[row['id']] = row
    finally:
        (gate_lock / 'owner').unlink()
        gate_lock.rmdir()
for case in expected:
    if case in deferred:
        if not deferred[case].get('closure') or not deferred[case].get('reason'):
            raise SystemExit('Incomplete coordinator deferral: ' + case)
        continue
    result = results.get(case)
    if not result:
        raise SystemExit('Missing oracle case: ' + case)
    if result['status'] != 'pass':
        if case not in conflicts or result['status'] != 'fail' or not conflicts[case]:
            raise SystemExit('Unaccepted oracle result: ' + case + ': ' + result['status'])
        if case not in json.dumps(data['conflicts']):
            raise SystemExit('Receipt omits exact historical conflict: ' + case)
if pkg.startswith('I8'):
    if deferred or data['gaps']:
        raise SystemExit('Release gate cannot defer cases or leave draft/replay/platform gaps')
    if not data.get('shared_v13_manifest') or not data.get('production_replay_manifest'):
        raise SystemExit('Release requires frozen shared v1.3 and production replay manifests')
print('Accepted post-rebase behaviour evidence; see receipt for separate draft/replay/platform gates')
