"""Check the packaged skills that the main server publishes through the MCP Skills extension."""

import asyncio
import hashlib
from collections.abc import Awaitable, Callable
from importlib.resources import files
from secrets import token_hex
from typing import Any

import pytest
from mcp import Client
from mcp.shared.exceptions import MCPError
from mcp.types import INVALID_PARAMS, Request, ServerCapabilities, TextResourceContents
from pydantic import TypeAdapter

from agentcy.mcp import server, skills

MODERN = "2026-07-28"
SKILL_NAMES = {"lead-conversation", "join-conversation"}
RAW_RESULT = TypeAdapter(dict[str, Any])


@pytest.fixture
def app(monkeypatch: pytest.MonkeyPatch) -> server.BridgeServer:
    """Build the main stdio server without starting its stdio loop."""
    registered: list[server.BridgeServer] = []

    def capture(built: server.BridgeServer, transport: str) -> None:
        """Keep the built server instead of running it."""
        registered.append(built)

    monkeypatch.setattr(server.BridgeServer, "run", capture)
    server.serve(token_hex(32))
    return registered[0]


def run_client(
    app: server.BridgeServer, mode: str, action: Callable[[Client], Awaitable[Any]]
) -> Any:
    """Connect an in-process client in the given protocol mode and run one action."""

    async def connect() -> Any:
        async with Client(app, mode=mode) as client:
            return await action(client)

    return asyncio.run(connect())


async def send(client: Client, method: str, params: dict[str, Any] | None = None) -> dict:
    """Send an extension request and return the raw result."""
    request = Request[dict[str, Any], str](method=method, params=params or {})
    return await client.session.send_request(request, RAW_RESULT)


async def read_text(client: Client, uri: str) -> TextResourceContents:
    """Read one resource and return its single text content."""
    result = await client.read_resource(uri)
    (content,) = result.contents
    assert isinstance(content, TextResourceContents)
    return content


def packaged_bytes(uri: str) -> bytes:
    """Read the packaged file behind a skill URI, independent of the extension code."""
    path = uri.removeprefix("skill://")
    return files("agentcy.mcp").joinpath("skills", *path.split("/")).read_bytes()


def file_frontmatter(name: str) -> dict[str, str]:
    """Extract the two frontmatter fields of a packaged SKILL.md in their YAML meaning."""
    lines = packaged_bytes(f"skill://{name}/SKILL.md").decode("utf-8").splitlines()
    end = lines.index("---", 1)
    fields = dict(line.split(": ", 1) for line in lines[1:end] if not line.startswith(" "))
    assert fields["description"] == ">-"
    folded = [line.strip() for line in lines[1:end] if line.startswith(" ")]
    return {"name": fields["name"], "description": " ".join(folded)}


def test_list_returns_every_skill_with_cache_hints(app: server.BridgeServer) -> None:
    """A 2026-07-28 client lists both skills with the required result fields."""
    listed = run_client(app, "auto", lambda client: send(client, "skills/list"))
    assert listed["resultType"] == "complete"
    assert listed["ttlMs"] == skills.CACHE_TTL_MS
    assert listed["cacheScope"] == "public"
    assert "nextCursor" not in listed
    assert {entry["uri"] for entry in listed["skills"]} == {
        f"skill://{name}/SKILL.md" for name in SKILL_NAMES
    }


def test_entries_match_the_served_bytes(app: server.BridgeServer) -> None:
    """Every manifest digest and size matches the bytes that resources/read serves."""

    async def check(client: Client) -> None:
        listed = await send(client, "skills/list")
        for entry in listed["skills"]:
            name = entry["uri"].removeprefix("skill://").removesuffix("/SKILL.md")
            assert entry["frontmatter"] == file_frontmatter(name)
            assert entry["resources"][0]["uri"] == entry["uri"]
            for resource in entry["resources"]:
                content = await read_text(client, resource["uri"])
                served = content.text.encode("utf-8")
                assert content.mime_type == "text/markdown"
                assert served == packaged_bytes(resource["uri"])
                assert resource["size"] == len(served)
                assert resource["digest"] == f"sha256:{hashlib.sha256(served).hexdigest()}"

    run_client(app, "auto", check)


def test_get_returns_the_listed_entry(app: server.BridgeServer) -> None:
    """skills/get returns the same entry as skills/list for each skill URI."""

    async def check(client: Client) -> None:
        listed = await send(client, "skills/list")
        for entry in listed["skills"]:
            fetched = await send(client, "skills/get", {"uri": entry["uri"]})
            assert fetched["resultType"] == "complete"
            assert fetched["ttlMs"] == skills.CACHE_TTL_MS
            assert fetched["cacheScope"] == "public"
            assert fetched["skill"] == entry

    run_client(app, "auto", check)


