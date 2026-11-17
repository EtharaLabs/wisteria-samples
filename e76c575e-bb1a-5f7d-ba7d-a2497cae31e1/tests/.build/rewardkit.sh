#!/bin/sh
# Reward Kit's console entry point, rewardkit.__main__:main, installed system-wide in the verifier
# image from the hash-locked manifest beside this file (harbor-rewardkit 0.2.4).
exec /usr/bin/python3 -c 'import sys; from rewardkit.__main__ import main; sys.exit(main())' "$@"
