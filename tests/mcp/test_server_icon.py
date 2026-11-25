"""Check the icon advertised by the Agentcy stdio server."""

import asyncio
import struct
import sys
from base64 import b64decode
from importlib.metadata import version
from pathlib import Path

from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
from mcp.types import InitializeResult

from agentcy.mcp.server import LISTEN


async def initialize(workspace: Path) -> InitializeResult:
    """Connect to the normal CLI and return its MCP initialization result."""
    parameters = StdioServerParameters(
        command=sys.executable,
        args=[
            "-c",
            "from agentcy.tools.cli import main; main()",
            "--workspace",
            str(workspace),
        ],
    )
    async with stdio_client(parameters) as streams, ClientSession(*streams) as session:
        return await asyncio.wait_for(session.initialize(), timeout=10)


def test_stdio_server_advertises_png_icon(tmp_path: Path) -> None:
    """Provide a self-contained 64-pixel icon, the version, and base instructions."""
    result = asyncio.run(initialize(tmp_path))
    server = result.server_info
    assert server.name == "agentcy"
    assert result.instructions is not None
    assert server.version == version("agentcy")
    assert "load lead-conversation before you create or lead a conversation" in result.instructions
    assert "post one short kind='note' without 'to' that introduces you" in result.instructions
    assert f"When idle, {LISTEN} and follow 'next'" in result.instructions
    assert "open unless your user explicitly asks to close it" in result.instructions
    assert "Keep your token private; never post credentials" in result.instructions
    assert "runs in a VM or a hosted sandbox, do not create a conversation" in result.instructions
    assert server.icons is not None
    (icon,) = server.icons
    assert icon.mime_type == "image/png"
    assert icon.sizes == ["64x64"]
    prefix = "data:image/png;base64,"
    assert icon.src.startswith(prefix)
    png = b64decode(icon.src.removeprefix(prefix), validate=True)
    assert png.startswith(b"\x89PNG\r\n\x1a\n")
    assert struct.unpack_from(">II", png, 16) == (64, 64)
