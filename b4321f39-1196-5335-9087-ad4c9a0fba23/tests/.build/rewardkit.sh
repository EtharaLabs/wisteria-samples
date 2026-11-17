#!/bin/sh
# Reward Kit's console entry point, rewardkit.__main__:main, for the CPython 3.13 of the verifier
# image; the package and its dependencies come from the hash-locked manifest beside this file
# (harbor-rewardkit 0.2.4).
exec /usr/local/bin/python3.13 -c 'import sys; from rewardkit.__main__ import main; sys.exit(main())' "$@"
