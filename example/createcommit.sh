#!/bin/sh
set -e
date -u --rfc-3339 ns > dummy.txt
git add dummy.txt
git commit -m"update dummy.txt"
