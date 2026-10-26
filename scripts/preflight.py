#!/usr/bin/env python3
"""Select local checks from an explicit diff or Git's pre-push ref updates."""
import argparse
import os
import shlex
import subprocess
import sys


def git(*args):
    return subprocess.check_output(['git', *args], text=True).rstrip('\n')


def changed(base, head=None):
    # Include both sides of renames and deleted files in routing.
    args = ['diff', '--no-renames', '--name-only', '-z', base]
    if head:
        args.append(head)
    return set(filter(None, git(*args).split('\0')))


def push_ranges(lines):
    ranges = []
    head = git('rev-parse', 'HEAD')
    for line in lines:
        local_ref, local_sha, remote_ref, remote_sha = line.split()
        if set(local_sha) == {'0'}:
            continue  # Deleting a ref does not publish code.
        if not remote_ref.startswith('refs/heads/'):
            raise ValueError('Preflight supports branch pushes only; validate tag publications separately.')
        if local_sha != head:
            raise ValueError('Check out the branch being pushed; preflight must validate its exact HEAD.')
        if set(remote_sha) == {'0'}:
            # New branches have no destination tip. Compare with the main fork
            # point; require that reference instead of silently skipping checks.
            try:
                base = git('merge-base', 'HEAD', 'refs/remotes/origin/main')
            except subprocess.CalledProcessError as exc:
                raise ValueError('Fetch origin/main before publishing a new branch.') from exc
        else:
            try:
                git('cat-file', '-e', remote_sha + '^{commit}')
            except subprocess.CalledProcessError as exc:
                raise ValueError('Fetch the destination branch before pushing; its tip is missing locally.') from exc
            base = remote_sha
        ranges.append((base, local_sha))
    return ranges


def plan(files, bases, extended=False):
    shared_inputs = {
        'Makefile', 'go.mod', 'go.sum', '.golangci.yml',
        'scripts/preflight.py', 'scripts/test_preflight.py', 'setup.sh',
    }
    broad = any(
        f in shared_inputs or f.startswith(('.github/workflows/', '.github/scripts/', '.githooks/'))
        for f in files
    )
    backend = broad or any(f.endswith('.go') or f.startswith('migrations/') for f in files)
    frontend = broad or any(f.startswith('frontend/') for f in files)
    deploy = broad or any(f.startswith(('deploy/', 'Dockerfile', 'docker-compose')) for f in files)
    commands = []
    if backend:
        commands.append((['make', 'lint'], '.'))
    if frontend:
        commands += [(['npm', 'run', 'typecheck'], 'frontend'), (['npm', 'run', 'lint'], 'frontend')]
        full = broad or any(
            f.startswith(('frontend/package', 'frontend/src/test/', 'frontend/docs/', 'frontend/src/app/'))
            or 'config.' in f or f.endswith(('.md', '.mdx'))
            for f in files
        )
        if full:
            commands += [(['npm', 'run', 'build'], 'frontend'), (['npx', '--no-install', 'vitest', 'run'], 'frontend')]
        else:
            for base in sorted(set(bases)):
                commands.append((['npx', '--no-install', 'vitest', 'run', '--changed=' + base, '--passWithNoTests'], 'frontend'))
    if broad:
        commands += [(['python3', '-B', '-m', 'unittest', 'discover', '-s', '.github/scripts', '-p', 'test_*.py'], '.'),
                     (['python3', '-B', '-m', 'unittest', 'discover', '-s', 'scripts', '-p', 'test_preflight.py'], '.')]
    if deploy:
        commands += [(['go', 'test', './deploy', '-count=1', '-timeout=120s'], '.'), (['go', 'vet', './deploy'], '.'), (['make', 'test-deploy-scripts'], '.')]
    if extended:
        if backend:
            commands += [(['make', 'security-check'], '.'), (['make', 'test-pr'], '.')]
        if frontend:
            commands.append((['npm', 'run', 'test:session-transcript-e2e'], 'frontend'))
    return commands, backend


def check_environment():
    # Hooks export repository-local Git state. Child checks may create their
    # own repositories; let each command discover its repository from cwd.
    local_names = set(git('rev-parse', '--local-env-vars').splitlines())
    return {key: value for key, value in os.environ.items() if key not in local_names}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base', default=os.environ.get('PREFLIGHT_BASE', 'origin/main'))
    parser.add_argument('--push', action='store_true')
    parser.add_argument('--extended', action='store_true')
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args()
    os.chdir(git('rev-parse', '--show-toplevel'))
    checked_head = git('rev-parse', 'HEAD')
    if args.push:
        ranges = push_ranges(sys.stdin)
        if not ranges:
            return
        if git('status', '--porcelain', '--untracked-files=normal'):
            raise ValueError('Commit or stash changes before pushing so checks validate the outgoing HEAD.')
        files = set().union(*(changed(base, head) for base, head in ranges))
        bases = [base for base, _ in ranges]
    else:
        base = git('rev-parse', '--verify', args.base + '^{commit}')
        files = changed(base) | set(filter(None, git('ls-files', '--others', '--exclude-standard', '-z').split('\0')))
        bases = [base]
    commands, backend = plan(files, bases, args.extended)
    environment = check_environment()
    for command, cwd in commands:
        print(f'[preflight] {cwd}: {shlex.join(command)}', flush=True)
        if not args.dry_run:
            subprocess.run(command, cwd=cwd, env=environment, check=True, stdin=subprocess.DEVNULL)
    if args.push and (git('rev-parse', 'HEAD') != checked_head or git('status', '--porcelain', '--untracked-files=normal')):
        raise ValueError('Checkout changed during preflight; commit or stash changes and rerun the push.')
    if backend and not args.extended:
        print('[preflight] Full security/database/browser checks not run. Run focused Go tests; use --extended with disposable test services for broader validation.')
    print('[preflight] Plan complete.' if args.dry_run else '[preflight] Selected checks passed; hosted CI remains required.')


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f'[preflight] {exc}', file=sys.stderr)
        sys.exit(1)
