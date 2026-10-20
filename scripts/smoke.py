"""Disposable Docker integration test; never targets the real Libation container."""
import json
from datetime import datetime
from zoneinfo import ZoneInfo
import subprocess
import tempfile
import time
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PROJECT = 'scheduler-smoke-' + uuid.uuid4().hex[:8]
TARGET = PROJECT + '-job'


def docker(*args, check=True):
    return subprocess.run(['docker', *args], text=True, capture_output=True, check=check).stdout.strip()


def wait_for(predicate, description, timeout=45):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(.25)
    raise AssertionError('Timed out: ' + description)


def main():
    with tempfile.TemporaryDirectory() as tmp:
        # Reuse the production proxy ACLs and hardening; isolate all names.
        module = (ROOT / 'compose/scheduler.yml').read_text().replace('libation', TARGET)
        module = module.replace('@every 4h', '@every 4s')
        module = module.replace('${TZ:-UTC}', 'Australia/Sydney')
        path = Path(tmp) / 'compose.yml'
        path.write_text(module + '\nnetworks:\n  scheduler_socket_proxy:\n    internal: true\n')

        def compose(*args):
            return docker('compose', '-p', PROJECT, '-f', str(path), *args)

        def logs():
            return compose('logs', '--no-color', 'scheduler')

        def create():
            docker('create', '--name', TARGET, '--restart=no', 'alpine:3.22',
                   'sh', '-c', 'echo started; sleep 16; echo finished')

        def running():
            return docker('inspect', '-f', '{{.State.Running}}', TARGET) == 'true'

        try:
            docker('pull', 'alpine:3.22')
            docker('pull', 'python:3.13-alpine')
            create()
            compose('up', '-d')
            wait_for(lambda: 'job scheduled' in logs(), 'job discovery')
            compose('exec', '-T', 'scheduler', '/container-scheduler', 'healthcheck')
            assert not running(), 'Started before first interval'
            record = next(json.loads(line[line.index('{'):]) for line in logs().splitlines()
                          if 'job scheduled' in line)
            for field in ('time', 'next'):
                stamp = datetime.fromisoformat(record[field])
                expected = stamp.astimezone(ZoneInfo('Australia/Sydney'))
                assert stamp.utcoffset() == expected.utcoffset(), (field, record[field])
            wait_for(running, 'first scheduled start')
            wait_for(lambda: 'skipped running container' in logs(), 'overlap skip')
            compose('stop', 'scheduler')
            assert running(), 'Scheduler shutdown interrupted target'
            wait_for(lambda: not running(), 'natural completion')
            assert 'finished' in docker('logs', TARGET)

            # Recreate while scheduler is live; it must resolve the new ID by name.
            ready_count = logs().count('control socket ready')
            compose('start', 'scheduler')
            old_id = docker('inspect', '-f', '{{.Id}}', TARGET)
            docker('rm', TARGET)
            create()
            assert docker('inspect', '-f', '{{.Id}}', TARGET) != old_id
            wait_for(lambda: logs().count('control socket ready') > ready_count, 'control socket readiness')
            # The fixture replaces every occurrence of libation with TARGET,
            # including the job label name.
            compose('exec', '-T', 'scheduler', '/container-scheduler', 'run-now', TARGET)
            wait_for(lambda: 'manual run requested; timer reset' in logs(), 'targeted manual request')
            wait_for(running, 'manual replacement container start')
            manual = next(json.loads(line[line.index('{'):]) for line in logs().splitlines()
                          if 'manual run requested; timer reset' in line)
            delay = (datetime.fromisoformat(manual['next']) -
                     datetime.fromisoformat(manual['time'])).total_seconds()
            assert 3.5 < delay <= 4, ('manual timer reset', delay)

            compose('stop', 'scheduler-socket-proxy')
            wait_for(lambda: 'scheduled start failed' in logs(), 'proxy outage logging')
            compose('start', 'scheduler-socket-proxy')
            wait_for(lambda: not running(), 'replacement completion')
            wait_for(running, 'recovery after proxy outage')

            # Probe denied API writes from the allowed scheduler IP by sharing its
            # network namespace. A distinct source IP would only test allowfrom.
            sid = compose('ps', '-q', 'scheduler')
            probe = '''import http.client,sys
for method,path in [("POST","/containers/create"),("POST","/containers/other/start"),("POST","/containers/NAME/exec"),("DELETE","/containers/NAME")]:
 c=http.client.HTTPConnection("scheduler-socket-proxy",2375,timeout=5)
 c.request(method,path,body="{}",headers={"Content-Type":"application/json"})
 r=c.getresponse()
 assert r.status in (403,405), (path,r.status,r.read())
 c.close()
'''.replace('NAME', TARGET)
            docker('run', '--rm', '--network', 'container:' + sid, 'python:3.13-alpine', 'python', '-c', probe)
            state = json.loads(docker('inspect', sid))[0]
            assert state['RestartCount'] == 0, 'Scheduler crashed or restarted'
            assert state['Config']['User'] == '65532:65532'
            assert state['State']['Health']['Status'] == 'healthy', state['State']['Health']
            print('Docker smoke tests passed')
        finally:
            print(compose('logs', '--no-color'))
            compose('down', '--remove-orphans')
            docker('rm', '-f', TARGET, check=False)


if __name__ == '__main__':
    main()
