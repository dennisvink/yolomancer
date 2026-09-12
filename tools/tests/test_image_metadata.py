import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

from PIL import Image, ExifTags

spec = importlib.util.spec_from_file_location("image_metadata", Path(__file__).parents[1] / "image_metadata.py")
tool = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tool)


class ImageMetadataTests(unittest.TestCase):
    def test_png_without_exif(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "plain.png"
            Image.new("RGB", (40, 30)).save(path)
            before = hashlib.sha256(path.read_bytes()).digest()
            result = tool.run({"path": str(path)})
            self.assertTrue(result["ok"])
            self.assertEqual((result["width"], result["height"]), (40, 30))
            self.assertFalse(result["has_exif"])
            self.assertEqual(result["gps"], {})
            self.assertEqual(before, hashlib.sha256(path.read_bytes()).digest())

    def test_jpeg_with_nested_exif_and_gps(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "photo.jpg"
            exif = Image.Exif()
            exif[271] = "Workshop camera"
            exif[272] = "Sample model"
            exif[34665] = {36867: "2026:09:11 12:30:00", 36881: "+02:00"}
            exif[34853] = {1: "N", 2: (52.0, 22.0, 12.0), 3: "E", 4: (4.0, 54.0, 0.0)}
            Image.new("RGB", (40, 30)).save(path, exif=exif)
            result = tool.run({"path": str(path)})
            self.assertTrue(result["ok"])
            self.assertEqual(result["exif"]["Make"], "Workshop camera")
            self.assertEqual(result["exif"]["DateTimeOriginal"], "2026:09:11 12:30:00")
            self.assertEqual(result["exif"]["OffsetTimeOriginal"], "+02:00")
            self.assertEqual(result["gps"]["GPSLatitude"], [52.0, 22.0, 12.0])
            json.dumps(result, allow_nan=False)

    def test_invalid_inputs(self):
        for args in ({}, {"path": ""}, {"path": 5}, None, {"path": "/nonexistent/image.png"}):
            self.assertFalse(tool.run(args)["ok"])
        with tempfile.TemporaryDirectory() as folder:
            self.assertFalse(tool.run({"path": folder})["ok"])
            path = Path(folder) / "not-an-image.jpg"
            path.write_text("not an image")
            self.assertFalse(tool.run({"path": str(path)})["ok"])

    def test_json_values(self):
        self.assertEqual(tool._value(b"camera\x00"), "camera")
        self.assertIsNone(tool._value(float("nan")))
        self.assertEqual(len(tool._value("x" * 2000)), 1024)


if __name__ == "__main__":
    unittest.main()
