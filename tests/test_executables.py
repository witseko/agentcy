"""Verify which POSIX PATH entries can provide an executable."""

import os
from pathlib import Path

import pytest

from agentcy.executables import find_on_path


def test_find_on_path_preserves_absolute_order_and_symlink(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Prefer the first absolute match and preserve its executable name."""
    first = tmp_path / "first"
    second = tmp_path / "second"
    first.mkdir()
    second.mkdir()
    executable = second / "example"
    executable.touch(mode=0o700)
    alias = first / executable.name
    alias.symlink_to(executable)
    (tmp_path / executable.name).touch(mode=0o700)
    monkeypatch.chdir(tmp_path)
    monkeypatch.setenv("PATH", os.pathsep.join(["", "second", str(first), str(second)]))

    assert find_on_path(executable.name) == str(alias)


@pytest.mark.parametrize("path", ["", "bin", ":.:bin"])
def test_find_on_path_requires_an_absolute_directory(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, path: str
) -> None:
    """Empty and relative PATH entries never select an executable."""
    monkeypatch.chdir(tmp_path)
    monkeypatch.setenv("PATH", path)
    (tmp_path / "example").touch(mode=0o700)
    directory = tmp_path / "bin"
    directory.mkdir()
    (directory / "example").touch(mode=0o700)

    assert find_on_path("example") is None
    assert find_on_path("./example") is None


def test_find_on_path_preserves_os_default(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """Use the OS default executable directories when PATH is unset."""
    monkeypatch.delenv("PATH", raising=False)
    monkeypatch.setattr(os, "defpath", str(tmp_path))
    executable = tmp_path / "example"
    executable.touch(mode=0o700)

    assert find_on_path("example") == str(executable)
