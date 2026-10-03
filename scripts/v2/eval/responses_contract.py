"""Native stateless Responses contract; reports metadata, never generated text.

Run against a qualified edge, or a temporary edge with provisional capabilities.
A provisional flag is a test input, not permission to promote a model. The caller
must enforce the deployment's memory admission policy and single-model lifecycle.
"""
import argparse
import hashlib
import http.client
import ipaddress
import json
import os
import time
import urllib.parse
from pathlib import Path


class Endpoint:
    def __init__(self, base_url, token, timeout=180):
        self.url = urllib.parse.urlsplit(base_url)
        if (self.url.scheme != 'http' or not self.url.hostname
                or self.url.username or self.url.password
                or self.url.path not in ('', '/') or self.url.query or self.url.fragment
                or not ipaddress.ip_address(self.url.hostname).is_loopback):
            raise ValueError('contract endpoint must be a literal loopback HTTP origin')
        self.token = token
        self.timeout = timeout

    def post(self, payload, cancel_after=None):
        connection = http.client.HTTPConnection(self.url.hostname, self.url.port, timeout=self.timeout)
        started = time.monotonic()
        events = []
        first_event_ms = None
        digest = hashlib.sha256()
        try:
            connection.request('POST', '/v1/responses', json.dumps(payload).encode(), {
                'Content-Type': 'application/json', 'Authorization': 'Bearer ' + self.token})
            response = connection.getresponse()
            if response.status != 200 or not payload.get('stream'):
                raw = response.read(64 * 1024 * 1024 + 1)
                if len(raw) > 64 * 1024 * 1024:
                    raise ValueError('response exceeded the contract limit')
                parsed = json.loads(raw)
                return response.status, parsed, {
                    'body_sha256': hashlib.sha256(raw).hexdigest(),
                    'duration_ms': round((time.monotonic() - started) * 1000, 2)}
            if not response.getheader('Content-Type', '').startswith('text/event-stream'):
                raise ValueError('stream has no SSE content type')
            size = 0
            for line in response:
                size += len(line)
                if size > 64 * 1024 * 1024:
                    raise ValueError('stream exceeded the contract limit')
                digest.update(line)
                if not line.startswith(b'data:'):
                    continue
                data = line[5:].strip()
                if not data:
                    continue
                event = json.loads(data)
                if first_event_ms is None:
                    first_event_ms = (time.monotonic() - started) * 1000
                events.append(event)
                if cancel_after and len(events) >= cancel_after:
                    break
            return response.status, events, {
                'body_sha256': digest.hexdigest(), 'event_count': len(events),
                'first_event_ms': round(first_event_ms, 2) if first_event_ms is not None else None,
                'duration_ms': round((time.monotonic() - started) * 1000, 2)}
        finally:
            connection.close()


def text_present(response):
    return any(part.get('type') == 'output_text' and bool(part.get('text', '').strip())
               for item in response.get('output', []) if item.get('type') == 'message'
               for part in item.get('content', []))


def completed_response(response):
    usage = response.get('usage') or {}
    return (response.get('object') == 'response' and isinstance(response.get('id'), str)
            and bool(response['id'].strip()) and response.get('status') == 'completed'
            and all(type(usage.get(key)) is int and usage[key] >= 0
                    for key in ('input_tokens', 'output_tokens')))


def valid_response(response):
    return completed_response(response) and text_present(response)


def valid_tool(response, marker):
    calls = [item for item in response.get('output', []) if item.get('type') == 'function_call']
    if len(calls) != 1:
        return False
    call = calls[0]
    try:
        arguments = json.loads(call.get('arguments', ''))
    except (ValueError, TypeError):
        return False
    return (completed_response(response) and call.get('name') == 'echo'
            and call.get('namespace') == 'functions' and isinstance(call.get('call_id'), str)
            and bool(call['call_id'].strip())
            and arguments == {'marker': marker})


def completed_stream(events, tool=False, marker=''):
    kinds = [event.get('type') for event in events]
    if (not kinds or kinds[0] != 'response.created'
            or kinds.count('response.completed') != 1
            or kinds[-1] != 'response.completed'
            or any(kind in ('error', 'response.failed', 'response.incomplete') for kind in kinds)):
        return False
    response = events[-1].get('response') or {}
    if tool:
        return ('response.function_call_arguments.delta' in kinds and valid_tool(response, marker))
    return 'response.output_text.delta' in kinds and valid_response(response)


