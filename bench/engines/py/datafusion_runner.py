"""DataFusion entry point. Needs `uv sync --group datafusion`."""

from __future__ import annotations

import sys

from engines.py import sql_runner

if __name__ == "__main__":
    sys.exit(sql_runner.main("datafusion"))
