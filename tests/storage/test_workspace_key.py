"""Verify workspace identity with ordinary repositories and linked worktrees."""

import hashlib
import os
import subprocess
from pathlib import Path

import pytest

from agentcy.executables import find_on_path
from agentcy.storage.database import workspace_key


def run_git(git: str, directory: Path, *arguments: str) -> subprocess.CompletedProcess[str]:
    """Run a Git fixture command with no inherited repository or user config."""
    return subprocess.run(  # noqa: S603 - installed Git and fixed test arguments
        [git, "-C", str(directory), *arguments],
        env={"PATH": os.defpath, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull},
        capture_output=True,
        check=False,
        text=True,
        timeout=10,
    )


@pytest.fixture
def git_repositories(tmp_path: Path) -> tuple[str, Path, Path]:
    """Create two independent empty repositories with the installed Git."""
    git = find_on_path("git")
    if git is None:
        pytest.skip("Git is unavailable")
    project = tmp_path / "project"
    other = tmp_path / "other"
    for repository in (project, other):
        repository.mkdir()
        init = run_git(git, repository, "init", "--quiet", "--template=")
        assert init.returncode == 0, init.stderr
    return git, project, other


def set_repo_environment(monkeypatch: pytest.MonkeyPatch, other: Path) -> None:
    """Leave the parent environment pointing at a separate legitimate repository."""
    monkeypatch.setenv("GIT_DIR", str(other / ".git"))
    monkeypatch.setenv("GIT_WORK_TREE", str(other))
    monkeypatch.setenv("GIT_COMMON_DIR", str(other / ".git"))


def test_workspace_key_uses_the_requested_repository(
    git_repositories: tuple[str, Path, Path], monkeypatch: pytest.MonkeyPatch
) -> None:
    """Use the requested workspace's Git directory despite inherited Git variables."""
    _, project, other = git_repositories
    set_repo_environment(monkeypatch, other)

    assert (
        workspace_key(project)
        == hashlib.sha256(os.fsencode(project.resolve() / ".git")).hexdigest()
    )
    assert workspace_key(project) != workspace_key(other)


def test_linked_worktree_shares_requested_repository_key(
    git_repositories: tuple[str, Path, Path], monkeypatch: pytest.MonkeyPatch
) -> None:
    """Use the main checkout's identity for a linked worktree without creating commits."""
    git, project, other = git_repositories
    help_page = run_git(git, project, "worktree", "add", "-h")
    if "--orphan" not in help_page.stdout + help_page.stderr:
        pytest.skip("Git lacks worktree add --orphan")
    linked = project.parent / "linked"
    added = run_git(git, project, "worktree", "add", "--orphan", "-b", "linked", str(linked))
    assert added.returncode == 0, added.stderr
    set_repo_environment(monkeypatch, other)

    assert workspace_key(linked) == workspace_key(project)
    assert workspace_key(linked) != workspace_key(other)


def test_workspace_key_falls_back_to_canonical_path(
    git_repositories: tuple[str, Path, Path], monkeypatch: pytest.MonkeyPatch
) -> None:
    """Use a directory's own identity when inherited variables point to a repository."""
    _, project, other = git_repositories
    directory = project.parent / "directory"
    directory.mkdir()
    set_repo_environment(monkeypatch, other)

    assert workspace_key(directory) == hashlib.sha256(os.fsencode(directory.resolve())).hexdigest()
