import os
import sys

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "..", "helpers"))
import xylem_verify


@pytest.fixture
def work_dir():
    return os.environ.get("WORK_DIR", "/workspace")


@pytest.fixture
def task_dir():
    return os.environ.get("TASK_DIR", os.path.dirname(os.path.dirname(__file__)))


@pytest.fixture
def verify():
    return xylem_verify
