#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Starts the monitor and opens the panel.
#
# A script rather than "just run the binary" because the first thing a person
# needs is the address and the password, and a binary double-clicked from a
# file manager prints both into a window that closes.
set -eu

cd "$(dirname "$0")"

# The binary is beside this script; the data is not. See internal/app.DataDir:
# an archive unpacked into a read-only place still has somewhere to write.
exec ./wbmon "$@"
