from __future__ import annotations

import os
import secrets
import tempfile
from pathlib import Path


def private_directory(path: Path) -> None:
    os.chown(path, 0, 0)
    path.chmod(0o700)


def blinded_order(candidate_ids: list[str]) -> list[str]:
    order = candidate_ids.copy()
    secrets.SystemRandom().shuffle(order)
    return order


def isolated_run_paths(job_root: Path) -> tuple[Path, Path]:
    parent = job_root / "runs"
    parent.mkdir(mode=0o711, exist_ok=True)
    parent.chmod(0o711)
    scratch = Path(tempfile.mkdtemp(prefix="run-", dir=parent))
    return scratch, scratch / "cache"
