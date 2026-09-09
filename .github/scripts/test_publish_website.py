import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("publish-website.sh").resolve()


class WebsitePublisherTest(unittest.TestCase):
    def test_uploads_and_preflight(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bindir = root / "bin"
            bindir.mkdir()
            log = root / "calls.jsonl"
            mock = bindir / "aws"
            mock.write_text("#!/usr/bin/env python3\nimport json, os, sys\nwith open(os.environ['CALL_LOG'], 'a') as f: f.write(json.dumps(sys.argv[1:]) + '\\n')\n")
            mock.chmod(0o755)
            entries = []
            for platform in ("darwin", "linux", "windows"):
                for arch in ("amd64", "arm64"):
                    name = f"yolomancer-{platform}-{arch}" + (".exe" if platform == "windows" else "")
                    payload = name.encode()
                    (root / name).write_bytes(payload)
                    entries.append(f"{hashlib.sha256(payload).hexdigest()}  {name}\n")
            (root / "SHA256SUMS").write_text("".join(entries))
            env = dict(os.environ, PATH=str(bindir) + os.pathsep + os.environ["PATH"], CALL_LOG=str(log))
            result = subprocess.run(["bash", str(SCRIPT), str(root)], env=env, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            calls = [json.loads(line) for line in log.read_text().splitlines()]
            self.assertEqual(len(calls), 6)
            for call in calls:
                self.assertEqual(call[:2], ["s3api", "put-object"])
                args = dict(zip(call[2:-1:2], call[3:-1:2]))
                self.assertEqual(args["--bucket"], "yolomancer-website-183305290766")
                filename = "yolomancer.exe" if "/windows/" in args["--key"] else "yolomancer"
                self.assertTrue(args["--key"].endswith("/" + filename))
                self.assertEqual(args["--content-disposition"], f'attachment; filename="{filename}"')
                self.assertIn("no-store", args["--cache-control"])
                self.assertEqual(args["--checksum-algorithm"], "SHA256")
            # A corrupt or absent last asset must prevent even the first upload.
            log.unlink()
            last = root / "yolomancer-windows-arm64.exe"
            last.write_bytes(b"corrupt")
            result = subprocess.run(["bash", str(SCRIPT), str(root)], env=env, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(log.exists())
            last.unlink()
            result = subprocess.run(["bash", str(SCRIPT), str(root)], env=env, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(log.exists())


if __name__ == "__main__":
    unittest.main()
