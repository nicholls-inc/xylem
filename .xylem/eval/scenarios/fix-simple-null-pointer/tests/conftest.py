import importlib.util
from pathlib import Path


shared_path = Path(__file__).resolve().parents[3] / "helpers" / "conftest.py"
spec = importlib.util.spec_from_file_location("xylem_eval_shared_conftest", shared_path)
shared = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(shared)

work_dir = shared.work_dir
task_dir = shared.task_dir
verify = shared.verify
