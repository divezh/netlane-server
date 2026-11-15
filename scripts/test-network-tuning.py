"""Exercise the updater's actual embedded tuner without changing host sysctls (run as root)."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = (Path(__file__).resolve().parent / 'netlane-update.sh').read_text(encoding='utf-8')
CODE = SCRIPT.split("<<'NETLANE_NETWORK_PY'\n", 1)[1].split('\nNETLANE_NETWORK_PY', 1)[0]
MODULE = {'__name__': 'network_tuning_test'}
exec(compile(CODE, 'netlane-update.sh:network', 'exec'), MODULE)


class NetworkTuning(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='netlane-network-test-', dir='/root')
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / '92-netlane-udp.conf'
        self.values = dict.fromkeys(MODULE['MINIMUM'], 212992)
        self.applied = 0
        self.denied = False
        self.ignored = False

    def run_sysctl(self, argv, **kwargs):
        if argv[1] == '-n':
            return subprocess.CompletedProcess(argv, 0, str(self.values[argv[2]]))
        self.assertEqual(argv, ['sysctl', '-q', '-p', str(self.path)])
        self.applied += 1
        if self.denied:
            raise subprocess.CalledProcessError(1, argv)
        if self.ignored:
            return subprocess.CompletedProcess(argv, 0)
        for line in self.path.read_text().splitlines():
            if '=' in line:
                key, value = line.split('=')
                key, value = key.strip(), int(value)
                if key.endswith('_default'):
                    self.assertLessEqual(value, self.values[key.replace('_default', '_max')])
                self.values[key] = value
        return subprocess.CompletedProcess(argv, 0)

    def configure(self):
        MODULE['configure'](self.path, self.run_sysctl)

    def test_fresh_install_and_idle_timer_are_idempotent(self):
        self.configure()
        self.assertEqual(self.values, MODULE['MINIMUM'])
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o644)
        modified = self.path.stat().st_mtime_ns
        self.configure()
        self.assertEqual(self.applied, 1)
        self.assertEqual(self.path.stat().st_mtime_ns, modified)

    def test_preserves_higher_runtime_and_saved_values(self):
        self.values['net.core.rmem_default'] = 16000000
        self.path.write_text('net.core.wmem_max = 32000000\n')
        self.configure()
        self.assertEqual(self.values['net.core.rmem_default'], 16000000)
        self.assertEqual(self.values['net.core.rmem_max'], 16000000)
        self.assertEqual(self.values['net.core.wmem_max'], 32000000)
        # A reboot or another tool lowered the live values; the saved settings recover them.
        self.values = dict.fromkeys(self.values, 212992)
        self.configure()
        self.assertEqual(self.values['net.core.rmem_default'], 16000000)

    def test_denied_kernel_keeps_persistent_config_for_retry(self):
        self.denied = True
        with self.assertRaises(subprocess.CalledProcessError):
            self.configure()
        self.assertIn('net.core.rmem_default = 4194304', self.path.read_text())
        self.denied = False
        self.configure()
        self.assertEqual(self.values, MODULE['MINIMUM'])

    def test_refuses_symlink_and_writable_config(self):
        target = self.path.with_suffix('.target')
        target.write_text('untouched')
        self.path.symlink_to(target)
        with self.assertRaises(ValueError):
            self.configure()

        self.assertEqual(target.read_text(), 'untouched')
        self.path.unlink()
        self.path.write_text('')
        self.path.chmod(0o666)
        with self.assertRaises(ValueError):
            self.configure()

    def test_detects_kernel_ignoring_writes_despite_zero_exit(self):
        self.ignored = True
        with self.assertRaisesRegex(ValueError, 'hosting kernel'):
            self.configure()

    def test_timer_tunes_before_request_check(self):
        self.assertLess(SCRIPT.index('then configure_network; fi'), SCRIPT.index('if ((requested)); then'))
        self.assertIn('except (OSError, ValueError, subprocess.SubprocessError)', CODE)


if __name__ == '__main__':
    assert os.geteuid() == 0, 'Use sudo: tests use a root-owned temporary directory, never real sysctls.'
    unittest.main()
