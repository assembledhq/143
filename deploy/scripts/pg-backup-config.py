#!/usr/bin/env python3
"""Generate or atomically install data-only offsite configuration; never eval it."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import sys

sys.dont_write_bytecode = True


def validate(data):
    fields = {'bucket', 'region', 'access_key_id', 'secret_access_key'}
    if not isinstance(data, dict) or set(data) != fields or not all(isinstance(v, str) and v for v in data.values()):
        raise ValueError('all four storage settings are required')
    if not re.fullmatch(r'[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]', data['bucket']):
        raise ValueError('invalid bucket')
    if not re.fullmatch(r'[a-z0-9-]+', data['region']):
        raise ValueError('invalid region')
    return data


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['generate', 'install'])
    parser.add_argument('--path', default='/opt/143/backup-storage.json')
    args = parser.parse_args()
    if args.action == 'generate':
        names = ['BACKUP_S3_BUCKET', 'BACKUP_S3_REGION', 'BACKUP_AWS_ACCESS_KEY_ID', 'BACKUP_AWS_SECRET_ACCESS_KEY']
        values = [os.environ.get(key, '') for key in names]
        print(json.dumps(validate(dict(zip(['bucket', 'region', 'access_key_id', 'secret_access_key'], values)))))
    else:
        data = validate(json.loads(sys.stdin.read(1024 * 1024)))
        path = Path(args.path)
        if path.parent.resolve() != path.parent.absolute() or path.is_symlink():
            raise ValueError('configuration path contains a symlink')
        spec = importlib.util.spec_from_file_location('policy', Path(__file__).with_name('pg-backup-policy.py'))
        policy = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(policy)
        policy.atomic_json(path, data)


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError) as exc:
        print('ERROR: backup storage configuration refused: ' + str(exc), file=sys.stderr)
        sys.exit(1)
