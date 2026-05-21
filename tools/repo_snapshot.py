def yolomancer_tool():
    return {
        "name": "repo_snapshot",
        "description": "Return a compact local workspace snapshot with sampled paths and file-extension counts. This is a sample Python tool for the workshop.",
        "parameters": {
            "type": "object",
            "properties": {
                "max_entries": {
                    "type": "integer",
                    "description": "Maximum number of sample paths to return (1-1000).",
                },
            },
            "additionalProperties": False,
        },
    }

import os


SKIP_DIRS = {".git", "target", "node_modules"}


def _clamp(value, minimum, maximum):
    try:
        value = int(value)
    except Exception:
        value = minimum
    return max(minimum, min(maximum, value))


def run(args):
    max_entries = _clamp(args.get("max_entries", 120), 1, 1000)
    root = os.getcwd()
    sample_entries = []
    extension_counts = {}
    total_files = 0

    for current_root, dirs, files in os.walk(root):
        dirs[:] = sorted(name for name in dirs if name not in SKIP_DIRS)
        files = sorted(files)
        relative_root = os.path.relpath(current_root, root)
        if relative_root == ".":
            relative_root = ""

        for directory in dirs:
            if len(sample_entries) < max_entries:
                sample_entries.append(os.path.join(relative_root, directory).strip(os.sep))

        for filename in files:
            total_files += 1
            _, ext = os.path.splitext(filename)
            ext = ext[1:] if ext else "(none)"
            extension_counts[ext] = extension_counts.get(ext, 0) + 1
            if len(sample_entries) < max_entries:
                sample_entries.append(os.path.join(relative_root, filename).strip(os.sep))

    return {
        "ok": True,
        "workspace_root": root,
        "sample_entries": sample_entries,
        "extension_counts": extension_counts,
        "total_files": total_files,
    }
