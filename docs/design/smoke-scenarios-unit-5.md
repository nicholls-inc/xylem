# Smoke Scenarios: Workstream 5 — Eval Suite (Harbor-native)

Spec reference: `docs/design/xylem-harness-impl-spec.md`, Section 9

These scenarios verify that the eval suite scaffolding — directory layout, task
file formats, shared helpers, harbor.yaml, and rubrics — is correctly in place
and usable by a first-time operator.

---

### S1: Eval directory scaffold exists

**Spec ref:** Section 9.2

**Preconditions:** WS5 implementation complete. Working copy of the repository
is checked out.

**Action:** Inspect the `.xylem/eval/` directory tree.

**Expected outcome:** The following paths exist:
- `.xylem/eval/harbor.yaml`
- `.xylem/eval/helpers/xylem_verify.py`
- `.xylem/eval/helpers/conftest.py`
- `.xylem/eval/scenarios/` (directory, may be empty or contain at least one
  scenario subdirectory)
- `.xylem/eval/rubrics/plan_quality.toml`
- `.xylem/eval/rubrics/evidence_quality.toml`

**Verification:** `ls -R .xylem/eval/` shows each of the above paths. No
`FileNotFoundError` or missing directory.

---

### S2: harbor.yaml is valid YAML with required fields

**Spec ref:** Section 9.4

**Preconditions:** `.xylem/eval/harbor.yaml` exists (S1 passes).

**Action:** Open `.xylem/eval/harbor.yaml` and inspect its contents.

**Expected outcome:** The file parses as valid YAML and contains all five
required top-level keys: `agent`, `model`, `path`, `n_attempts`,
`n_concurrent`. The `agent` value is `claude-code`, `path` is `scenarios/`.
`n_attempts` and `n_concurrent` are positive integers.

**Verification:** `python3 -c "import yaml, sys; d=yaml.safe_load(open('.xylem/eval/harbor.yaml')); assert d['agent']=='claude-code'; assert d['path']=='scenarios/'; print('OK')"` exits 0 and prints `OK`.

---

### S3: xylem_verify.py imports cleanly with no missing dependencies

**Spec ref:** Section 9.5

**Preconditions:** `.xylem/eval/helpers/xylem_verify.py` exists (S1 passes).
Python 3.10+ is available.

**Action:** Run `python3 -c "import sys; sys.path.insert(0, '.xylem/eval/helpers'); import xylem_verify"`.

**Expected outcome:** The command exits 0 with no output and no `ImportError`.
All standard-library imports (`os`, `json`, `glob`) resolve correctly.

**Verification:** Exit code is 0. No `ModuleNotFoundError` is printed to
stderr.

---

### S4: xylem_verify.py exposes the expected public API

**Spec ref:** Section 9.5

**Preconditions:** `xylem_verify` imports successfully (S3 passes).

**Action:** Run a Python one-liner that checks for the presence of each
documented function:
```
python3 -c "
import sys; sys.path.insert(0, '.xylem/eval/helpers'); import xylem_verify as xv
names = ['find_vessel_dir','load_summary','load_evidence','load_phase_output',
         'load_audit_log','assert_vessel_completed','assert_vessel_failed',
         'assert_phases_completed','assert_gates_passed','assert_evidence_level',
         'assert_cost_within_budget','compute_reward','write_reward','EVIDENCE_RANK']
missing = [n for n in names if not hasattr(xv, n)]
assert not missing, f'Missing: {missing}'; print('OK')
"
```

**Expected outcome:** Exits 0 and prints `OK`. No function listed in the spec
is absent from the module.

**Verification:** Exit code 0. `missing` list is empty.

---

### S5: EVIDENCE_RANK contains all five documented levels

**Spec ref:** Section 9.5

**Preconditions:** `xylem_verify` imports successfully (S3 passes).

**Action:** Run:
```
python3 -c "
import sys; sys.path.insert(0, '.xylem/eval/helpers'); import xylem_verify as xv
required = {'proved', 'mechanically_checked', 'behaviorally_checked', 'observed_in_situ', ''}
missing = required - set(xv.EVIDENCE_RANK.keys())
assert not missing, f'Missing keys: {missing}'
assert xv.EVIDENCE_RANK['proved'] > xv.EVIDENCE_RANK['mechanically_checked'] > xv.EVIDENCE_RANK['behaviorally_checked'] > xv.EVIDENCE_RANK['observed_in_situ'] > xv.EVIDENCE_RANK['']
print('OK')
"
```

