import json
import subprocess
import unittest
from unittest.mock import patch
import board


class BoardToolTests(unittest.TestCase):
    @patch('board.subprocess.run')
    def test_signed_cli_transport_and_bounded_summary(self, run):
        run.return_value = subprocess.CompletedProcess([], 0, json.dumps({'stories': [{'id': 1, 'title': 'Claim', 'lane': 'backlog', 'assignee': '', 'archived': False, 'version': 2, 'comments': ['large']}]}), '')
        result = board.run({'op': 'create', 'title': 'Claim', 'repo': 'prosus/prosus-user-096/workshop'})
        args, options = run.call_args
        self.assertEqual(args[0], ['yolomancer', 'board', 'create', '--json', '--repo', 'prosus/prosus-user-096/workshop'])
        request = json.loads(options['input'])
        self.assertTrue(request['agent'])
        self.assertEqual(len(request['requestId']), 32)
        self.assertNotIn('comments', result['result']['stories'][0])
        self.assertTrue(result['ok'])

    @patch('board.subprocess.run', side_effect=subprocess.TimeoutExpired('yolomancer', 100))
    def test_retry_keeps_request_id(self, run):
        key = 'a' * 32
        result = board.run({'op': 'comment', 'id': 1, 'comment': 'Review', 'requestId': key})
        self.assertFalse(result['ok'])
        self.assertEqual(result['requestId'], key)

    def test_invalid_operation(self):
        self.assertFalse(board.run({'op': 'shell'})['ok'])


if __name__ == '__main__':
    unittest.main()
