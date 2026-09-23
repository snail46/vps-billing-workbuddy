#!/usr/bin/env python3
"""Checks that a web image can type-check from the files it actually copies.

A Docker build stage sees only what its Dockerfile COPYs. A file the build reads
but the Dockerfile never copies is invisible from the outside: the host build
passes and only the image build fails, in a job whose logs need authentication to
read. That is exactly how the Phase 0 Gate first broke — `tsconfig.base.json` was
missing from the image, so `tsc` aborted with TS5083 ("Cannot read file") and
cascaded into TS6142 ("--jsx is not set") for every shared component, while
`npm run build` on the host stayed green.

The check deliberately does not hardcode a file list. It parses the COPY
instructions out of the Dockerfile's build stage and stages exactly those, so it
follows the Dockerfile rather than drifting from it, and then runs the same
type-check the image build runs.

Usage:
    python scripts/check-docker-context.py <context-root> <dockerfile> <workspace>...

The context root is the directory the Dockerfile is built from (the `frontend`
directory for these images). The workspaces are checked in order; any that fails
makes the whole run exit non-zero.
"""

from __future__ import annotations

import shutil
import subprocess
import sys
from pathlib import Path

# Mirrors .dockerignore. Staging these would make the check pass for the wrong
# reason, since the real build context never contains them.
IGNORED_NAMES = {"node_modules", "dist", "coverage", ".vite"}

# The staging directory lives inside the context root rather than in the system
# temporary directory, so the staged tree resolves node_modules exactly as the
# image does: by walking up to the workspace root. Staging elsewhere would make
# every dependency import unresolvable and turn the check into a false alarm.
STAGING_DIRECTORY_NAME = ".docker-context-check"


def build_stage_instructions(dockerfile: Path) -> list[str]:
    """Returns the instructions of the first stage, with continuations joined."""
    instructions: list[str] = []
    pending = ""
    stages_seen = 0

    for raw_line in dockerfile.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue

        # A backslash joins the next line onto this instruction.
        if line.endswith("\\"):
            pending += line[:-1].rstrip() + " "
            continue
        line = (pending + line).strip()
        pending = ""

        if line.upper().startswith("FROM "):
            stages_seen += 1
            # Only the build stage is staged. The runtime stage copies from the
            # build stage rather than from the context.
            if stages_seen > 1:
                break
            continue

        if stages_seen == 1:
            instructions.append(line)

    return instructions


def copy_sources(instructions: list[str]) -> list[tuple[list[str], str]]:
    """Extracts (sources, destination) pairs from the COPY instructions."""
    copies: list[tuple[list[str], str]] = []

    for instruction in instructions:
        if not instruction.upper().startswith("COPY "):
            continue

        tokens = instruction.split()[1:]
        # `COPY --from=...` reads from another stage, not from the context.
        tokens = [token for token in tokens if not token.startswith("--")]
        if len(tokens) < 2:
            continue

        *sources, destination = tokens
        copies.append((sources, destination))

    return copies


def should_ignore(path: Path) -> bool:
    return any(part in IGNORED_NAMES for part in path.parts)


def stage_copy(context_root: Path, sources: list[str], destination: str, stage_root: Path) -> None:
    """Applies one COPY instruction to the staging directory.

    Docker's semantics differ by source kind: a directory's *contents* are
    copied, while a file is copied into the destination directory when that
    directory is the target. Getting this wrong would stage a different file set
    than the image build sees, which would defeat the check.
    """
    destination_path = (stage_root / destination.lstrip("./")).resolve()
    destination_is_directory = destination.endswith("/") or destination in (".", "./")

    for source in sources:
        source_path = context_root / source
        if not source_path.exists():
            raise FileNotFoundError(f"COPY source does not exist in the context: {source}")

        if source_path.is_dir():
            shutil.copytree(
                source_path,
                destination_path,
                dirs_exist_ok=True,
                ignore=shutil.ignore_patterns(*IGNORED_NAMES),
            )
        else:
            destination_path.mkdir(parents=True, exist_ok=True)
            target = destination_path / source_path.name if destination_is_directory else destination_path
            shutil.copy2(source_path, target)


def type_check(stage_root: Path, workspace: str, compiler: Path) -> int:
    """Runs the image build's type-check against the staged tree."""
    tsconfig = stage_root / workspace / "tsconfig.json"
    if not tsconfig.exists():
        print(f"  FAIL  {workspace}: no tsconfig.json was staged from the Dockerfile")
        return 1

    node = shutil.which("node")
    if node is None:
        print("  skip  node is not on PATH")
        return 0

    if not compiler.exists():
        print("  skip  TypeScript is not installed (run npm install in the context root)")
        return 0

    # The staged tree resolves node_modules by walking up into the real context
    # root, which is also how the image finds the ones `npm ci` installed. The
    # point of the check is configuration completeness, not dependency
    # installation, so the host's compiler is used deliberately.
    result = subprocess.run(
        [node, str(compiler), "--noEmit", "-p", str(tsconfig)],
        capture_output=True,
        text=True,
    )
    if result.returncode == 0:
        return 0

    print(f"  FAIL  {workspace} does not type-check from the Docker build context")
    for line in (result.stdout + result.stderr).splitlines()[:12]:
        print(f"        {line}")
    return 1


def main(argv: list[str]) -> int:
    if len(argv) < 4:
        print(__doc__)
        return 2

    context_root = Path(argv[1]).resolve()
    dockerfile = Path(argv[2]).resolve()
    workspaces = argv[3:]

    instructions = build_stage_instructions(dockerfile)
    copies = copy_sources(instructions)
    if not copies:
        print(f"  FAIL  no COPY instruction found in the build stage of {dockerfile}")
        return 1

    stage_root = context_root / STAGING_DIRECTORY_NAME
    shutil.rmtree(stage_root, ignore_errors=True)

    # Derived from the context root rather than from this file's own location, so
    # the script behaves the same however it is invoked.
    compiler = context_root / "node_modules" / "typescript" / "bin" / "tsc"

    try:
        for sources, destination in copies:
            try:
                stage_copy(context_root, sources, destination, stage_root)
            except FileNotFoundError as error:
                print(f"  FAIL  {dockerfile.name}: {error}")
                return 1

        staged = sorted({source for sources, _ in copies for source in sources})
        print(f"  staged from {dockerfile.name}: {', '.join(staged)}")

        failures = 0
        for workspace in workspaces:
            if type_check(stage_root, workspace, compiler) != 0:
                failures = 1
        return failures
    finally:
        # The staging tree is inside the context, so leaving it behind would end
        # up in the next image build.
        shutil.rmtree(stage_root, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main(sys.argv))
