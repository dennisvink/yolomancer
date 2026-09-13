"""Serve the workshop website: python serve_www.py (PORT defaults to 8080)."""
import os
from pathlib import Path

from flask import Flask, abort, send_from_directory
from waitress import serve


def create_app(root=None):
    root = Path(root or Path(__file__).parent / 'www').resolve()
    app = Flask(__name__, static_folder=None)

    @app.get('/', defaults={'filename': 'index.html'})
    @app.get('/<path:filename>')
    def website(filename):
        target = (root / filename).resolve()
        if target.is_dir():
            filename = filename.rstrip('/') + '/index.html'
            target = (root / filename).resolve()
        # Never serve source, credentials, dotfiles, or symlinks outside www.
        if not target.is_relative_to(root) or any(p.startswith('.') for p in Path(filename).parts):
            abort(404)
        return send_from_directory(root, filename, max_age=0)

    @app.after_request
    def headers(response):
        response.headers['Cache-Control'] = 'no-store'
        response.headers['X-Content-Type-Options'] = 'nosniff'
        return response

    return app


if __name__ == '__main__':
    serve(create_app(), host='0.0.0.0', port=int(os.environ.get('PORT', '8080')))
