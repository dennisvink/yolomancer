"""Manage the shared repository task board using the enrolled lab SSH identity."""
import json
import subprocess
import uuid

OPS = ['list', 'get', 'create', 'edit', 'comment', 'move', 'assign', 'take', 'archive', 'unarchive', 'reset']


def yolomancer_tool():
    return {
        'name': 'board',
        'description': 'Read and manage the repository shared task board. Invited collaborators share the board. List/get before changing a story and supply its version for edit/move/assign/take/archive/unarchive. Comments merge safely. Reset deletes all stories and requires explicit user approval, owner identity and confirm=true. Lab delete/reinstall also clears the board. Never reset unless the user requested it.',
        'parameters': {'type': 'object', 'properties': {
            'op': {'type': 'string', 'enum': OPS},
            'repo': {'type': 'string', 'description': 'Yolomancer repository URL or prosus/prosus-user-NNN/workshop. Defaults to current Git origin.'},
            'id': {'type': 'integer', 'minimum': 1},
            'version': {'type': 'integer', 'minimum': 1},
            'title': {'type': 'string', 'maxLength': 200},
            'body': {'type': 'string', 'maxLength': 4000},
            'comment': {'type': 'string', 'maxLength': 4000},
            'lane': {'type': 'string', 'enum': ['backlog', 'ready', 'doing', 'review', 'done']},
            'assignee': {'type': 'string', 'description': 'Participant username; empty to unassign.'},
            'afterId': {'type': 'integer', 'minimum': 0, 'description': 'Move after this story in the target lane; 0 places first.'},
            'requestId': {'type': 'string', 'pattern': '^[a-f0-9]{32}$', 'description': 'Reuse this id when retrying the same uncertain mutation to prevent duplicate comments/stories.'},
            'confirm': {'type': 'boolean'},
        }, 'required': ['op'], 'additionalProperties': False},
    }


def run(args):
    if not isinstance(args, dict) or args.get('op') not in OPS:
        return {'ok': False, 'error': 'Provide a valid board operation.'}
    fields = dict(args)
    op = fields.pop('op')
    repo = fields.pop('repo', None)
    fields['agent'] = True
    if op not in ('list', 'get'):
        fields.setdefault('requestId', uuid.uuid4().hex)
    command = ['yolomancer', 'board', op, '--json']
    if repo:
        command += ['--repo', repo]
    try:
        result = subprocess.run(command, input=json.dumps(fields), text=True, capture_output=True, timeout=100, check=False)
        if result.returncode:
            return {'ok': False, 'error': result.stderr.strip()[:2000], 'requestId': fields.get('requestId')}
        data = json.loads(result.stdout)
        if isinstance(data, dict) and 'stories' in data:
            # Keep large comment/history lists out of the agent context; get
            # one story to inspect its details. Every summary retains version.
            data['stories'] = [{k: s[k] for k in ('id', 'title', 'lane', 'assignee', 'archived', 'version')} for s in data['stories']]
        if op == 'get':
            data['comments'] = data.get('comments', [])[-20:]
            data['history'] = data.get('history', [])[-20:]
        return {'ok': True, 'result': data, 'requestId': fields.get('requestId')}
    except subprocess.TimeoutExpired:
        return {'ok': False, 'error': 'Request timed out; it may have succeeded. Retry the same action using this requestId.', 'requestId': fields.get('requestId')}
    except (OSError, ValueError):
        return {'ok': False, 'error': 'Unable to run Yolomancer board. Ensure the installed binary supports the board command.', 'requestId': fields.get('requestId')}
