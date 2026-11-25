"""Load the packaged skills and publish them through the MCP Skills extension.

The extension (`io.modelcontextprotocol/skills`, SEP-2640) adds `skills/list` and
`skills/get`. Each skill file is also an ordinary text resource, so a client without
the extension can still read it with `resources/read`.
"""

import hashlib
import re
from collections.abc import Sequence
from dataclasses import dataclass
from importlib.resources import files
from importlib.resources.abc import Traversable
from typing import Any

from mcp.server.context import HandlerResult, ServerRequestContext
from mcp.server.extension import Extension, MethodBinding, ResourceBinding
from mcp.server.mcpserver.resources import TextResource
from mcp.shared.exceptions import MCPError
from mcp.types import INVALID_PARAMS, PaginatedRequestParams, RequestParams
from mcp_types.version import MODERN_PROTOCOL_VERSIONS

EXTENSION_ID = "io.modelcontextprotocol/skills"
METHOD_LIST = "skills/list"
METHOD_GET = "skills/get"
PACKAGED_SKILLS = ("lead-conversation", "join-conversation")
SKILL_FILE = "SKILL.md"
MARKDOWN = "text/markdown"
# Claude Code refuses larger skills from an MCP server.
MAX_FILE_BYTES = 100_000
MAX_SKILL_BYTES = 400_000
MAX_SKILLS = 100
# Agent Skills specification limits for the two required frontmatter fields.
SKILL_NAME = re.compile(r"[a-z0-9]+(?:-[a-z0-9]+)*")
MAX_NAME_LENGTH = 64
MAX_DESCRIPTION_LENGTH = 1024
# The skills are the same for every caller and change only with a new release.
CACHE_TTL_MS = 300_000
CACHE_SCOPE = "public"

FRONTMATTER_FENCE = "---"
FIELD = re.compile(r"(?P<key>[a-z][a-z0-9_-]*): (?P<value>.+)")
FOLDED_MARKER = ">-"
# A plain YAML value that starts with one of these is not a simple string.
YAML_INDICATORS = tuple("'\"[]{}>|*&!%@#`,?:-")
# YAML reads these plain values as booleans or null, not as strings.
NON_STRING_WORDS = {"true", "false", "yes", "no", "on", "off", "null", "~"}


@dataclass(frozen=True)
class SkillFile:
    """One served file of a skill, with the digest and size of its exact bytes."""

    uri: str
    text: str
    digest: str
    size: int


@dataclass(frozen=True)
class Skill:
    """One packaged skill: its SKILL.md URI, its frontmatter, and all of its files."""

    uri: str
    frontmatter: dict[str, str]
    files: tuple[SkillFile, ...]

    def entry(self) -> dict[str, Any]:
        """Return the wire form of this skill for `skills/list` and `skills/get`."""
        return {
            "uri": self.uri,
            "frontmatter": dict(self.frontmatter),
            "resources": [
                {"uri": file.uri, "digest": file.digest, "size": file.size} for file in self.files
            ],
        }


class ListSkillsParams(PaginatedRequestParams):
    """Parameters for `skills/list`. The listing has one page, so a cursor is ignored."""


class GetSkillParams(RequestParams):
    """Parameters for `skills/get`."""

    uri: str


def _plain_value(value: str, key: str) -> str:
    """Return a one-line plain YAML value, or raise if YAML would read it differently."""
    if (
        value.startswith(YAML_INDICATORS)
        or ": " in value
        or " #" in value
        or value.endswith(":")
        or value.lower() in NON_STRING_WORDS
        or _is_number(value)
    ):
        raise ValueError(f"frontmatter field {key!r} needs YAML features this parser rejects")
    return value


def _is_number(value: str) -> bool:
    """Tell whether YAML would read a plain value as a number."""
    try:
        float(value)
    except ValueError:
        return False
    return True


def _folded_value(lines: list[str], start: int, key: str) -> tuple[str, int]:
    """Join the indented lines of a `>-` block into one string.

    Returns:
        The folded text and the index of the first line after the block.
    """
    end = start
    while end < len(lines) and lines[end].startswith(" "):
        end += 1
    block = lines[start:end]
    if not block:
        raise ValueError(f"frontmatter field {key!r} has an empty folded block")
    indent = len(block[0]) - len(block[0].lstrip(" "))
    for line in block:
        if not line.strip() or len(line) - len(line.lstrip(" ")) != indent:
            raise ValueError(f"frontmatter field {key!r} needs YAML features this parser rejects")
    return " ".join(line.strip() for line in block), end


def parse_frontmatter(text: str) -> dict[str, str]:
    """Parse the YAML frontmatter of a SKILL.md file.

    The project has no YAML dependency, so this accepts only the two forms the
    packaged skills use: `key: value` and `key: >-` with one indented paragraph.

    Raises:
        ValueError: If the frontmatter is missing or uses any other YAML form.
    """
    lines = text.splitlines()
    if not lines or lines[0] != FRONTMATTER_FENCE or FRONTMATTER_FENCE not in lines[1:]:
        raise ValueError("SKILL.md must start with a frontmatter block between '---' lines")
    body = lines[1 : lines.index(FRONTMATTER_FENCE, 1)]
    fields: dict[str, str] = {}
    index = 0
    while index < len(body):
        match = FIELD.fullmatch(body[index])
        if match is None:
            raise ValueError(f"frontmatter line {body[index]!r} is not 'key: value'")
        key, value = match["key"], match["value"]
        if key in fields:
            raise ValueError(f"frontmatter field {key!r} appears twice")
        if value == FOLDED_MARKER:
            fields[key], index = _folded_value(body, index + 1, key)
        else:
            fields[key] = _plain_value(value, key)
            index += 1
    return fields


