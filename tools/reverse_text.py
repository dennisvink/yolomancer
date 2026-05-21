def yolomancer_tool():
    return {
        "name": "reverse_text",
        "description": "Reverse a string and report its length. This sample shows the tools/*.py format for local Python tools.",
        "parameters": {
            "type": "object",
            "properties": {
                "text": {
                    "type": "string",
                    "description": "Text to reverse.",
                },
            },
            "required": ["text"],
            "additionalProperties": False,
        },
    }

def run(args):
    text = args.get("text", "")
    return {
        "ok": True,
        "text": text[::-1],
        "length": len(text),
    }
