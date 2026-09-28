#!/usr/bin/env python3
"""Opt-in monitoring installation; never writes the backup/restore schedule."""
import argparse
import os
from pathlib import Path
import re
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parent))
from pg_backup_monitor import CONFIG, BACKUP_CRON, validate_config, schedule_settings
from pg_backup_state import atomic_json, require


def write(path, content, mode):
    require(not path.is_symlink(), 'refusing symlink destination')
    if path.exists() and path.read_text() == content and path.stat().st_mode & 0o777 == mode:
        return
    fd, name = tempfile.mkstemp(prefix='.' + path.name + '.', dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as f:
            f.write(content)
            f.flush()
            os.fsync(f.fileno())
        os.chmod(name, mode)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def install(args):
    config = validate_config(dict(logging_host=args.logging_host, monitor_id=args.monitor_id))
    schedule_settings(args.backup_cron)
    for path in (args.scripts_dir, args.config, args.backup_cron, args.cron_file, args.log, args.logrotate_file):
        require(re.fullmatch(r'/[a-zA-Z0-9_./-]+', str(path)) and '..' not in path.parts,
                'installation paths must be absolute literal paths')
        require(path.resolve() == path.absolute(), 'installation paths must not contain symlinks')
    for path in (args.config, args.cron_file, args.log, args.logrotate_file):
        if path.exists():
            require(path.is_file() and path.stat().st_nlink == 1 and path.stat().st_uid == os.geteuid(),
                    'monitoring destination must be a single-link owned file')
    require(len({args.config, args.backup_cron, args.cron_file, args.log, args.logrotate_file}) == 5,
            'installation paths must be distinct')
    for name in ('pg_backup_monitor.py', 'pg_backup_health.py', 'pg_backup_state.py'):
        require((args.scripts_dir / name).is_file(), 'copy monitoring helpers before installation')
    # No helper is executed and no delivery is attempted during installation.
    # Publish cron last; an interrupted installation cannot schedule a partial config.
    require(not args.config.is_symlink() and not args.log.is_symlink(), 'unsafe monitoring destination')
    atomic_json(args.config, config)
    fd = os.open(args.log, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    os.close(fd)
    args.log.chmod(0o600)
    write(args.logrotate_file, f'''{args.log} {{
    daily
    maxsize 1M
    rotate 7
    compress
    missingok
    notifempty
    create 0600 root root
}}
''', 0o644)
    write(args.cron_file, f'''# Managed by install-pg-backup-monitoring.py. Independent of backup/restore holds.
SHELL=/bin/sh
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
* * * * * root /usr/bin/timeout --kill-after=5s 25s /usr/bin/python3 -I -B {args.scripts_dir}/pg_backup_monitor.py --config {args.config} --backup-cron {args.backup_cron} >> {args.log} 2>&1
''', 0o644)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--logging-host', required=True)
    parser.add_argument('--monitor-id', required=True)
    for name, default in dict(scripts_dir='/opt/143/deploy/scripts', config=CONFIG, backup_cron=BACKUP_CRON,
                              cron_file='/etc/cron.d/143-pg-backup-health', log='/var/log/pg-backup-health.log',
                              logrotate_file='/etc/logrotate.d/143-pg-backup-health').items():
        parser.add_argument('--' + name.replace('_', '-'), type=Path, default=Path(default))
    args = parser.parse_args()
    require(os.geteuid() == 0, 'run the installer as root')
    require(Path('/usr/bin/timeout').is_file(), 'GNU timeout is required')
    require(Path('/usr/sbin/logrotate').is_file(), 'logrotate is required')
    install(args)
    print('Backup health collection installed; backup and restore schedules were not changed.')


if __name__ == '__main__':
    main()
