"""Keep the committed operator protocol files in step with the request and result models."""

import json
from pathlib import Path
from typing import get_args

from jsonschema import Draft202012Validator

from agents_chat.conversations.operator_models import CONTRACT, contract_schema
from agents_chat.mcp.core import REQUEST

PROTOCOL = Path(__file__).parents[2] / "protocol"


def test_committed_schema_matches_the_models() -> None:
    """Fail when a model changes without regenerating protocol/operator.v1.schema.json."""
    committed = json.loads((PROTOCOL / "operator.v1.schema.json").read_bytes())
    assert committed == contract_schema()


def test_examples_cover_every_action_and_validate() -> None:
    """Validate each documented request and result against the schema and the core parser."""
    schema = json.loads((PROTOCOL / "operator.v1.schema.json").read_bytes())
    Draft202012Validator.check_schema(schema)
    examples = json.loads((PROTOCOL / "operator.v1.examples.json").read_bytes())
    results = {
        get_args(request.model_fields["action"].annotation)[0]: result
        for request, result in CONTRACT.items()
    }
    assert sorted(item["action"] for item in examples) == sorted(results)
    for item in examples:
        Draft202012Validator(schema).validate(item["request"])
        assert REQUEST.validate_python(item["request"]).action == item["action"]
        result_model = results[item["action"]]
        result_schema = {"$ref": f"#/$defs/{result_model.__name__}", "$defs": schema["$defs"]}
        Draft202012Validator(result_schema).validate(item["result"])
        result_model.model_validate(item["result"])
