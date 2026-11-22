"""Shell-level tests for scripts/storage_restore_drill.sh guards. No docker.

The script is sourced in a bash subshell; its source-guard stops execution
before the main flow, exposing the guard functions. A stub `docker` function
stands in for the real binary where a guard shells out.
Run: python3 -m pytest scripts/tests -q
"""
import subprocess
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'scripts' / 'storage_restore_drill.sh'


def bash(code):
    """Source the drill script, then run code under its set -euo pipefail."""
    proc = subprocess.run(['bash', '-c', f'source "{SCRIPT}"\n' + code],
                          cwd=ROOT, capture_output=True, text=True)
    return proc.returncode, proc.stdout + proc.stderr


class TestCheckDrillProject:
    @pytest.mark.parametrize('project', [
        'hardening-drill',                      # the old fixed drill name
        'hardening-drill-20261122120000-123',   # a generated per-run name
        'hardening-drill-x',
    ])
    def test_source_in_drill_namespace_refused(self, project):
        rc, out = bash(f'rc=0; check_drill_project {project} || rc=$?; echo rc=$rc')
        assert rc == 0 and 'rc=1' in out and 'collides' in out

    @pytest.mark.parametrize('project', ['storzo', 'hardening', 'prod'])
    def test_normal_source_accepted(self, project):
        rc, out = bash(f'check_drill_project {project} && echo ok')
        assert rc == 0 and 'ok' in out


class TestCheckDrillVolume:
    def test_same_volume_refused(self):
        rc, out = bash('rc=0; check_drill_volume vol1 p1 vol1 || rc=$?; echo rc=$rc')
        assert rc == 0 and 'rc=1' in out and 'equals source volume' in out

    def test_empty_drill_volume_refused(self):
        rc, out = bash('rc=0; check_drill_volume vol1 p1 "" || rc=$?; echo rc=$rc')
        assert rc == 0 and 'rc=1' in out

    def test_foreign_prefix_refused(self):
        rc, out = bash('rc=0; check_drill_volume src_vol p1 other_storage-data || rc=$?; echo rc=$rc')
        assert rc == 0 and 'rc=1' in out and 'prefix' in out

    def test_own_volume_accepted(self):
        rc, out = bash('check_drill_volume src_vol p1 p1_storage-data && echo ok')
        assert rc == 0 and 'ok' in out


class TestDrillNameFree:
    STUB = '''
docker() {
  if [[ "$1" == ps ]]; then printf '%s' "$STUB_PS"; return 0; fi
  if [[ "$1" == volume ]]; then printf '%s' "$STUB_VOL"; return 0; fi
  return 0
}
'''

    def test_existing_project_refused(self):
        rc, out = bash(self.STUB + 'STUB_PS=cid123 STUB_VOL=""\n'
                       'rc=0; drill_name_free hardening-drill-t-1 || rc=$?; echo rc=$rc')
        assert rc == 0 and 'rc=1' in out and 'already exists' in out

    def test_existing_volume_refused(self):
        rc, out = bash(self.STUB + 'STUB_PS="" STUB_VOL="hardening-drill-t-1_storage-data"\n'
                       'rc=0; drill_name_free hardening-drill-t-1 || rc=$?; echo rc=$rc')
        assert rc == 0 and 'rc=1' in out and 'already exists' in out

    def test_free_name_accepted(self):
        rc, out = bash(self.STUB + 'STUB_PS="" STUB_VOL="unrelated_storage-data"\n'
                       'drill_name_free hardening-drill-t-1 && echo ok')
        assert rc == 0 and 'ok' in out


class TestStopOrdering:
    def test_flag_armed_before_stop_even_when_stop_fails(self):
        # Stub docker reports the flag value AT stop time, then fails:
        # recovery must already be armed (comment 4). The script redirects
        # stop's stdout, so the stub logs to a file.
        rc, out = bash('''
CALLS="$(mktemp)"
docker() {
  if [[ "$1" == compose && "$4" == stop ]]; then
    echo "flag-at-stop=$SOURCE_STOPPED" >> "$CALLS"
    return 1
  fi
  return 0
}
PROJECT=hardening
SOURCE_STOPPED=0
rc=0; stop_source_storage || rc=$?
cat "$CALLS"; rm -f "$CALLS"
echo "rc=$rc flag=$SOURCE_STOPPED"
''')
        assert rc == 0
        assert 'flag-at-stop=1' in out
        assert 'rc=1 flag=1' in out

    def test_successful_stop_keeps_flag(self):
        rc, out = bash('''
docker() { return 0; }
PROJECT=hardening
SOURCE_STOPPED=0
stop_source_storage
echo "flag=$SOURCE_STOPPED"
''')
        assert rc == 0 and 'flag=1' in out


class TestCleanupRestart:
    SETUP = '''
WORK="$(mktemp -d)"
PROJECT=hardening
DRILL_PROJECT=hardening-drill-t-1
DRILL_UP=0
SEEDED=0
VOLUME=hardening_storage-data
DRILL_VOLUME=""
'''

    def test_restart_attempted_even_if_start_fails(self):
        # Harmless when storage was never stopped: compose start is a no-op,
        # and a failing start must not abort the rest of cleanup. The script
        # redirects start's output, so the stub logs to a file.
        rc, out = bash(self.SETUP + '''
SOURCE_STOPPED=1
CALLS="$(mktemp)"
docker() {
  if [[ "$1" == compose && "$4" == start ]]; then echo start-called >> "$CALLS"; return 1; fi
  return 0
}
( cleanup )
cat "$CALLS"; rm -f "$CALLS"
echo "work-exists=$([[ -d $WORK ]] && echo yes || echo no)"
''')
        assert rc == 0
        assert 'start-called' in out
        assert 'work-exists=no' in out

    def test_no_restart_when_never_stopped(self):
        rc, out = bash(self.SETUP + '''
SOURCE_STOPPED=0
CALLS="$(mktemp)"
docker() { echo "docker-called:$*" >> "$CALLS"; return 0; }
( cleanup )
cat "$CALLS"; rm -f "$CALLS"
''')
        assert rc == 0 and 'docker-called' not in out

    def test_teardown_refused_when_drill_volume_equals_source(self):
        rc, out = bash(self.SETUP + '''
SOURCE_STOPPED=0
DRILL_UP=1
DRILL_VOLUME=hardening_storage-data   # same as VOLUME: must NOT down -v
touch "$WORK/drill-compose.yaml"
CALLS="$(mktemp)"
docker() { echo "docker-called:$*" >> "$CALLS"; return 0; }
( cleanup )
cat "$CALLS"; rm -f "$CALLS"
''')
        assert rc == 0
        assert 'down' not in out
        assert 'refusing' in out