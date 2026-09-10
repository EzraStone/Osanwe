import contextlib
import io
import json
import unittest
from unittest.mock import Mock, patch

import phase0_latency
import providers


class Phase0SafetyTests(unittest.TestCase):
    def test_dry_run_never_needs_a_key_or_session(self):
        output = io.StringIO()
        with patch('sys.argv', ['phase0_latency.py', '--provider', 'tokenrouter',
                                '--runs', '30', '--proxy', 'http://user:secret@relay.test:8080', '--dry-run']), \
             patch('requests.Session', side_effect=AssertionError('No networking')), \
             contextlib.redirect_stdout(output):
            self.assertEqual(phase0_latency.main(), 0)
        value = json.loads(output.getvalue())
        self.assertEqual(value['requests_including_warmup'], 62)
        self.assertEqual(value['model'], 'z-ai/glm-5.3-free')
        self.assertNotIn('secret', output.getvalue())

    def trial(self, session):
        return phase0_latency.run_trial('direct', session, provider=providers.PROVIDERS['tokenrouter'],
            base_url='https://api.tokenrouter.com', model='z-ai/glm-5.3-free', api_key='synthetic-key',
            max_tokens=32, proxies=None, timeout=1)

    def test_http_failure_discards_body_closes_response_and_disallows_redirects(self):
        session = Mock()
        response = session.post.return_value
        response.status_code = 302
        response.text = 'private-account-and-key'
        result = self.trial(session)
        self.assertEqual(result.error, 'HTTP 302')
        response.close.assert_called_once()
        self.assertFalse(session.post.call_args.kwargs['allow_redirects'])

    def test_transport_exception_is_not_recorded(self):
        session = Mock()
        session.post.side_effect = phase0_latency.requests.RequestException('secret proxy credentials')
        self.assertEqual(self.trial(session).error, 'request transport failed')


if __name__ == '__main__':
    unittest.main()
