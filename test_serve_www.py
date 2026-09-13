import tempfile
import unittest
from pathlib import Path
from serve_www import create_app


class WebsiteTests(unittest.TestCase):
    def test_static_files_and_containment(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            root = base / 'www'
            root.mkdir()
            (root / 'index.html').write_text('workshop')
            (root / 'style.css').write_text('body {}')
            (base / 'secret').write_text('private')
            (root / 'escape').symlink_to(base / 'secret')
            (root / '.env').write_text('private')
            (root / 'nested').mkdir()
            (root / 'nested' / 'index.html').symlink_to(base / 'secret')
            client = create_app(root).test_client()
            with client.get('/') as response:
                self.assertEqual(response.data, b'workshop')
            (root / 'index.html').write_text('updated without restarting')
            with client.get('/') as response:
                self.assertEqual(response.data, b'updated without restarting')
            with client.get('/style.css') as response:
                self.assertEqual(response.headers['Cache-Control'], 'no-store')
            for url in ['/escape', '/nested', '/.env', '/../secret', '/%2e%2e/secret', '/missing']:
                self.assertEqual(client.get(url).status_code, 404, url)


if __name__ == '__main__':
    unittest.main()
