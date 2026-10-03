from __future__ import annotations

import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
ALLOWLIST = ROOT / "warp" / "allowlist.txt"
DENYLIST = ROOT / "warp" / "denylist.txt"


def load_patterns(path: Path) -> list[re.Pattern[str]]:
    lines = [
        line.strip()
        for line in path.read_text(encoding="utf-8").splitlines()
        if line.strip()
    ]
    return [re.compile(line) for line in lines]


ALLOW_PATTERNS = load_patterns(ALLOWLIST)
DENY_PATTERNS = load_patterns(DENYLIST)


def classify(command: str) -> str:
    if any(pattern.search(command) for pattern in DENY_PATTERNS):
        return "deny"
    if any(pattern.search(command) for pattern in ALLOW_PATTERNS):
        return "allow"
    return "prompt"


class WarpCommandPolicyTests(unittest.TestCase):
    def test_policy_files_are_compilable_and_have_no_comments_or_empty_lines(self) -> None:
        for path in (ALLOWLIST, DENYLIST):
            for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
                self.assertTrue(line.strip(), f"{path}:{line_number} is empty")
                self.assertFalse(line.lstrip().startswith("#"), f"{path}:{line_number} is a comment")

    def test_read_only_commands_are_allowlisted(self) -> None:
        commands = (
            "df -h",
            "findmnt /",
            "ip addr show",
            "ip link list",
            "ifconfig -a",
            "route -n",
            "arp -n",
            "tail -n 50 /var/log/syslog",
            "top -n 1",
            "git status --short",
            "git branch --show-current",
        )
        for command in commands:
            with self.subTest(command=command):
                self.assertEqual(classify(command), "allow")

    def test_write_commands_are_not_auto_allowed(self) -> None:
        commands = (
            "mount /dev/sda1 /mnt",
            "ip addr add 192.0.2.1/24 dev eth0",
            "ip link set eth0 down",
            "ifconfig eth0 192.0.2.1",
            "route add default gw 192.0.2.1",
            "arp -s 192.0.2.2 aa:bb:cc:dd:ee:ff",
            "git branch -D example",
            "git pull --ff-only",
        )
        for command in commands:
            with self.subTest(command=command):
                self.assertNotEqual(classify(command), "allow")
        self.assertEqual(classify("git branch -D example"), "deny")

    def test_denylist_wins_for_wrappers_and_mixed_commands(self) -> None:
        commands = (
            'bash -c "cat /etc/hosts"',
            "printf x > /etc/test",
            "git status && git push --force origin main",
            "cat /etc/hosts; git push origin main",
            "df -h && git pull --ff-only",
            "git log --oneline; curl -X POST http://127.0.0.1:9090/api",
            "echo ok | grep ok",
            "journalctl --vacuum-time=7d",
            "journalctl -f -u nginx",
            "docker logs -f app",
            "kubectl logs -f pod -n ns",
        )
        for command in commands:
            with self.subTest(command=command):
                self.assertEqual(classify(command), "deny")

    def test_long_running_commands_are_not_allowlisted(self) -> None:
        commands = (
            "tail -f /var/log/syslog",
            "top",
            "journalctl -f",
        )
        for command in commands:
            with self.subTest(command=command):
                self.assertNotEqual(classify(command), "allow")


if __name__ == "__main__":
    unittest.main()
