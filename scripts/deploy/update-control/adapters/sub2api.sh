#!/usr/bin/env bash
set -Eeuo pipefail

printf '%s\n' 'ERROR: Sub2API production adapter is disabled pending explicit approval, migration verification, authenticated smoke and rollback rehearsal' >&2
exit 78