def run(endpoint, model, iterations=10, tools=False):
    base = {'model': model, 'store': False, 'max_output_tokens': 2048}
    rows = []

    def record(name, status, passed, metadata):
        rows.append(dict(id=name, status=status, passed=bool(passed), **metadata))
        print('%s status=%s passed=%s' % (name, status, bool(passed)), flush=True)

    status, response, metadata = endpoint.post(dict(base, input='Reply with READY.'))
    record('responses_text', status, status == 200 and valid_response(response), metadata)
    status, events, metadata = endpoint.post(dict(base, input='Reply with READY.', stream=True))
    record('responses_stream', status, status == 200 and completed_stream(events), metadata)

    if tools:
        tool_list = [{'type': 'namespace', 'name': 'functions', 'tools': [{
            'type': 'function', 'name': 'echo', 'description': 'Echo an exact marker.',
            'parameters': {'type': 'object', 'properties': {'marker': {'type': 'string'}},
                           'required': ['marker'], 'additionalProperties': False}}]}]
        status, response, metadata = endpoint.post(dict(base, input='Echo a synthetic marker.',
            tools=tool_list, tool_choice={'type': 'function', 'name': 'echo', 'namespace': 'functions'}))
        record('named_tool_constraint_refused', status, status == 400
               and response.get('error', {}).get('code') == 'unsupported_feature', metadata)
        for index in range(iterations):
            marker = 'C:/synthetic/%d/**/*.go' % index
            conversation = [{'role': 'user', 'content': 'Call echo with marker literal ' + marker}]
            payload = dict(base, input=conversation, tools=tool_list, tool_choice='required',
                           parallel_tool_calls=False)
            status, response, metadata = endpoint.post(payload)
            passed = status == 200 and valid_tool(response, marker)
            record('namespace_tool_%d' % index, status, passed, metadata)
            if not passed:
                continue
            call = next(item for item in response['output'] if item.get('type') == 'function_call')
            history = conversation + response['output'] + [{
                'type': 'function_call_output', 'call_id': call['call_id'], 'output': 'READY'}]
            status, continued, metadata = endpoint.post(dict(base, input=history, tools=tool_list,
                                                            tool_choice='none'))
            record('tool_continuation_%d' % index, status,
                   status == 200 and valid_response(continued)
                   and not any(item.get('type') == 'function_call' for item in continued.get('output', [])), metadata)
        marker = 'C:/synthetic/stream/**/*.go'
        payload = dict(base, input='Call echo with marker literal ' + marker, tools=tool_list,
                       tool_choice='required', parallel_tool_calls=False, stream=True)
        status, events, metadata = endpoint.post(payload)
        record('namespace_tool_stream', status,
               status == 200 and completed_stream(events, tool=True, marker=marker), metadata)

    status, events, metadata = endpoint.post(dict(base, stream=True, input=
        'List the integers from 1 to 1000, one per line.'), cancel_after=4)
    record('cancel_disconnect', status, status == 200 and len(events) == 4
           and not any(item.get('type') == 'response.completed' for item in events), metadata)
    started = time.monotonic()
    status, response, metadata = endpoint.post(dict(base, input='Reply with READY.'))
    record('recovery_after_cancel', status, status == 200 and valid_response(response)
           and time.monotonic() - started < 30, metadata)

    for name, extra in [('stateful_response', {'previous_response_id': 'synthetic-id'}),
                        ('stored_response', {'store': True})]:
        status, response, metadata = endpoint.post(dict(base, input='READY', **extra))
        record(name, status, status == 400 and response.get('error', {}).get('code') == 'unsupported_feature', metadata)
    return rows


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--base-url', required=True)
    parser.add_argument('--model', required=True)
    parser.add_argument('--out', required=True)
    parser.add_argument('--tools', action='store_true')
    parser.add_argument('--iterations', type=int, default=10)
    parser.add_argument('--timeout', type=int, default=180)
    args = parser.parse_args()
    if not 1 <= args.iterations <= 100:
        parser.error('iterations must be between 1 and 100')
    token = os.environ.get('CIA_LOCAL_API_KEY', '')
    if not token:
        parser.error('CIA_LOCAL_API_KEY must be supplied by the credential helper')
    endpoint = Endpoint(args.base_url, token, timeout=args.timeout)
    report = {'schema_version': 1, 'scenario': 'native-responses-contract', 'model': args.model,
              'started_utc': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}
    try:
        report['checks'] = run(endpoint, args.model, args.iterations, args.tools)
        report['passed'] = all(row['passed'] for row in report['checks'])
    except Exception as exc:
        report['passed'] = False
        report['failure_type'] = type(exc).__name__
    Path(args.out).write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(report))
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
