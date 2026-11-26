"""Keep the committed member protocol files in step with the request models."""

import json
from pathlib import Path
from typing import get_args

import pytest
from jsonschema import Draft202012Validator
from pydantic import ValidationError

from agentcy.conversations import models
from agentcy.conversations.models import MemberRequest, contract_schema
from agentcy.mcp.core import REQUEST, rejects_operator_key

PROTOCOL = Path(__file__).parents[2] / "protocol"
# Actions that only the Go core accepts. They are in the committed schema and cases, but
# not in the Python models: the Python core rejects them, which is what makes a Go shim
# fall back to the older requests (protocol/README.md, "Go core only").
GO_ONLY_ACTIONS = {"listen": "Listen"}
REQUEST_MODELS = {
    get_args(model.model_fields["action"].annotation)[0]: model for model in get_args(MemberRequest)
}


def load_cases() -> list[dict]:
    """Read the committed accept and reject cases."""
    return json.loads((PROTOCOL / "member.v1.cases.json").read_bytes())


def case_line(case: dict) -> bytes:
    """Return the request line as the core reads it, newline included."""
    text = case["raw"] if "raw" in case else json.dumps(case["request"])
    return text.encode() + b"\n"


def python_schema(schema: dict) -> dict:
    """Return the committed schema without the Go-only actions."""
    go_only = set(GO_ONLY_ACTIONS.values())
    schema["$defs"] = {name: value for name, value in schema["$defs"].items() if name not in go_only}
    schema["oneOf"] = [ref for ref in schema["oneOf"] if ref["$ref"].split("/")[-1] not in go_only]
    return schema


def test_committed_schema_matches_the_models() -> None:
    """Fail when a model changes without regenerating protocol/member.v1.schema.json."""
    committed = json.loads((PROTOCOL / "member.v1.schema.json").read_bytes())
    assert python_schema(committed) == contract_schema()


def test_schema_is_valid_and_lists_every_action() -> None:
    """Check the schema against the metaschema and against the actions of the union."""
    schema = json.loads((PROTOCOL / "member.v1.schema.json").read_bytes())
    Draft202012Validator.check_schema(schema)
    assert len(REQUEST_MODELS) == 19
    assert len(schema["oneOf"]) == len(REQUEST_MODELS) + len(GO_ONLY_ACTIONS)


def test_cases_cover_every_action_and_field() -> None:
    """Require a minimal, a full, and a rejected case per action, and every field in the full one."""
    cases = load_cases()
    names = [case["name"] for case in cases]
    assert len(names) == len(set(names))
    by_name = {case["name"]: case for case in cases}
    for action, model in REQUEST_MODELS.items():
        minimal = by_name[f"{action}: minimal valid"]
        full = by_name[f"{action}: every optional field"]
        assert minimal["valid"] and full["valid"]
        assert set(full["request"]) == set(model.model_fields)
        assert any(case["action"] == action and not case["valid"] for case in cases)


def test_valid_examples_satisfy_the_schema() -> None:
    """Check that the minimal and full requests of every action pass the JSON schema."""
    schema = json.loads((PROTOCOL / "member.v1.schema.json").read_bytes())
    validator = Draft202012Validator(schema)
    for case in load_cases():
        if case["name"].endswith((": minimal valid", ": every optional field")):
            validator.validate(case["request"])


@pytest.mark.parametrize("case", load_cases(), ids=lambda case: case["name"][:80])
def test_case_matches_the_core_validator(case: dict) -> None:
    """Replay one case through the parser the core uses and compare the decision and result."""
    line = case_line(case)
    if case["action"] in GO_ONLY_ACTIONS:
        with pytest.raises(ValidationError):
            REQUEST.validate_json(line)
        return
    try:
        parsed = REQUEST.validate_json(line)
        member = models.REQUEST.validate_json(line)
    except ValidationError as error:
        assert not case["valid"]
        assert not rejects_operator_key(error)
        return
    assert case["valid"]
    assert type(parsed) is type(member)
    expected = case.get("normalised", case.get("request"))
    assert parsed.model_dump(mode="json") == expected