**Expected outcome:** Exits 0 and prints `OK`. The five keys are present and
their integer values are strictly ordered from `proved` (highest) down to `''`
(lowest).

**Verification:** Exit code 0, no assertion error.

---

### S6: compute_reward returns correct scores

**Spec ref:** Section 9.5

**Preconditions:** `xylem_verify` imports successfully (S3 passes).

**Action:** Run three inline assertions:
```
python3 -c "
import sys; sys.path.insert(0, '.xylem/eval/helpers'); import xylem_verify as xv
# All pass, uniform weight
assert xv.compute_reward([('a', True), ('b', True)]) == 1.0
# Half pass, uniform weight
r = xv.compute_reward([('a', True), ('b', False)])
assert r == 0.5, r
# Empty checks
assert xv.compute_reward([]) == 0.0
print('OK')
"
```

**Expected outcome:** Exits 0 and prints `OK`. Weighted scoring works for the
all-pass, half-pass, and empty cases.

**Verification:** Exit code 0, no assertion error.

---

### S7: conftest.py exposes work_dir, task_dir, and verify fixtures

**Spec ref:** Section 9.5

**Preconditions:** `.xylem/eval/helpers/conftest.py` exists (S1 passes).

**Action:** Parse `conftest.py` with Python's `ast` module and check for the
three required fixture functions:
```
python3 -c "
import ast, pathlib
src = pathlib.Path('.xylem/eval/helpers/conftest.py').read_text()
tree = ast.parse(src)
funcs = [n.name for n in ast.walk(tree) if isinstance(n, ast.FunctionDef)]
for name in ['work_dir', 'task_dir', 'verify']:
    assert name in funcs, f'{name} fixture missing'
print('OK')
"
```

**Expected outcome:** Exits 0 and prints `OK`. All three fixture functions are
defined in `conftest.py`.

**Verification:** Exit code 0, no assertion error.

---

### S8: task.toml for an existing scenario has required fields

**Spec ref:** Section 9.3

**Preconditions:** At least one scenario directory exists under
`.xylem/eval/scenarios/` containing a `task.toml` (S1 passes; note that
scenario population is deferred — see S18).

**Action:** For the first scenario directory found, open its `task.toml` and
check it parses as valid TOML with `task.id` and
`task.environment.timeout_seconds` present.

**Expected outcome:** `task.id` matches the scenario's directory name exactly.
`task.environment.timeout_seconds` is a positive integer. The file contains no
TOML syntax errors.

**Verification:**
```
python3 -c "
import tomllib, os, sys
scenario = next(iter(sorted(os.listdir('.xylem/eval/scenarios/'))))
with open(f'.xylem/eval/scenarios/{scenario}/task.toml', 'rb') as f:
    d = tomllib.load(f)
assert d['task']['id'] == scenario, f'id mismatch: {d[\"task\"][\"id\"]} vs {scenario}'
assert d['task']['environment']['timeout_seconds'] > 0
print('OK')
"
```
Exit code 0, prints `OK`.

---

### S9: instruction.md for an existing scenario contains no Harbor-specific terms

**Spec ref:** Section 9.3

**Preconditions:** At least one scenario directory exists with an
`instruction.md` (see S8 precondition).

**Action:** Read the `instruction.md` and check it does not contain the words
"Harbor", "scoring", or "verification".

**Expected outcome:** None of the forbidden words appear in the file. The file
is non-empty and includes at least one `xylem` command (e.g., `xylem enqueue`
or `xylem drain`).

**Verification:**
```
python3 -c "
import os
scenario = next(iter(sorted(os.listdir('.xylem/eval/scenarios/'))))
text = open(f'.xylem/eval/scenarios/{scenario}/instruction.md').read().lower()
for word in ['harbor', 'scoring', 'verification']:
    assert word not in text, f'Forbidden word found: {word}'
assert 'xylem' in text
print('OK')
"
```

---

### S10: Scenario directory contains all required files

**Spec ref:** Section 9.2

**Preconditions:** At least one scenario directory exists (see S8 precondition).

