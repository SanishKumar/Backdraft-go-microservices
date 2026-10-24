"""Regression checks for smoke-test readiness after an overload experiment."""

import unittest
from unittest.mock import patch

from smoke import wait_for_idle


def sample(tick, *, queued=0, active=0, attempts=0, offered=0,
           connected=True, missing=False, fault=False):
    return {
        "state": {
            "services": [{"name": "catalog", "instances": int(connected)}],
            "faults": {"catalog": fault},
        },
        "live": {
            "t": tick,
            "entry": {"offered": offered},
            "services": {} if missing else {"catalog": {"queued": queued, "inFlight": active}},
            "edges": {"gateway>catalog": {"attempts": attempts}},
        },
    }


class ReadinessTests(unittest.TestCase):
    def run_samples(self, samples, timeout=8):
        self.polls = 0
        elapsed = 0

        def sleep(seconds):
            nonlocal elapsed
            elapsed += seconds

        def request(url):
            current = samples[min(self.polls, len(samples) - 1)]
            if url.endswith("/state"):
                return current["state"]
            self.assertTrue(url.endswith("/live"))
            self.polls += 1
            return current["live"]

        with patch("smoke.time.monotonic", side_effect=lambda: elapsed), \
                patch("smoke.time.sleep", side_effect=sleep):
            wait_for_idle(request, "http://lab", {"catalog"}, timeout)

    def test_waits_for_backlog_and_three_quiet_samples(self):
        self.run_samples([
            sample(1, queued=300000, active=8),
            sample(2, queued=12, active=8),
            sample(3), sample(4), sample(5),
        ])
        self.assertEqual(self.polls, 5)

    def test_cached_idle_snapshot_does_not_count_as_recovery(self):
        with self.assertRaisesRegex(RuntimeError, "lab did not drain"):
            self.run_samples([sample(1)])

    def test_work_faults_and_missing_agents_break_idle_streak(self):
        for condition in ({"queued": 1}, {"active": 1}, {"attempts": 1},
                          {"offered": 1}, {"connected": False},
                          {"missing": True}, {"fault": True}):
            with self.subTest(condition=condition):
                self.run_samples([
                    sample(1), sample(2, **condition),
                    sample(3), sample(4), sample(5),
                ])
                self.assertEqual(self.polls, 5)

    def test_recovery_timeout_reports_backlog(self):
        with self.assertRaisesRegex(RuntimeError, "300000 queued, 8 in flight"):
            self.run_samples([sample(1, queued=300000, active=8)])


if __name__ == "__main__":
    unittest.main()
