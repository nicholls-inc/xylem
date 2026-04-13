## xylem daemon operator conventions

### Binary path

The xylem binary in the daemon worktree is always at:

```
/Users/harry.nicholls/repos/xylem/.daemon-root/cli/xylem
```

Or `./cli/xylem` when CWD is `.daemon-root/`. Do not search for it, do not use `which xylem`, do not add it to PATH. Use the absolute path.

### Daemon liveness check

The sandbox blocks `kill -0`, `ps aux`, and `pgrep`. All three produce false negatives (report daemon as dead when it is alive). Do not use them. `xylem doctor` uses `kill -0` internally and inherits the same false negative.

Instead, read `.xylem/state/daemon-health.json`. The daemon writes `updated_at` every tick (~30s). If it is less than 120 seconds old, the daemon is alive:

```bash
cd /Users/harry.nicholls/repos/xylem/.daemon-root
python3 -c "
import json
from datetime import datetime, timezone
h = json.load(open('.xylem/state/daemon-health.json'))
age = (datetime.now(timezone.utc) - datetime.fromisoformat(h['updated_at'].replace('Z','+00:00'))).total_seconds()
print(f'PID {h[\"pid\"]} | updated {int(age)}s ago | {\"ALIVE\" if age < 120 else \"DEAD\"}')
"
```

If the file does not exist, the daemon has never started.

### Combining with doctor

Run `xylem doctor` for queue health, zombie detection, and worktree status. But override its daemon-liveness verdict with the `daemon-health.json` check above. Doctor's "Daemon not running" is a known false positive in sandboxed environments.
