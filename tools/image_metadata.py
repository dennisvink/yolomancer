"""Read image metadata without changing the file. Requires Pillow.

Install tools/requirements.txt into the Python environment used by Yolomancer.
EXIF values are claims made by the file, not proof of authenticity.
"""


def yolomancer_tool():
    return {
        "name": "image_metadata",
        "description": "Read an image's format, dimensions, EXIF dates, camera, software and GPS metadata. Read-only; missing or edited metadata does not establish fraud or AI generation. Dates are returned as stored, without assuming a timezone.",
        "parameters": {
            "type": "object",
            "properties": {
                "path": {"type": "string", "description": "Local image path, relative to the workspace or absolute."},
            },
            "required": ["path"],
            "additionalProperties": False,
        },
    }


def _value(value):
    """Convert Pillow values (including EXIF rationals) to bounded JSON."""
    import math

    if value is None or isinstance(value, (bool, int)):
        return value
    if isinstance(value, bytes):
        return value[:1024].decode("utf-8", errors="replace").rstrip("\x00")
    if isinstance(value, str):
        return value[:1024].rstrip("\x00")
    if isinstance(value, (tuple, list)):
        return [_value(v) for v in value[:16]]
    try:
        number = float(value)
        return number if math.isfinite(number) else None
    except (TypeError, ValueError, ZeroDivisionError):
        return None


def run(args):
    from pathlib import Path
    import warnings

    if not isinstance(args, dict) or not isinstance(args.get("path"), str) or not args["path"].strip():
        return {"ok": False, "error": "Provide a non-empty image path."}
    try:
        from PIL import Image, ExifTags, UnidentifiedImageError
    except ImportError:
        return {"ok": False, "error": "Pillow is missing. Install tools/requirements.txt into the Python environment used to launch Yolomancer."}

    try:
        path = Path(args["path"]).expanduser().resolve()
        if not path.is_file():
            return {"ok": False, "error": "Image file not found or not a regular file."}
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            with Image.open(path) as image:
                result = {
                    "ok": True, "path": str(path), "size_bytes": path.stat().st_size,
                    "format": image.format, "width": image.width, "height": image.height,
                    "color_mode": image.mode, "has_exif": False, "exif": {}, "gps": {},
                    "warnings": [],
                    "note": "Metadata can be absent or modified. It does not prove authenticity, fraud, or AI generation. Dates are unmodified source values.",
                }
                try:
                    exif = image.getexif()
                    result["has_exif"] = bool(exif)
                    tags = dict(exif)
                    tags.update(exif.get_ifd(ExifTags.IFD.Exif))
                    wanted = ("Make", "Model", "Software", "Orientation", "DateTime", "DateTimeOriginal", "DateTimeDigitized", "OffsetTime", "OffsetTimeOriginal", "OffsetTimeDigitized")
                    result["exif"] = {ExifTags.TAGS[k]: _value(v) for k, v in tags.items() if ExifTags.TAGS.get(k) in wanted}
                    gps = exif.get_ifd(ExifTags.IFD.GPSInfo)
                    result["gps"] = {ExifTags.GPSTAGS.get(k, str(k)): _value(v) for k, v in list(gps.items())[:32]}
                except (OSError, ValueError, TypeError, KeyError, SyntaxError, OverflowError):
                    result["warnings"].append("Some EXIF metadata could not be decoded; fields may be incomplete.")
                if not result["has_exif"]:
                    result["warnings"].append("No readable EXIF metadata found.")
            if caught:
                result["warnings"].append("The image reader reported warnings; metadata may be incomplete or malformed.")
            return result
    except (OSError, ValueError, SyntaxError, UnidentifiedImageError, Image.DecompressionBombError):
        return {"ok": False, "error": "Cannot read this image: inaccessible, unsupported, damaged, or exceeds the reader's safety limit."}
