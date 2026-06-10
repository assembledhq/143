#!/usr/bin/env python3
"""Script to fix migration numbering and push to remote."""
import subprocess
import os
import sys

def run(cmd, **kwargs):
    """Run a command and return stdout."""
    result = subprocess.run(cmd, capture_output=True, text=True, **kwargs)
    print(f"CMD: {' '.join(cmd)}")
    print(f"STDOUT: {result.stdout}")
    print(f"STDERR: {result.stderr}")
    print(f"RC: {result.returncode}")
    return result

os.chdir('/home/sandbox/143')

# Check git status
run(['git', 'status'])
run(['git', 'status', '--porcelain'])
run(['git', 'diff', '--name-status', 'HEAD'])
run(['git', 'diff', '--cached', '--name-status'])
