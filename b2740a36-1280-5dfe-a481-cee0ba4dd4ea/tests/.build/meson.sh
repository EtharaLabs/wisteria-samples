#!/bin/sh
# meson, installed system-wide from the hash-locked manifest beside this file (meson 1.11.2), run as
# a module so that the build files it writes call it back the same way.
exec /usr/bin/python3 -m mesonbuild.mesonmain "$@"
