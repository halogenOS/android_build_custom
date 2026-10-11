#!/bin/bash
# Copyright (C) 2026 The halogenOS Project
# SPDX-License-Identifier: Apache-2.0
#
# Cleans the two signal headers of an installed kernel header tree for bionic: runs bionic's kernel
# header cleaner in its update mode on usr/include/asm/signal.h and usr/include/asm-generic/signal.h
# below the given directory. The working directory is the top of the source tree, which the cleaner
# reads from ANDROID_BUILD_TOP; the one argument is the directory a headers_install wrote into.

set -u

script=$(basename "$0")
if [ $# -ne 1 ]; then
  echo "$script: takes exactly one argument, the directory of the installed headers, and got $#" >&2
  exit 1
fi
headers=$1
if [ ! -d "$headers" ]; then
  echo "$script: $headers is not a directory" >&2
  exit 1
fi
case $headers in
  /*) ;;
  *) headers=$PWD/$headers ;;
esac

files=()
for file in usr/include/asm/signal.h usr/include/asm-generic/signal.h; do
  if [ ! -f "$headers/$file" ]; then
    echo "$script: $headers/$file is missing or is no regular file" >&2
    exit 1
  fi
  files+=("$headers/$file")
done

ANDROID_BUILD_TOP=$PWD exec bionic/libc/kernel/tools/clean_header.py -u "${files[@]}"
