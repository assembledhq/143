"""Private, atomic backup evidence and bounded command helpers."""
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

class Refused(RuntimeError):
    pass


def require(ok, message):
    if not ok:
        raise Refused(message)


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def emit(event, **fields):
    print(json.dumps(dict(at=now(), event=event, **fields)), flush=True)


def identity(path):
    s = path.lstat()
    require(stat.S_ISREG(s.st_mode) and s.st_nlink == 1, 'unsafe file: ' + path.name)
    require(s.st_uid == os.geteuid() and not s.st_mode & 0o077, 'file must be private and owned by operator: ' + path.name)
    return dict(device=s.st_dev, inode=s.st_ino, bytes=s.st_size, mtime_ns=s.st_mtime_ns, ctime_ns=s.st_ctime_ns)


def read_json(path):
    identity(path)
    require(path.stat().st_size < 1024 * 1024, 'oversized JSON record')
    def unique_fields(pairs):
        data = {}
        for key, value in pairs:
            require(key not in data, 'duplicate JSON field')
            data[key] = value
        return data
    return json.loads(path.read_text(), object_pairs_hook=unique_fields)


def scheduled_backup_profile(state, current=None):
    """Read explicit private trial or ongoing approval, without claiming attendance.

    Code/cron installation never creates or renews this profile. Expiry blocks
    new starts, not completion or cleanup of an already admitted operation.
    """
    profile = read_json(state / 'scheduled-backup.json')
    require(isinstance(profile, dict), 'invalid scheduled backup profile')
    require(type(profile.get('schema')) is int and profile['schema'] in (1, 2), 'invalid schedule schema')
    fields = {'schema', 'owner', 'evidence', 'starts_at',
              'host_memory_full_percent', 'db_memory_full_percent'}
    fields |= {'expires_at'} if profile['schema'] == 1 else {'mode', 'timezone', 'hours'}
    require(set(profile) == fields, 'invalid scheduled backup profile fields')
    for key in ('owner', 'evidence'):
        require(isinstance(profile[key], str) and 0 < len(profile[key].strip()) <= 1024,
                'scheduled backup requires ' + key)
    for key in ('host_memory_full_percent', 'db_memory_full_percent'):
        require(type(profile[key]) is int and profile[key] in (1, 5), 'invalid scheduled pressure limit')
    times = []
    for key in (('starts_at', 'expires_at') if profile['schema'] == 1 else ('starts_at',)):
        require(isinstance(profile[key], str), 'invalid schedule timestamp')
        parsed = dt.datetime.fromisoformat(profile[key])
        require(parsed.tzinfo is not None, 'schedule timestamp requires timezone')
        times.append(parsed.timestamp())
    current = dt.datetime.now(dt.timezone.utc).timestamp() if current is None else current
    if profile['schema'] == 1:
        start, end = times
        require(0 < end - start <= 24 * 3600, 'scheduled backup window must be at most 24 hours')
        require(start <= current < end, 'scheduled backup window is not active')
    else:
        require(profile['mode'] == 'ongoing', 'ongoing schedule requires explicit mode')
        zone = profile['timezone']
        require(isinstance(zone, str) and 0 < len(zone) <= 128, 'invalid schedule timezone')
        try:
            ZoneInfo(zone)
        except (ValueError, ZoneInfoNotFoundError) as exc:
            raise Refused('schedule timezone is unavailable') from exc
        hours = profile['hours']
        require(isinstance(hours, list) and 1 <= len(hours) <= 24 and
                all(type(h) is int and 0 <= h <= 23 for h in hours), 'invalid schedule hours')
        require(hours == sorted(set(hours)), 'schedule hours must be unique and sorted')
        require(times[0] <= current, 'scheduled backup window is not active')
    return profile


def scheduled_backup_due(profile, current=None):
    """An hourly cron tick may start ongoing work only in an approved local hour.

    Accept only an already validated profile. Trial timing stays controlled by
    its existing cron expression; ongoing profiles use timezone-aware hours.
    """
    if profile['schema'] == 1:
        return True
    current = dt.datetime.now(dt.timezone.utc).timestamp() if current is None else current
    return dt.datetime.fromtimestamp(current, ZoneInfo(profile['timezone'])).hour in profile['hours']


def sync_dir(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def atomic_json(path, data):
    fd, temporary = tempfile.mkstemp(prefix='.' + path.name, dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump(data, f, sort_keys=True)
            f.write('\n')
            f.flush()
            os.fsync(f.fileno())
        os.replace(temporary, path)
        sync_dir(path.parent)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def sha256(path, *, tick=None, drop_cache=False):
    before = identity(path)
    h = hashlib.sha256()
    with path.open('rb') as f:
        if drop_cache:
            require(hasattr(os, 'posix_fadvise'), 'cache-aware hashing requires Linux posix_fadvise')
            os.fsync(f.fileno())
            os.posix_fadvise(f.fileno(), 0, 0, os.POSIX_FADV_DONTNEED)
            os.posix_fadvise(f.fileno(), 0, 0, os.POSIX_FADV_SEQUENTIAL)
        offset = 0
        while True:
            if tick:
                tick()
            block = f.read(8 * 1024 * 1024)
            if not block:
                break
            h.update(block)
            if drop_cache:
                os.posix_fadvise(f.fileno(), offset, len(block), os.POSIX_FADV_DONTNEED)
            offset += len(block)
    require(identity(path) == before, 'archive changed while hashing')
    return h.hexdigest()


def invoke(args, *, env=None, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, timeout=120):
    # Do not include raw command output/credentials in an exception or receipt.
    result = subprocess.run(args, env=env, stdin=stdin, stdout=stdout,
                            stderr=subprocess.PIPE, timeout=timeout, check=False)
    require(result.returncode == 0, 'command failed: ' + Path(args[0]).name + ' (exit ' + str(result.returncode) + ')')
    return (result.stdout or b'').decode()
