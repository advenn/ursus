"""Per-run resource isolation.

Every engine subprocess is launched inside a transient systemd scope with the
same memory ceiling and the same CPU affinity, so a result is a statement about
the engine rather than about what else the machine happened to be doing.

Two deliberate choices:

  * MemorySwapMax=0. Swapping is not a graceful degradation for a benchmark; it
    is a silent 100x. An engine that exceeds the budget must be killed and
    recorded as `oom`, which is a real and reportable outcome.
  * CPU affinity via taskset, not -p AllowedCPUs. The cpuset controller is
    frequently not delegated to the user slice (it is not on the machine this
    was written for), and a property that is accepted-then-ignored is worse than
    one that was never requested.
"""

from __future__ import annotations

import shutil
from dataclasses import dataclass

# 128 + SIGKILL: what a shell and systemd both report for an OOM kill.
OOM_EXIT_CODE = 137


@dataclass(frozen=True)
class Budget:
    """The resource envelope a single engine run is given."""

    mem_limit: str | None = None  # systemd byte spec, e.g. "8G"
    cpus: int | None = None  # pin to CPUs 0..cpus-1
    enforce: bool = True

    def wrap(self, argv: list[str], scope_name: str) -> list[str]:
        """Wrap `argv` in the isolation prefix appropriate for this machine."""
        cmd: list[str] = []

        if self.enforce and self.mem_limit and shutil.which("systemd-run"):
            cmd += [
                "systemd-run",
                "--user",
                "--scope",
                "--quiet",
                "--collect",
                f"--unit={scope_name}",
                "-p",
                f"MemoryMax={self.mem_limit}",
                "-p",
                "MemorySwapMax=0",
                "--",
            ]

        if self.cpus and shutil.which("taskset"):
            cmd += ["taskset", "-c", f"0-{self.cpus - 1}"]

        return cmd + argv

    def describe(self) -> str:
        parts = []
        parts.append(f"mem={self.mem_limit}" if self.mem_limit else "mem=unbounded")
        parts.append(f"cpus=0-{self.cpus - 1}" if self.cpus else "cpus=all")
        if not self.enforce:
            parts.append("unenforced")
        return " ".join(parts)


def classify_exit(returncode: int) -> str | None:
    """Map a subprocess exit code to a result status, or None if it looks normal.

    systemd reports an OOM-killed scope as 137; a directly signalled child comes
    back as -9 through subprocess. Both mean the same thing here.
    """
    if returncode in (OOM_EXIT_CODE, -9):
        return "oom"
    if returncode != 0:
        return "error"
    return None