**Action:** Check that the first scenario directory contains the four required
files/directories: `instruction.md`, `task.toml`, `tests/test.sh`,
`tests/test_verification.py`.

**Expected outcome:** All four paths are present under the scenario directory.
`tests/test.sh` is executable (mode includes execute bit).

**Verification:**
```
python3 -c "
import os, stat
scenario = next(iter(sorted(os.listdir('.xylem/eval/scenarios/'))))
base = f'.xylem/eval/scenarios/{scenario}'
for p in ['instruction.md', 'task.toml', 'tests/test.sh', 'tests/test_verification.py']:
    assert os.path.exists(os.path.join(base, p)), f'Missing: {p}'
mode = os.stat(os.path.join(base, 'tests/test.sh')).st_mode
assert mode & stat.S_IXUSR, 'test.sh not executable'
print('OK')
"
```

---

### S11: plan_quality.toml is valid TOML with weights summing to 1.0

**Spec ref:** Section 9.7

**Preconditions:** `.xylem/eval/rubrics/plan_quality.toml` exists (S1 passes).

**Action:** Load the file and sum the `weight` fields across all criteria.

**Expected outcome:** File parses without error. `rubric.name` is
`"plan_quality"`. There are exactly 3 criteria. Weights sum to exactly 1.0
(within floating-point tolerance of 0.001). Each criterion has a non-empty
`description`.

**Verification:**
```
python3 -c "
import tomllib
with open('.xylem/eval/rubrics/plan_quality.toml', 'rb') as f:
    d = tomllib.load(f)
assert d['rubric']['name'] == 'plan_quality'
total = sum(c['weight'] for c in d['rubric']['criteria'])
assert abs(total - 1.0) < 0.001, f'weights sum to {total}'
print('OK')
"
```

---

### S12: evidence_quality.toml is valid TOML with weights summing to 1.0

**Spec ref:** Section 9.7

**Preconditions:** `.xylem/eval/rubrics/evidence_quality.toml` exists (S1
passes).

**Action:** Load the file and verify the same constraints as S11 applied to
`evidence_quality`.

**Expected outcome:** `rubric.name` is `"evidence_quality"`. Weights sum to
1.0 within tolerance. Each criterion has a `description`.

**Verification:**
```
python3 -c "
import tomllib
with open('.xylem/eval/rubrics/evidence_quality.toml', 'rb') as f:
    d = tomllib.load(f)
assert d['rubric']['name'] == 'evidence_quality'
total = sum(c['weight'] for c in d['rubric']['criteria'])
assert abs(total - 1.0) < 0.001, f'weights sum to {total}'
print('OK')
"
```

---

### S13: harbor.yaml path resolves to a directory that exists

**Spec ref:** Section 9.4

**Preconditions:** `harbor.yaml` and the `scenarios/` directory both exist (S1
passes).

**Action:** Read the `path` field from `harbor.yaml` and check that the
resulting path exists relative to `.xylem/eval/`.

**Expected outcome:** `.xylem/eval/scenarios/` exists as a directory. No 404 or
missing-path error when `harbor run` resolves its scenario path.

**Verification:**
```
python3 -c "
import yaml, os
d = yaml.safe_load(open('.xylem/eval/harbor.yaml'))
resolved = os.path.join('.xylem/eval', d['path'])
assert os.path.isdir(resolved), f'Not a directory: {resolved}'
print('OK')
"
```

---

### S14: Workflow-execution verification template produces a valid reward

**Spec ref:** Section 9.6

**Preconditions:** `xylem_verify.compute_reward` works (S6 passes). A synthetic
summary dict is constructed in memory — no actual Harbor run needed.

**Action:** Run the reward computation logic from the workflow-execution
template against a synthetic summary where all checks pass:
```
python3 -c "
import sys; sys.path.insert(0, '.xylem/eval/helpers'); import xylem_verify as xv
checks = [
    ('vessel_completed', True),
    ('phases_completed', True),
    ('gate_passed', True),
    ('evidence_level', True),
    ('budget_ok', True),
]
weights = {'vessel_completed': 3.0, 'phases_completed': 2.0,
           'gate_passed': 2.0, 'evidence_level': 1.0, 'budget_ok': 1.0}
score = xv.compute_reward(checks, weights)
assert score == 1.0, f'Expected 1.0, got {score}'
print('OK')
"
```

