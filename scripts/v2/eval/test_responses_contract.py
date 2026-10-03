import copy
import http.server
import json
import threading
import unittest

import responses_contract as contract


def response():
    return {'object': 'response', 'id': 'synthetic', 'status': 'completed',
            'usage': {'input_tokens': 10, 'output_tokens': 2},
            'output': [{'type': 'message', 'content': [{'type': 'output_text', 'text': 'READY'}]}]}


class ResponsesContractTests(unittest.TestCase):
    def test_text_requires_completed_output_and_native_usage(self):
        self.assertTrue(contract.valid_response(response()))
        for change in ({'status': 'incomplete'}, {'usage': {}}, {'output': []},
                       {'id': ''}, {'id': True}, {'id': '  '}, {'object': 'chat.completion'},
                       {'usage': {'input_tokens': True, 'output_tokens': 2}},
                       {'usage': {'input_tokens': 10, 'output_tokens': -1}}):
            invalid = dict(response(), **change)
            self.assertFalse(contract.valid_response(invalid), change)

    def test_tool_requires_namespace_exact_arguments_and_call_identity(self):
        item = {'type': 'function_call', 'name': 'echo', 'namespace': 'functions',
                'call_id': 'synthetic', 'arguments': json.dumps({'marker': '**/*.go'})}
        valid = dict(response(), output=[item])
        self.assertTrue(contract.valid_tool(valid, '**/*.go'))
        for change in ({'arguments': 'invalid'}, {'arguments': '{"marker":"*.go"}'},
                       {'namespace': ''}, {'call_id': ''}, {'name': 'other'}):
            invalid = dict(valid, output=[dict(item, **change)])
            self.assertFalse(contract.valid_tool(invalid, '**/*.go'), change)
        for change in ({'object': 'chat.completion'}, {'id': ''}, {'usage': {}},
                       {'usage': {'input_tokens': 10, 'output_tokens': True}}):
            self.assertFalse(contract.valid_tool(dict(valid, **change), '**/*.go'), change)

    def test_truncated_or_error_stream_cannot_pass(self):
        valid = [{'type': 'response.created'}, {'type': 'response.output_text.delta'},
                 {'type': 'response.completed', 'response': response()}]
        self.assertTrue(contract.completed_stream(valid))
        for invalid in (valid[:-1], valid[1:], valid + [valid[-1]],
                        valid[:-1] + [{'type': 'error'}] + valid[-1:]):
            self.assertFalse(contract.completed_stream(invalid))
        invalid = copy.deepcopy(valid)
        invalid[-1]['response']['output'] = []
        self.assertFalse(contract.completed_stream(invalid))

    def test_endpoint_refuses_external_origins_and_credentials(self):
        for url in ('https://127.0.0.1', 'http://example.com', 'http://192.0.2.1',
                    'http://user:password@127.0.0.1', 'http://127.0.0.1/v1',
                    'http://127.0.0.1?token=test', 'http://127.0.0.1#fragment'):
            with self.assertRaises(ValueError, msg=url):
                contract.Endpoint(url, 'synthetic')
        contract.Endpoint('http://127.0.0.1:18096', 'synthetic')
        contract.Endpoint('http://[::1]:18096', 'synthetic')

    def test_redirect_is_recorded_without_forwarding_credentials(self):
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                self.send_response(302)
                self.send_header('Location', 'http://192.0.2.1/')
                self.end_headers()
                self.wfile.write(b'{"error":{"code":"redirect"}}')

            def log_message(self, *args):
                pass

        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            endpoint = contract.Endpoint('http://127.0.0.1:%d' % server.server_port, 'synthetic', 1)
            status, parsed, metadata = endpoint.post({'input': 'fixture'})
            self.assertEqual(status, 302)
            self.assertEqual(parsed['error']['code'], 'redirect')
            self.assertNotIn('synthetic', json.dumps(metadata))
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == '__main__':
    unittest.main()
