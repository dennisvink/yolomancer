def yolomancer_tool():
    return {
        "name": "workshop_exercise",
        "description": "Generate a short vibe-coding workshop exercise brief. This is a sample Python tool participants can re-imagine or replace.",
        "parameters": {
            "type": "object",
            "properties": {
                "topic": {"type": "string"},
                "audience": {"type": "string"},
                "duration_minutes": {"type": "integer"},
            },
            "additionalProperties": False,
        },
    }


def _clamp(value, minimum, maximum):
    try:
        value = int(value)
    except Exception:
        value = minimum
    return max(minimum, min(maximum, value))


def run(args):
    topic = args.get("topic") or "agentic CLI redesign"
    audience = args.get("audience") or "experienced agentic AI users"
    duration_minutes = _clamp(args.get("duration_minutes", 20), 5, 90)

    return {
        "ok": True,
        "topic": topic,
        "audience": audience,
        "duration_minutes": duration_minutes,
        "brief": "Re-imagine one slice of the CLI for {audience}, focused on {topic}.".format(
            audience=audience,
            topic=topic,
        ),
        "constraints": [
            "Keep the change small enough to implement and verify during the session.",
            "Expose at least one local tool or command that makes the agent more capable.",
            "Write down the product bet before coding, then compare the result to that bet.",
        ],
        "suggested_flow": [
            "Inspect the current CLI behavior.",
            "Pick one user workflow to make sharper.",
            "Ask the agent to implement the smallest useful version.",
            "Run the CLI or tests and capture what changed.",
        ],
    }
