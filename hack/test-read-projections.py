#!/usr/bin/env python3
"""Exercise projected reads against the local Tilt stack (stdlib only).

Read-only by default. --exercise-sync creates, updates and deletes one uniquely
named app ConfigMap in kind-suparship-dev; it never publishes a workload.
Credentials: SUPARSHIP_DEV_USER / SUPARSHIP_DEV_PASSWORD (Tilt dev defaults).
"""
import argparse
import concurrent.futures
import http.cookiejar
import json
import os
import statistics
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url', default='http://127.0.0.1:8080')
    parser.add_argument('--project', default='demo')
    parser.add_argument('--app', default='shipnotes')
    parser.add_argument('--concurrency', type=int, default=16)
    parser.add_argument('--requests', type=int, default=160)
    parser.add_argument('--exercise-sync', action='store_true')
    parser.add_argument('--report', default='/tmp/suparship-live-read-report.json')
    args = parser.parse_args()
    parsed = urllib.parse.urlparse(args.base_url)
    if parsed.hostname not in ('127.0.0.1', 'localhost', '::1'):
        parser.error('Only a local Tilt endpoint is allowed')
    if args.concurrency < 1 or args.requests < 1:
        parser.error('Concurrency and request count must be positive')
    base = args.base_url.rstrip('/') + '/api/v1'
    jar = http.cookiejar.CookieJar()
    login = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    credentials = json.dumps({'username': os.environ.get('SUPARSHIP_DEV_USER', 'admin@local'),
                              'password': os.environ.get('SUPARSHIP_DEV_PASSWORD', 'admin123')}).encode()
    with login.open(urllib.request.Request(base + '/auth/login', data=credentials,
                    headers={'Content-Type': 'application/json'}), timeout=30) as response:
        assert response.status == 200
    cookie = '; '.join(c.name + '=' + c.value for c in jar)

    def get(path, authenticated=True):
        request = urllib.request.Request(base + path, headers={'Cookie': cookie} if authenticated else {})
        started = time.perf_counter()
        try:
            response = urllib.request.urlopen(request, timeout=30)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            body = response.read()
            return response.status, dict(response.headers), body, (time.perf_counter() - started) * 1000

    def fresh(path, timeout=40):
        deadline = time.monotonic() + timeout
        while True:
            result = get(path)
            if result[0] == 200 and result[1].get('X-Read-State') == 'fresh':
                return result
            if time.monotonic() > deadline:
                raise AssertionError(f'{path}: status={result[0]} state={result[1].get("X-Read-State")}')
            time.sleep(0.5)

    project = urllib.parse.quote(args.project, safe='')
    app = urllib.parse.quote(args.app, safe='')
    root = f'/projects/{project}/apps'
    detail = root + '/' + app
    report = {'base_url': args.base_url, 'concurrency': args.concurrency, 'requests_per_endpoint': args.requests,
              'routes': {}, 'latency': {}, 'checks': {}}
    failures = []
    for path in ['/org', '/projects', '/environments', '/clusters', root, detail,
                 detail + '/environments', detail + '/environments/staging/history',
                 detail + '/envs/staging/envconfig/resolved']:
        try:
            result = fresh(path)
            report['routes'][path] = {'status': result[0], 'state': result[1]['X-Read-State'], 'bytes': len(result[2])}
            if path == root:
                apps = json.loads(result[2])['apps']
                report['fixture'] = {'apps': len(apps), 'environments': sum(len(a.get('environments', [])) for a in apps)}
            print('PASS fresh', path, flush=True)
        except AssertionError as error:
            failures.append(str(error))
            print('FAIL', error, flush=True)

    for path in [root, detail]:
        with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
            results = list(pool.map(lambda _: get(path), range(args.requests)))
        times = sorted(r[3] for r in results)
        percentile = lambda fraction: round(times[min(len(times)-1, int(len(times)*fraction))], 3)
        measurement = {'p50_ms': percentile(.50), 'p95_ms': percentile(.95), 'p99_ms': percentile(.99),
                       'mean_ms': round(statistics.mean(times), 3),
                       'successes': sum(r[0] == 200 for r in results),
                       'fresh_responses': sum(r[1].get('X-Read-State') == 'fresh' for r in results)}
        report['latency'][path] = measurement
        print('LATENCY', path, json.dumps(measurement), flush=True)
        if (measurement['p95_ms'] >= 300 or measurement['successes'] != args.requests
                or measurement['fresh_responses'] != args.requests):
            failures.append(f'{path}: warm-read gate failed')

    for suffix in ['', '?watch=1']:
        status = get(detail + suffix, authenticated=False)[0]
        report['checks']['unauthenticated' + suffix] = status
        if status != 401:
            failures.append('Unauthenticated read/watch did not return 401')

    if args.exercise_sync:
        name = 'read-probe-' + uuid.uuid4().hex[:8]
        cm_name = f'suparship-app-{args.project}-{name}'
        probe_path = root + '/' + name
        record = {'name': name, 'projectName': args.project, 'spec': {'displayName': 'Read probe before'}}
        cm = {'apiVersion': 'v1', 'kind': 'ConfigMap',
              'metadata': {'name': cm_name, 'namespace': 'suparship-system',
                           'labels': {'suparship.io/type': 'app', 'suparship.io/project': args.project,
                                      'suparship.io/app': name, 'suparship.io/test': 'read-projection'}},
              'data': {'app.json': json.dumps(record)}}

        def kube(*arguments, data=None):
            return subprocess.run(['kubectl', '--context', 'kind-suparship-dev', *arguments],
                                  input=json.dumps(data) if data else None, text=True, check=True,
                                  capture_output=True, timeout=30)

        created = False
        try:
            kube('create', '-f', '-', data=cm)
            created = True
            initial = get(probe_path)
            report['checks']['cold_status'] = initial[0]
            report['checks']['cold_state'] = initial[1].get('X-Read-State')
            assert initial[0] == 503 and initial[1].get('X-Read-State') == 'synchronizing'
            fresh(probe_path)
            request = urllib.request.Request(base + probe_path + '?watch=1', headers={'Cookie': cookie})
            with urllib.request.urlopen(request, timeout=20) as stream:
                def event():
                    while True:
                        line = stream.readline()
                        if not line:
                            raise AssertionError('SSE stream closed before observation')
                        if line.startswith(b'data: '):
                            return json.loads(line[6:])
                previous = event()['revision']
                record['spec']['displayName'] = 'Read probe after'
                started = time.monotonic()
                kube('patch', 'configmap', cm_name, '-n', 'suparship-system', '--type=merge',
                     '-p', json.dumps({'data': {'app.json': json.dumps(record)}}))
                while event()['revision'] == previous:
                    pass
                lag = time.monotonic() - started
                observed = json.loads(fresh(probe_path)[2])['app']
                assert observed['displayName'] == 'Read probe after'
                report['checks']['sse_observation_lag_seconds'] = round(lag, 3)
                assert lag < 15, 'SSE observation exceeded 15-second budget'
            # A new stream must resynchronize without Last-Event-ID/replay.
            with urllib.request.urlopen(request, timeout=20) as stream:
                while True:
                    line = stream.readline()
                    if not line:
                        raise AssertionError('Reconnect produced no event')
                    if line.startswith(b'data: '):
                        assert json.loads(line[6:])['revision'] != previous
                        break
            report['checks']['sse_reconnect'] = 'passed'
            # Corrupt only our disposable record to simulate an unreadable source.
            # Last good data must survive, explicitly stale, until it recovers.
            kube('patch', 'configmap', cm_name, '-n', 'suparship-system', '--type=merge',
                 '-p', json.dumps({'data': {'app.json': '{'}}))
            started = time.monotonic()
            while True:
                retained = get(probe_path)
                if retained[1].get('X-Read-State') == 'stale':
                    assert retained[0] == 200
                    assert json.loads(retained[2])['app']['displayName'] == 'Read probe after'
                    break
                assert time.monotonic() - started < 15, 'Source failure was not marked stale'
                time.sleep(.25)
            report['checks']['source_failure_retains_last_good'] = 'passed'
            kube('patch', 'configmap', cm_name, '-n', 'suparship-system', '--type=merge',
                 '-p', json.dumps({'data': {'app.json': json.dumps(record)}}))
            fresh(probe_path)
            report['checks']['source_recovery'] = 'passed'
            kube('delete', 'configmap', cm_name, '-n', 'suparship-system')
            created = False
            started = time.monotonic()
            while get(probe_path)[0] != 404:
                assert time.monotonic() - started < 15, 'Deletion did not propagate'
                time.sleep(.25)
            report['checks']['deletion_lag_seconds'] = round(time.monotonic() - started, 3)
            print('PASS cold read, source update, SSE reconnect and deletion', flush=True)
        except Exception as error:
            failures.append(str(error))
            print('FAIL synchronization exercise:', type(error).__name__, str(error), flush=True)
        finally:
            if created:
                kube('delete', 'configmap', cm_name, '-n', 'suparship-system', '--ignore-not-found')

    report['failures'] = failures
    with open(args.report, 'w') as output:
        json.dump(report, output, indent=2)
        output.write('\n')
    print('Report:', args.report, flush=True)
    return 1 if failures else 0


if __name__ == '__main__':
    raise SystemExit(main())