**Expected outcome:** Score is 1.0 when all checks pass with the documented
weights.

**Verification:** Exit code 0, prints `OK`.

---

### S15: Workflow-execution verification template fails below threshold when checks fail

**Spec ref:** Section 9.6

**Preconditions:** `xylem_verify.compute_reward` works (S6 passes).

**Action:** Run compute_reward with only `vessel_completed=False` and all other
checks True, using the documented weights:
```
python3 -c "
import sys; sys.path.insert(0, '.xylem/eval/helpers'); import xylem_verify as xv
checks = [
    ('vessel_completed', False),
    ('phases_completed', True),
    ('gate_passed', True),
    ('evidence_level', True),
    ('budget_ok', True),
]
weights = {'vessel_completed': 3.0, 'phases_completed': 2.0,
           'gate_passed': 2.0, 'evidence_level': 1.0, 'budget_ok': 1.0}
score = xv.compute_reward(checks, weights)
assert score < 0.8, f'Expected below threshold 0.8, got {score}'
print(f'OK score={score:.4f}')
"
```

**Expected outcome:** Score drops below 0.8 when `vessel_completed` (weight
3.0) fails, confirming that the weighted scoring produces a meaningful signal
and not a pass when a high-weight check fails.

**Verification:** Exit code 0, prints `OK score=...` with a value less than 0.8.

---

### S16: write_reward creates reward.txt with a four-decimal score

**Spec ref:** Section 9.5

**Preconditions:** `xylem_verify` imports successfully (S3 passes). A writable
temporary directory is available.

**Action:**
```
python3 -c "
import sys, tempfile, os
sys.path.insert(0, '.xylem/eval/helpers'); import xylem_verify as xv
with tempfile.TemporaryDirectory() as d:
    xv.write_reward(d, 0.7500)
    content = open(os.path.join(d, 'reward.txt')).read().strip()
    assert content == '0.7500', f'Got: {repr(content)}'
    print('OK')
"
```

**Expected outcome:** `reward.txt` is created in the target directory. Its
content is `0.7500` (four decimal places, no extra whitespace after strip).

**Verification:** Exit code 0, prints `OK`.

---

### S17: Deferred items are NOT present

**Spec ref:** Section 9.10

**Preconditions:** WS5 implementation complete.

**Action:** Assert that none of the deferred items are present:
1. No `Dockerfile` under any scenario's `environment/` subdirectory (fixture Docker images are deferred).
2. No `.github/workflows/` file references `harbor run` (CI pipeline is deferred).
3. No `jobs/` directory at the repo root or under `.xylem/eval/` (baseline establishment is deferred).

**Expected outcome:** All three assertions pass. The scaffold exists without
any deferred content partially stubbed in.

**Verification:**
```
python3 -c "
import os, glob
dockerfiles = glob.glob('.xylem/eval/scenarios/**/Dockerfile', recursive=True)
assert not dockerfiles, f'Unexpected Dockerfiles (deferred): {dockerfiles}'

ci_files = glob.glob('.github/workflows/*.yml')
for f in ci_files:
    assert 'harbor run' not in open(f).read(), f'harbor run found in CI (deferred): {f}'

assert not os.path.isdir('.xylem/eval/jobs'), 'jobs/ dir present under .xylem/eval/ (deferred)'
assert not os.path.isdir('jobs'), 'jobs/ dir present at repo root (deferred)'
print('OK')
"
```

---

### S18: scenarios/ directory is present even with no scenarios yet populated

**Spec ref:** Section 9.2, Section 9.10

**Preconditions:** WS5 implementation complete. Scenario population is listed
as a deferred item.

**Action:** Check whether `.xylem/eval/scenarios/` exists and is a directory,
regardless of whether it contains any subdirectories.

**Expected outcome:** The `scenarios/` directory exists (created as part of
the scaffold). It may be empty or contain placeholder content. Its absence
would mean `harbor run -c .xylem/eval/harbor.yaml` fails immediately with a
path-not-found error before attempting any scenario.

**Verification:**
```
python3 -c "
import os
assert os.path.isdir('.xylem/eval/scenarios/'), 'scenarios/ directory missing'
print('OK')
"
```
