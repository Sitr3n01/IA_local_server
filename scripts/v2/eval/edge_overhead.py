"""Paired warm Chat probes of edge vs router; persists metadata only.

The caller owns idle/admission checks and model cleanup. Both credentials must
be supplied in the environment, never as command-line arguments. Cold warmups
are recorded separately and cannot be counted as edge overhead.
"""
import argparse
import hashlib
import http.client
import json
import math
import os
import time
from pathlib import Path

from responses_contract import Endpoint


def sample(endpoint, payload):
    connection = http.client.HTTPConnection(endpoint.url.hostname, endpoint.url.port, timeout=180)
    started = time.perf_counter()
    try:
        connection.request('POST', '/v1/chat/completions', json.dumps(payload).encode(), {
            'Content-Type': 'application/json', 'Authorization': 'Bearer ' + endpoint.token})
        response = connection.getresponse()
        raw = response.read(64 * 1024 * 1024 + 1)
        elapsed_ms = (time.perf_counter() - started) * 1000
        if len(raw) > 64 * 1024 * 1024:
            raise ValueError('probe response exceeded limit')
        data = json.loads(raw)
        timing = data.get('timings') or {}
        usage = data.get('usage') or {}
        tokens = usage.get('completion_tokens')
        prompt_ms, prediction_ms = timing.get('prompt_ms'), timing.get('predicted_ms')
        if (response.status != 200 or type(tokens) is not int or tokens <= 0
                or not all(type(value) in (int, float) and math.isfinite(value) and value >= 0
                           for value in (prompt_ms, prediction_ms))
                or not any(choice.get('message', {}).get('content', '').strip()
                           for choice in data.get('choices', []))):
            raise ValueError('probe requires successful output and native timing metadata')
        return {'elapsed_ms': elapsed_ms, 'compute_ms': prompt_ms + prediction_ms,
                'output_tokens': tokens, 'body_sha256': hashlib.sha256(raw).hexdigest()}
    finally:
        connection.close()


def percentile95(values):
    return sorted(values)[math.ceil(len(values) * 0.95) - 1]


def summarize(pairs):
    if len(pairs) < 20:
        raise ValueError('at least twenty pairs are required')
    deltas = [(pair['edge']['elapsed_ms'] - pair['edge']['compute_ms'])
              - (pair['direct']['elapsed_ms'] - pair['direct']['compute_ms']) for pair in pairs]
    rates = {kind: sum(pair[kind]['output_tokens'] for pair in pairs)
             / (sum(pair[kind]['elapsed_ms'] for pair in pairs) / 1000) for kind in ('edge', 'direct')}
    matched = all(pair['edge']['output_tokens'] == pair['direct']['output_tokens'] for pair in pairs)
    regression = (1 - rates['edge'] / rates['direct']) * 100
    p95 = percentile95(deltas)
    return {'samples': len(pairs), 'output_token_counts_match': matched,
            'p95_edge_overhead_ms': round(p95, 3),
            'throughput_regression_percent': round(regression, 3),
            'edge_tokens_per_second': round(rates['edge'], 3),
            'direct_tokens_per_second': round(rates['direct'], 3),
            'passed': matched and p95 < 50 and regression < 5}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--edge-url', required=True)
    parser.add_argument('--direct-url', required=True)
    parser.add_argument('--model', required=True)
    parser.add_argument('--out', required=True)
    parser.add_argument('--pairs', type=int, default=30)
    args = parser.parse_args()
    if not 20 <= args.pairs <= 100:
        parser.error('pairs must be between 20 and 100')
    edge_token = os.environ.get('CIA_LOCAL_API_KEY')
    direct_token = os.environ.get('CIA_ROUTER_TOKEN')
    if not edge_token or not direct_token:
        parser.error('credential helper and caller must supply the inference and router tokens')
    endpoints = {'edge': Endpoint(args.edge_url, edge_token), 'direct': Endpoint(args.direct_url, direct_token)}
    payload = {'model': args.model, 'messages': [{'role': 'user', 'content': 'Reply with READY.'}],
               'max_tokens': 512, 'temperature': 0, 'seed': 42}
    report = {'schema_version': 1, 'scenario': 'paired-warm-edge-overhead', 'model': args.model,
              'started_utc': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
              'warmups': {}, 'pairs': [], 'passed': False}
    try:
        for kind in ('direct', 'edge'):
            report['warmups'][kind] = sample(endpoints[kind], payload)
        for index in range(args.pairs):
            pair = {}
            for kind in (('edge', 'direct') if index % 2 else ('direct', 'edge')):
                pair[kind] = sample(endpoints[kind], payload)
            report['pairs'].append(pair)
            print('pair %d/%d complete' % (index + 1, args.pairs), flush=True)
        report.update(summarize(report['pairs']))
    except Exception as exc:
        report['failure_type'] = type(exc).__name__
    Path(args.out).write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')
    print(json.dumps({key: value for key, value in report.items() if key not in ('pairs', 'warmups')}))
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