@pytest.mark.parametrize(
    "uri",
    [
        "skill://missing/SKILL.md",
        "skill://lead-conversation/../join-conversation/SKILL.md",
        "file:///etc/passwd",
    ],
)
def test_unknown_uri_is_invalid_params(app: server.BridgeServer, uri: str) -> None:
    """An unknown skill URI fails with -32602 on skills/get and resources/read."""

    async def check(client: Client) -> None:
        with pytest.raises(MCPError) as get_error:
            await send(client, "skills/get", {"uri": uri})
        assert get_error.value.error.code == INVALID_PARAMS
        with pytest.raises(MCPError) as read_error:
            await client.read_resource(uri)
        assert read_error.value.error.code == INVALID_PARAMS

    run_client(app, "auto", check)


def test_modern_client_sees_resources_and_the_extension(app: server.BridgeServer) -> None:
    """The server declares the resources capability and the Skills extension."""

    async def capabilities_of(client: Client) -> ServerCapabilities:
        assert client.protocol_version == MODERN
        return client.server_capabilities

    capabilities = run_client(app, "auto", capabilities_of)
    assert capabilities.resources is not None
    assert capabilities.extensions == {skills.EXTENSION_ID: {}}


def test_legacy_client_still_works(app: server.BridgeServer) -> None:
    """A legacy client initializes, lists tools, and reads the skills as plain resources."""

    async def check(client: Client) -> None:
        assert client.protocol_version != MODERN
        tools = await client.list_tools()
        assert "join_conversation" in {tool.name for tool in tools.tools}
        resources = await client.list_resources()
        uris = {str(resource.uri) for resource in resources.resources}
        assert uris == {f"skill://{name}/SKILL.md" for name in SKILL_NAMES}
        for uri in uris:
            content = await read_text(client, uri)
            assert content.text.encode("utf-8") == packaged_bytes(uri)
        listed = await send(client, "skills/list")
        assert "ttlMs" not in listed
        assert "cacheScope" not in listed

    run_client(app, "legacy", check)


def test_skills_stay_within_host_limits() -> None:
    """Each packaged skill stays within the Claude Code limits for MCP skills."""
    loaded = skills.load_packaged_skills()
    assert {skill.frontmatter["name"] for skill in loaded} == SKILL_NAMES
    assert len(loaded) <= skills.MAX_SKILLS
    for skill in loaded:
        assert all(file.size <= skills.MAX_FILE_BYTES for file in skill.files)
        assert sum(file.size for file in skill.files) <= skills.MAX_SKILL_BYTES


def test_oversized_file_fails_at_load(monkeypatch: pytest.MonkeyPatch) -> None:
    """A skill file over the size limit stops the server from starting."""
    monkeypatch.setattr(skills, "MAX_FILE_BYTES", 100)
    with pytest.raises(ValueError, match="exceeds 100 bytes"):
        skills.load_skill("lead-conversation")


def test_name_must_equal_directory() -> None:
    """A frontmatter name that differs from the directory name is rejected."""
    with pytest.raises(ValueError, match="frontmatter name"):
        skills._validate_frontmatter({"name": "other", "description": "Text."}, "lead-conversation")


def test_parser_reads_plain_and_folded_values() -> None:
    """The parser joins a folded block into one line and keeps plain values unchanged."""
    text = "---\nname: demo\ndescription: >-\n  First line.\n  Second line.\n---\n# Body\n"
    assert skills.parse_frontmatter(text) == {
        "name": "demo",
        "description": "First line. Second line.",
    }


@pytest.mark.parametrize(
    "frontmatter",
    [
        "name: demo\ndescription: 'quoted'",
        "name: demo\nallowed-tools:\n  - Bash",
        "name: demo\ndescription: >-\n  One.\n\n  Two.",
        "name: demo\ndescription: >-\n  One.\n    Two.",
        "name: demo\nversion: 1",
        "name: demo\nenabled: true",
        "name: demo\ndescription: a: b",
        "name: demo\nname: again",
    ],
)
def test_parser_rejects_other_yaml(frontmatter: str) -> None:
    """Any YAML form beyond the two supported ones fails loudly."""
    with pytest.raises(ValueError):
        skills.parse_frontmatter(f"---\n{frontmatter}\n---\n")
