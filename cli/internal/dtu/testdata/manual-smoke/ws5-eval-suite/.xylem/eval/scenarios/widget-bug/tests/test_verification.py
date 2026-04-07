import xylem_verify as xv


def test_fixture_scaffold(task_dir, verify):
    verify.write_reward(task_dir, xv.compute_reward([("fixture_seeded", True)]))
