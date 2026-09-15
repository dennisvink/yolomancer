"""Serve the workshop website: python serve_www.py (PORT defaults to 8080)."""
import os
from pathlib import Path

from flask import Flask, Response, abort, request, send_from_directory
from waitress import serve

API_BASE = os.environ.get("YOLOMANCER_API_URL", "http://127.0.0.1:8081")


def create_app(root=None):
    root = Path(root or Path(__file__).parent / 'www').resolve()
    app = Flask(__name__, static_folder=None)

    # ── API proxy ───────────────────────────────────────────────────────
    # The yolomancer API listens on loopback only and does not enable CORS.
    # These proxy routes let the browser integration reach it through the
    # same origin that serves the website.

    @app.get('/api/healthz')
    def api_healthz():
        import urllib.request
        try:
            r = urllib.request.urlopen(f"{API_BASE}/healthz", timeout=5)
            return Response(r.read(), status=r.status, content_type='application/json')
        except Exception as exc:
            return Response(f'{{"ok":false,"error":"{exc}"}}', status=502, content_type='application/json')

    @app.get('/api/v1/agents')
    def api_agents():
        import urllib.request
        try:
            r = urllib.request.urlopen(f"{API_BASE}/v1/agents", timeout=5)
            return Response(r.read(), status=r.status, content_type='application/json')
        except Exception as exc:
            return Response(f'{{"error":"{exc}"}}', status=502, content_type='application/json')

    @app.post('/api/v1/runs')
    def api_runs():
        """Stream SSE from the yolomancer API back to the browser."""
        import json
        import urllib.request

        body = request.get_data()
        req = urllib.request.Request(
            f"{API_BASE}/v1/runs",
            data=body,
            headers={"Content-Type": "application/json"},
            method="POST",
        )

        def generate():
            try:
                resp = urllib.request.urlopen(req, timeout=920)
                while True:
                    line = resp.readline()
                    if not line:
                        break
                    yield line
            except Exception as exc:
                yield f"event: run.failed\ndata: {json.dumps({'type':'run.failed','data':{'message':str(exc)}})}\n\n".encode()

        return Response(generate(), content_type='text/event-stream',
                        headers={'Cache-Control': 'no-cache', 'X-Accel-Buffering': 'no'})

    # ── Static files ────────────────────────────────────────────────────

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