def _validate_frontmatter(frontmatter: dict[str, str], directory: str) -> None:
    """Check the required fields and that the skill name equals its directory name."""
    name = frontmatter.get("name", "")
    description = frontmatter.get("description", "")
    if name != directory:
        raise ValueError(f"skill {directory!r} has frontmatter name {name!r}")
    if not SKILL_NAME.fullmatch(name) or len(name) > MAX_NAME_LENGTH:
        raise ValueError(f"skill name {name!r} breaks the Agent Skills naming rules")
    if not description or len(description) > MAX_DESCRIPTION_LENGTH:
        raise ValueError(f"skill {name!r} needs a description of 1 to 1024 characters")


def _relative_files(directory: Traversable, prefix: str = "") -> list[str]:
    """List the paths of all files below a packaged directory, relative to it."""
    paths = []
    for child in directory.iterdir():
        path = f"{prefix}{child.name}"
        if child.is_dir():
            paths.extend(_relative_files(child, f"{path}/"))
        else:
            paths.append(path)
    return paths


def _load_file(skill_directory: Traversable, name: str, path: str) -> SkillFile:
    """Read one skill file and compute its digest and size from the bytes served."""
    if not path.endswith(".md"):
        raise ValueError(f"skill {name!r} file {path!r} is not Markdown")
    data = skill_directory.joinpath(*path.split("/")).read_bytes()
    if len(data) > MAX_FILE_BYTES:
        raise ValueError(f"skill {name!r} file {path!r} exceeds {MAX_FILE_BYTES} bytes")
    return SkillFile(
        uri=f"skill://{name}/{path}",
        text=data.decode("utf-8"),
        digest=f"sha256:{hashlib.sha256(data).hexdigest()}",
        size=len(data),
    )


def load_skill(name: str) -> Skill:
    """Load one packaged skill from the agentcy.mcp package data and validate it.

    Raises:
        ValueError: If the skill breaks the frontmatter rules or a size limit.
    """
    skill_directory = files("agentcy.mcp").joinpath("skills", name)
    paths = sorted(_relative_files(skill_directory), key=lambda path: (path != SKILL_FILE, path))
    if SKILL_FILE not in paths:
        raise ValueError(f"skill {name!r} has no {SKILL_FILE}")
    skill_files = tuple(_load_file(skill_directory, name, path) for path in paths)
    if sum(file.size for file in skill_files) > MAX_SKILL_BYTES:
        raise ValueError(f"skill {name!r} exceeds {MAX_SKILL_BYTES} bytes")
    frontmatter = parse_frontmatter(skill_files[0].text)
    _validate_frontmatter(frontmatter, name)
    return Skill(uri=skill_files[0].uri, frontmatter=frontmatter, files=skill_files)


def load_packaged_skills() -> tuple[Skill, ...]:
    """Load every skill that agentcy ships."""
    if len(PACKAGED_SKILLS) > MAX_SKILLS:
        raise ValueError(f"a server can publish at most {MAX_SKILLS} skills")
    return tuple(load_skill(name) for name in PACKAGED_SKILLS)


def _cacheable(result: dict[str, Any], protocol_version: str) -> dict[str, Any]:
    """Add the 2026-07-28 cache hints, which older protocol versions do not define."""
    if protocol_version in MODERN_PROTOCOL_VERSIONS:
        return {**result, "ttlMs": CACHE_TTL_MS, "cacheScope": CACHE_SCOPE}
    return result


class SkillsExtension(Extension):
    """Serve `skills/list`, `skills/get`, and one text resource per skill file.

    Skill URIs resolve only against the in-memory manifest built at load time.
    """

    identifier = EXTENSION_ID

    def __init__(self, skills: Sequence[Skill]) -> None:
        self._skills = {skill.uri: skill for skill in skills}

    def resources(self) -> Sequence[ResourceBinding]:
        """Expose every skill file as a Markdown text resource."""
        bindings = []
        for skill in self._skills.values():
            for file in skill.files:
                is_skill_file = file.uri == skill.uri
                resource = TextResource(
                    uri=file.uri,
                    name=skill.frontmatter["name"] if is_skill_file else file.uri,
                    description=skill.frontmatter["description"] if is_skill_file else None,
                    mime_type=MARKDOWN,
                    text=file.text,
                )
                bindings.append(ResourceBinding(resource))
        return bindings

    def methods(self) -> Sequence[MethodBinding]:
        """Bind the two methods the extension requires."""
        return [
            MethodBinding(METHOD_LIST, ListSkillsParams, self._list_skills),
            MethodBinding(METHOD_GET, GetSkillParams, self._get_skill),
        ]

    async def _list_skills(
        self, ctx: ServerRequestContext[Any, Any], params: ListSkillsParams
    ) -> HandlerResult:
        """Return every skill on one page."""
        skills = [skill.entry() for skill in self._skills.values()]
        return _cacheable({"skills": skills}, ctx.protocol_version)

    async def _get_skill(
        self, ctx: ServerRequestContext[Any, Any], params: GetSkillParams
    ) -> HandlerResult:
        """Return the skill whose SKILL.md URI the client names."""
        skill = self._skills.get(params.uri)
        if skill is None:
            raise MCPError(code=INVALID_PARAMS, message=f"No skill is served at {params.uri}")
        return _cacheable({"skill": skill.entry()}, ctx.protocol_version)


def skills_extension() -> SkillsExtension:
    """Build the extension for the packaged skills."""
    return SkillsExtension(load_packaged_skills())
