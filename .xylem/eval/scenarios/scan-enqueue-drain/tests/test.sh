#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
pip install -q pytest > /dev/null 2>&1
pytest tests/test_verification.py -v --tb=short
